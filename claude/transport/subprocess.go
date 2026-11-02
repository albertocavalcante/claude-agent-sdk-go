package transport

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults for Subprocess.
const (
	DefaultGrace        = 3 * time.Second
	DefaultTermWait     = 5 * time.Second
	DefaultMaxLineBytes = 16 << 20
	DefaultStderrBytes  = 64 << 10
	DefaultDrainGrace   = 2 * time.Second
)

// Subprocess runs the claude CLI as a child process in its own process
// group and speaks NDJSON over its stdin and stdout.
//
// Close ends the process like this: close stdin, wait Grace for it to exit,
// SIGTERM the whole process group, wait TermWait, SIGKILL the group. The
// group is signalled even when the leader already exited, so MCP servers and
// Bash children do not outlive the session.
//
// Stdout is read by one goroutine into a small buffer; that goroutine blocks
// when nobody calls ReadLine, which in turn backpressures the CLI through
// the pipe. Stderr is never blocked on: it is drained into a capped ring.
type Subprocess struct {
	// Path is the executable; looked up on PATH if it has no separator.
	Path string
	// Args is the argv after the executable.
	Args []string
	// Dir is the working directory ("" = inherit).
	Dir string
	// Env is the base environment as KEY=VALUE; nil inherits os.Environ().
	Env []string
	// EnvUnset removes these keys after defaults are applied. CLAUDECODE is
	// always removed so a CLI spawned from inside Claude Code does not think
	// it is nested.
	EnvUnset []string
	// Grace is how long Close waits for exit after closing stdin (default 3s).
	Grace time.Duration
	// TermWait is how long Close waits after SIGTERM before SIGKILL (default 5s).
	TermWait time.Duration
	// MaxLineBytes caps one stdout line (default 16 MiB).
	MaxLineBytes int
	// StderrBytes caps the retained stderr tail (default 64 KiB).
	StderrBytes int
	// DrainGrace is how long after the process exited stdout may stay silent
	// while a descendant holds it open before it is force-closed with
	// ErrOutputTruncated (default 2s).
	DrainGrace time.Duration
	// OnStderr, if set, receives every complete stderr line. It must not block.
	OnStderr func(line string)

	mu      sync.Mutex
	started bool
	cmd     *exec.Cmd
	stdinW  *os.File
	stdoutR *os.File
	stderr  *tailBuffer
	lines   chan lineItem

	wmu         sync.Mutex // serialises writes
	inputClosed bool       // guarded by wmu

	exited   chan struct{}
	pumpDone chan struct{}
	inRead   atomic.Bool
	lastRead atomic.Int64
	forced   atomic.Bool
	done     chan struct{} // closed by Close
	status   ExitStatus
	waitEr   error

	closeOnce sync.Once
}

type lineItem struct {
	line []byte
	err  error
}

func (s *Subprocess) env() []string { return BuildEnv(s.Env, s.EnvUnset, s.Dir) }

// parentSessionVars identify the Claude Code session that launched this
// process. A CLI spawned from inside it must not inherit them, or it would
// believe it is that session (or its child) and talk to its sockets. User
// configuration such as CLAUDE_CONFIG_DIR and CLAUDE_CODE_OAUTH_TOKEN is not
// listed and stays unless the caller unsets it.
var parentSessionVars = []string{
	"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_BRIDGE_SESSION_ID",
	"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_PID", "CLAUDE_CODE_SESSION_ATTENDED",
	"CLAUDE_CODE_EXECPATH", "CLAUDE_CODE_SSE_PORT", "CLAUDE_CODE_PARENT_SESSION_ID",
}

// BuildEnv computes the child environment Subprocess uses, for transports
// that launch the CLI elsewhere (a runner) and must apply the same policy.
// base nil inherits os.Environ(). Defaults are added (CLAUDE_CODE_ENTRYPOINT
// and CLAUDE_CODE_SDK_READS_SESSION_STATE unless already present,
// CLAUDE_AGENT_SDK_VERSION always, PWD when dir is set), then unset keys and
// CLAUDECODE are removed.
func BuildEnv(base, unset []string, dir string) []string {
	inherited := base == nil
	if inherited {
		base = os.Environ()
	}
	env := append([]string(nil), base...)
	has := func(k string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, k+"=") {
				return true
			}
		}
		return false
	}
	set := func(k, v string) {
		for i, e := range env {
			if strings.HasPrefix(e, k+"=") {
				env[i] = k + "=" + v
				return
			}
		}
		env = append(env, k+"="+v)
	}
	// An entrypoint inherited from a parent Claude Code names that session's
	// entry, not ours; only one the caller passed explicitly is kept.
	if inherited || !has("CLAUDE_CODE_ENTRYPOINT") {
		set("CLAUDE_CODE_ENTRYPOINT", DefaultEntrypoint)
	}
	if !has("CLAUDE_CODE_SDK_READS_SESSION_STATE") {
		set("CLAUDE_CODE_SDK_READS_SESSION_STATE", "1")
	}
	set("CLAUDE_AGENT_SDK_VERSION", SDKVersion)
	if dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			set("PWD", abs) // the CLI reads PWD, not only its working directory
		}
	}
	drop := map[string]bool{}
	for _, k := range parentSessionVars {
		drop[k] = true
	}
	for _, k := range unset {
		drop[k] = true
	}
	out := env[:0]
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if !drop[k] {
			out = append(out, e)
		}
	}
	return out
}

// Start launches the process. ctx only bounds the launch; the process lives
// until Close (or until it exits on its own).
func (s *Subprocess) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("transport: already started")
	}
	path := s.Path
	if path == "" {
		path = "claude"
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return fmt.Errorf("transport: cannot find %s: %w", path, err)
	}
	if s.Grace <= 0 {
		s.Grace = DefaultGrace
	}
	if s.TermWait <= 0 {
		s.TermWait = DefaultTermWait
	}
	if s.MaxLineBytes <= 0 {
		s.MaxLineBytes = DefaultMaxLineBytes
	}
	if s.StderrBytes <= 0 {
		s.StderrBytes = DefaultStderrBytes
	}

	// The process must outlive Start's ctx, so it is bound to Background; Close ends it.
	cmd := exec.CommandContext(context.Background(), resolved, s.Args...) //nolint:gosec // launching the configured CLI is the purpose
	cmd.Dir = s.Dir
	cmd.Env = s.env()
	setProcAttr(cmd)
	s.stderr = newTailBuffer(s.StderrBytes, s.OnStderr)
	cmd.Stderr = s.stderr
	cmd.WaitDelay = time.Second // grandchildren holding stderr open must not stall Wait

	inR, inW, err := os.Pipe()
	if err != nil {
		return err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return err
	}
	cmd.Stdin = inR
	cmd.Stdout = outW
	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW} {
			_ = f.Close()
		}
		return fmt.Errorf("transport: start %s: %w", resolved, err)
	}
	_ = inR.Close()
	_ = outW.Close() // only the child (and its descendants) hold the write end now

	s.cmd, s.stdinW, s.stdoutR = cmd, inW, outR
	s.lines = make(chan lineItem, 32)
	s.exited = make(chan struct{})
	s.done = make(chan struct{})
	s.pumpDone = make(chan struct{})
	s.started = true

	go s.pump()
	go s.reap()
	return nil
}

// pump frames stdout into lines. It reads until EOF so a slow consumer never
// loses the tail; after Close it keeps reading and discards, so a CLI blocked
// on a full pipe can still exit. Each line is built with at most one copy.
func (s *Subprocess) pump() {
	defer close(s.pumpDone)
	defer close(s.lines)
	r := bufio.NewReaderSize(s.stdoutR, 64<<10)
	var acc []byte // only used for lines spanning several buffer fills
	discarding := false
	emit := func(it lineItem) {
		select { // blocks on a slow consumer, backpressuring the CLI
		case s.lines <- it:
		case <-s.done: // closed: drop
		}
	}
	for {
		s.lastRead.Store(time.Now().UnixNano()) // idle time counts from entering the read, not from the last data
		s.inRead.Store(true)
		chunk, err := r.ReadSlice('\n')
		s.inRead.Store(false)
		if len(chunk) > 0 {
			s.lastRead.Store(time.Now().UnixNano())
		}
		if err == bufio.ErrBufferFull || (err == nil || err == io.EOF) && len(chunk) > 0 {
			switch {
			case discarding:
			case len(acc)+len(chunk) > s.MaxLineBytes+2:
				discarding, acc = true, nil //nolint:prealloc // dropping the buffer
			case err == bufio.ErrBufferFull || len(acc) > 0:
				acc = append(acc, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == nil || (err == io.EOF && (len(chunk) > 0 || len(acc) > 0 || discarding)) { // a line ended, or EOF mid-line
			switch {
			case discarding:
				emit(lineItem{err: &LineTooLongError{Limit: s.MaxLineBytes}})
			default:
				var line []byte
				if len(acc) > 0 {
					line = acc
				} else {
					line = bytes.Clone(chunk) // chunk aliases the reader's buffer
				}
				if line = bytes.TrimRight(line, "\r\n"); len(line) > 0 {
					emit(lineItem{line: line})
				}
			}
			acc, discarding = nil, false
		}
		if err != nil {
			if err != io.EOF && s.forced.Load() {
				emit(lineItem{err: ErrOutputTruncated})
			}
			return
		}
	}
}

// reap waits for the process and records its status. It then watches stdout:
// if a descendant keeps the pipe open and nothing has been read for
// DrainGrace after the exit, the pipe is force-closed and readers get
// ErrOutputTruncated instead of waiting forever. A slow consumer is never
// cut off: the force-close only happens while the pump is idle in read.
func (s *Subprocess) reap() {
	err := s.cmd.Wait()
	st := ExitStatus{}
	if ps := s.cmd.ProcessState; ps != nil {
		st.ExitCode, st.Signal = exitInfo(ps)
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) && !errors.Is(err, exec.ErrWaitDelay) {
		s.waitEr = err
	}
	st.StderrTail = s.stderr.String()
	s.status = st
	exitedAt := time.Now()
	close(s.exited)
	grace := s.DrainGrace
	if grace <= 0 {
		grace = DefaultDrainGrace
	}
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.pumpDone:
			return
		case <-tick.C:
			last := time.Unix(0, s.lastRead.Load())
			if last.Before(exitedAt) {
				last = exitedAt
			}
			if s.inRead.Load() && time.Since(last) > grace {
				s.forced.Store(true)
				_ = s.stdoutR.Close()
				return
			}
		}
	}
}

// ReadLine implements Transport.
func (s *Subprocess) ReadLine(ctx context.Context) ([]byte, error) {
	if !s.isStarted() {
		return nil, ErrNotStarted
	}
	select {
	case it, ok := <-s.lines:
		if !ok {
			return nil, io.EOF
		}
		return it.line, it.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Subprocess) isStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// WriteLine implements Transport.
func (s *Subprocess) WriteLine(line []byte) error {
	if !s.isStarted() {
		return ErrNotStarted
	}
	buf := make([]byte, 0, len(line)+1)
	buf = append(append(buf, line...), '\n')
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.inputClosed {
		return ErrInputClosed
	}
	_, err := s.stdinW.Write(buf)
	if err != nil && errors.Is(err, os.ErrClosed) {
		return ErrInputClosed
	}
	return err
}

// CloseInput implements Transport.
func (s *Subprocess) CloseInput() error {
	if !s.isStarted() {
		return ErrNotStarted
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.closeInputLocked()
}

func (s *Subprocess) closeInputLocked() error {
	if s.inputClosed {
		return nil
	}
	s.inputClosed = true
	return s.stdinW.Close()
}

// Detach implements Transport; a Subprocess owns its process.
func (s *Subprocess) Detach() error { return ErrDetachUnsupported }

// Wait implements Transport.
func (s *Subprocess) Wait(ctx context.Context) (ExitStatus, error) {
	if !s.isStarted() {
		return ExitStatus{}, ErrNotStarted
	}
	select {
	case <-s.exited:
		return s.status, s.waitEr
	case <-ctx.Done():
		return ExitStatus{}, ctx.Err()
	}
}

// Close implements Transport.
func (s *Subprocess) Close() error {
	if !s.isStarted() {
		return nil
	}
	s.closeOnce.Do(s.terminate)
	return nil
}

func (s *Subprocess) terminate() {
	pid := s.cmd.Process.Pid
	close(s.done) // release a pump blocked on an unread line
	// Close stdin; if a writer is stuck on a full pipe, force it.
	done := make(chan struct{})
	go func() { _ = s.CloseInput(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		_ = s.stdinW.Close()
	}

	settled := func() bool {
		select {
		case <-s.exited:
			return !groupAlive(pid)
		default:
			return false
		}
	}
	waitSettled := func(d time.Duration) bool {
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if settled() {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return settled()
	}
	if !waitSettled(s.Grace) {
		_ = signalGroup(pid, sigTerm())
		if !waitSettled(s.TermWait) {
			_ = signalGroup(pid, sigKill())
			waitSettled(2 * time.Second)
		}
	}
	<-s.exited
	_ = s.stdoutR.Close()
}
