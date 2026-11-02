//go:build unix

package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHelperProcess is the fake CLI. It only runs when HELPER_MODE is set,
// i.e. when a test re-executes this test binary through Subprocess.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("HELPER_MODE")
	if mode == "" {
		return
	}
	switch mode {
	case "echo":
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			fmt.Println("echo:" + sc.Text())
		}
		os.Exit(0)
	case "env":
		out := map[string]string{}
		for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_AGENT_SDK_VERSION",
			"CLAUDE_CODE_SDK_READS_SESSION_STATE", "KEEP_ME", "CUSTOM", "PWD"} {
			if v, ok := os.LookupEnv(k); ok {
				out[k] = v
			}
		}
		b, _ := json.Marshal(out)
		fmt.Println(string(b))
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "grandchild":
		// Leader exits on stdin EOF; the grandchild would live for minutes.
		gc := exec.Command("sleep", "300")
		if err := gc.Start(); err != nil {
			os.Exit(90)
		}
		fmt.Println(gc.Process.Pid)
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "stubborn":
		// Ignores EOF and SIGTERM; only SIGKILL stops it.
		gc := exec.Command("sleep", "300")
		_ = gc.Start()
		fmt.Println(gc.Process.Pid)
		ignoreTerm()
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	case "stderr":
		chunk := strings.Repeat("e", 1023) + "\n"
		for range 1024 {
			fmt.Fprint(os.Stderr, chunk)
		}
		fmt.Fprintln(os.Stderr, "LAST-STDERR-LINE")
		os.Exit(3)
	case "overlong":
		fmt.Println(`{"type":"first"}`)
		fmt.Println(`{"pad":"` + strings.Repeat("x", 200000) + `"}`)
		fmt.Println(`{"type":"after"}`)
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "partial":
		fmt.Print(`{"type":"nonl"}`)
		os.Exit(0)
	case "exit":
		n, _ := strconv.Atoi(os.Getenv("HELPER_CODE"))
		os.Exit(n)
	case "killself":
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		time.Sleep(time.Minute)
	case "holder":
		// A descendant keeps stdout open after the leader exits.
		gc := exec.Command("sleep", "300")
		gc.Stdout = os.Stdout
		_ = gc.Start()
		fmt.Println(`{"type":"before-exit"}`)
		os.Exit(0)
	case "flood-until-eof":
		go func() {
			line := strings.Repeat("f", 1000)
			for {
				fmt.Println(line)
			}
		}()
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "idle":
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(99)
}

func helper(mode string, extra ...string) *Subprocess {
	var env []string
	for _, e := range os.Environ() { // a clean slate: the tests may run inside Claude Code
		if !strings.HasPrefix(e, "CLAUDE") {
			env = append(env, e)
		}
	}
	env = append(env, "HELPER_MODE="+mode)
	env = append(env, extra...)
	return &Subprocess{
		Path:  os.Args[0],
		Args:  []string{"-test.run=^TestHelperProcess$", "--"},
		Env:   env,
		Grace: 300 * time.Millisecond, TermWait: 500 * time.Millisecond,
		MaxLineBytes: 64 << 10,
	}
}

func ctx5(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mustLine(t *testing.T, s *Subprocess) string {
	t.Helper()
	b, err := s.ReadLine(ctx5(t))
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	return string(b)
}

func TestWriteReadRoundTrip(t *testing.T) {
	s := helper("echo")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteLine([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if got := mustLine(t, s); got != `echo:{"a":1}` {
		t.Errorf("got %q", got)
	}
	if err := s.CloseInput(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadLine(ctx5(t)); !errors.Is(err, io.EOF) {
		t.Errorf("after CloseInput want EOF, got %v", err)
	}
	st, err := s.Wait(ctx5(t))
	if err != nil || st.ExitCode != 0 {
		t.Errorf("Wait = %+v, %v", st, err)
	}
	if err := s.WriteLine([]byte("x")); !errors.Is(err, ErrInputClosed) {
		t.Errorf("write after CloseInput: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestStartTwiceAndMissingBinary(t *testing.T) {
	s := helper("idle")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Start(ctx5(t)); err == nil {
		t.Error("second Start should fail")
	}
	bad := &Subprocess{Path: "/nonexistent/claude-xyz"}
	if err := bad.Start(ctx5(t)); err == nil {
		t.Error("missing binary should fail Start")
	}
}

func TestEnvHandling(t *testing.T) {
	s := helper("env", "CLAUDE_CODE_OAUTH_TOKEN=secret", "CLAUDECODE=1", "KEEP_ME=yes")
	s.EnvUnset = []string{"CLAUDE_CODE_OAUTH_TOKEN"}
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got map[string]string
	if err := json.Unmarshal([]byte(mustLine(t, s)), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["CLAUDE_CODE_OAUTH_TOKEN"]; ok {
		t.Error("EnvUnset did not remove CLAUDE_CODE_OAUTH_TOKEN")
	}
	if _, ok := got["CLAUDECODE"]; ok {
		t.Error("CLAUDECODE must always be dropped")
	}
	if got["KEEP_ME"] != "yes" {
		t.Error("unrelated env must be kept")
	}
	if got["CLAUDE_CODE_ENTRYPOINT"] != DefaultEntrypoint {
		t.Errorf("entrypoint = %q", got["CLAUDE_CODE_ENTRYPOINT"])
	}
	if got["CLAUDE_AGENT_SDK_VERSION"] != SDKVersion {
		t.Errorf("sdk version = %q", got["CLAUDE_AGENT_SDK_VERSION"])
	}
	if got["CLAUDE_CODE_SDK_READS_SESSION_STATE"] != "1" {
		t.Error("session state env missing")
	}
}

func TestPWDFollowsDir(t *testing.T) {
	dir := t.TempDir()
	s := helper("env")
	s.Dir = dir
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got map[string]string
	_ = json.Unmarshal([]byte(mustLine(t, s)), &got)
	want, _ := filepath.EvalSymlinks(dir)
	have, _ := filepath.EvalSymlinks(got["PWD"])
	if have != want {
		t.Errorf("PWD = %q, want %q", got["PWD"], dir)
	}
}

func TestEnvDefaultsAreOverridable(t *testing.T) {
	s := helper("env", "CLAUDE_CODE_ENTRYPOINT=sdk-ts", "CLAUDE_CODE_SDK_READS_SESSION_STATE=0")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got map[string]string
	_ = json.Unmarshal([]byte(mustLine(t, s)), &got)
	if got["CLAUDE_CODE_ENTRYPOINT"] != "sdk-ts" || got["CLAUDE_CODE_SDK_READS_SESSION_STATE"] != "0" {
		t.Errorf("caller-set values must win for non-version keys: %v", got)
	}
}

func TestNilEnvInheritsProcessEnv(t *testing.T) {
	t.Setenv("CUSTOM", "inherited")
	s := helper("env")
	s.Env = nil
	t.Setenv("HELPER_MODE", "env")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got map[string]string
	_ = json.Unmarshal([]byte(mustLine(t, s)), &got)
	if got["CUSTOM"] != "inherited" {
		t.Errorf("nil Env must inherit os.Environ: %v", got)
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive", pid)
}

func TestCloseKillsProcessGroupGrandchild(t *testing.T) {
	s := helper("grandchild")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(mustLine(t, s))
	if !alive(pid) {
		t.Fatal("grandchild not running")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	waitDead(t, pid)
}

func TestCloseEscalatesToSIGKILL(t *testing.T) {
	s := helper("stubborn")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(mustLine(t, s))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	start := time.Now()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("Close took %v", d)
	}
	waitDead(t, pid)
	st, _ := s.Wait(ctx5(t))
	if st.Signal != "killed" {
		t.Errorf("status = %+v", st)
	}
}

func TestStderrRingIsCapped(t *testing.T) {
	var lines int
	s := helper("stderr")
	s.StderrBytes = 4096
	s.OnStderr = func(string) { lines++ }
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ReadLine(ctx5(t)); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
	st, err := s.Wait(ctx5(t))
	if err != nil {
		t.Fatal(err)
	}
	if st.ExitCode != 3 {
		t.Errorf("exit = %d", st.ExitCode)
	}
	if len(st.StderrTail) > 4096 || !strings.HasSuffix(strings.TrimSpace(st.StderrTail), "LAST-STDERR-LINE") {
		t.Errorf("tail len=%d suffix=%q", len(st.StderrTail), st.StderrTail[max(0, len(st.StderrTail)-40):])
	}
	if lines < 1000 {
		t.Errorf("OnStderr saw %d lines, want all", lines)
	}
}

func TestOversizedLineIsAnErrorNotAHang(t *testing.T) {
	s := helper("overlong")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := mustLine(t, s); got != `{"type":"first"}` {
		t.Fatalf("first = %q", got)
	}
	_, err := s.ReadLine(ctx5(t))
	var tl *LineTooLongError
	if !errors.As(err, &tl) || !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("want *LineTooLongError, got %v", err)
	}
	if got := mustLine(t, s); got != `{"type":"after"}` {
		t.Errorf("stream must continue after an oversized line, got %q", got)
	}
}

func TestPartialFinalLineDelivered(t *testing.T) {
	s := helper("partial")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := mustLine(t, s); got != `{"type":"nonl"}` {
		t.Errorf("got %q", got)
	}
}

func TestExitStatusMapping(t *testing.T) {
	s := helper("exit", "HELPER_CODE=7")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	st, err := s.Wait(ctx5(t))
	if err != nil || st.ExitCode != 7 || st.Signal != "" {
		t.Errorf("exit 7: %+v %v", st, err)
	}
	k := helper("killself")
	if err := k.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	st, _ = k.Wait(ctx5(t))
	if st.ExitCode != -1 || st.Signal != "killed" {
		t.Errorf("killed: %+v", st)
	}
}

func TestReadLineHonoursContext(t *testing.T) {
	s := helper("idle")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.ReadLine(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want deadline exceeded, got %v", err)
	}
	// the transport is still usable
	if err := s.WriteLine([]byte("x")); err != nil {
		t.Errorf("write after ReadLine timeout: %v", err)
	}
}

func TestWaitHonoursContext(t *testing.T) {
	s := helper("idle")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want deadline exceeded, got %v", err)
	}
}

func TestDetachUnsupported(t *testing.T) {
	s := helper("idle")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Detach(); !errors.Is(err, ErrDetachUnsupported) {
		t.Errorf("Detach = %v", err)
	}
}

func TestConcurrentWritesAreWholeLines(t *testing.T) {
	s := helper("echo")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const n = 50
	payload := strings.Repeat("p", 5000)
	for range n {
		go func() { _ = s.WriteLine([]byte(payload)) }()
	}
	for range n {
		if got := mustLine(t, s); got != "echo:"+payload {
			t.Fatalf("interleaved write: %q", got[:40])
		}
	}
}

func TestCloseIsIdempotentAndUnblocksReader(t *testing.T) {
	s := helper("idle")
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.ReadLine(context.Background()); done <- err }()
	time.Sleep(50 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Errorf("reader got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadLine did not unblock on Close")
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func ignoreTerm() { signal.Ignore(syscall.SIGTERM) }

var _ Transport = (*Subprocess)(nil)

// B1: a consumer that is slow (well behind the CLI's exit) must still get
// every line, including the final result.
func TestTailSurvivesSlowConsumer(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cli.sh")
	body := "#!/bin/sh\npad=$(printf '%0190d' 0)\ni=0\nwhile [ $i -lt 150 ]; do echo \"{\\\"type\\\":\\\"x\\\",\\\"i\\\":$i,\\\"p\\\":\\\"$pad\\\"}\"; i=$((i+1)); done\necho '{\"type\":\"result\",\"final\":true}'\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Subprocess{Path: script, DrainGrace: 200 * time.Millisecond}
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Wait(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(900 * time.Millisecond) // far longer than DrainGrace
	n, last := 0, ""
	for {
		l, err := s.ReadLine(ctx5(t))
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("after %d lines: %v", n, err)
		}
		n++
		last = string(l)
	}
	if n != 151 || !strings.Contains(last, `"final":true`) {
		t.Errorf("read %d of 151 lines; last=%.60s", n, last)
	}
}

func TestForcedCloseIsAnErrorNotEOF(t *testing.T) {
	s := helper("holder")
	s.DrainGrace = 200 * time.Millisecond
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := mustLine(t, s); got != `{"type":"before-exit"}` {
		t.Fatalf("got %q", got)
	}
	_, err := s.ReadLine(ctx5(t))
	if !errors.Is(err, ErrOutputTruncated) {
		t.Fatalf("want ErrOutputTruncated, got %v", err)
	}
}

func TestCloseUnderLoadDoesNotWaitForTheGrace(t *testing.T) {
	s := helper("flood-until-eof")
	s.Grace, s.TermWait = 5*time.Second, 5*time.Second
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // nobody reads: the pipe and queue fill up
	start := time.Now()
	_ = s.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Close under load took %v: stdout was not drained during shutdown", d)
	}
	st, _ := s.Wait(ctx5(t))
	if st.Signal != "" {
		t.Errorf("the CLI should exit on EOF, not be signalled: %+v", st)
	}
}

// B1 follow-up: the forced close means "blocked in read for longer than
// DrainGrace", so a consumer that is merely slow (the pump busy delivering)
// must lose nothing even with a tiny grace.
func TestSlowConsumerWithTinyDrainGraceLosesNothing(t *testing.T) {
	big := strings.Repeat("x", 200)
	s := &Subprocess{Path: "awk", Args: []string{"BEGIN{for(i=0;i<3000;i++) print i \" " + big + "\"}"}, DrainGrace: 20 * time.Millisecond}
	if err := s.Start(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := 0
	for {
		_, err := s.ReadLine(ctx5(t))
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("after %d/3000 lines: %v", n, err)
		}
		n++
		if n%100 == 0 {
			time.Sleep(60 * time.Millisecond)
		}
	}
	if n != 3000 {
		t.Fatalf("got %d lines", n)
	}
}
