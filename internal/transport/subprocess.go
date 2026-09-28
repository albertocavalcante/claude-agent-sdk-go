package transport

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// SubprocessTransport spawns the claude CLI as a subprocess and streams
// raw JSON lines from its stdout.
type SubprocessTransport struct {
	cmd       *exec.Cmd
	ch        chan RawLineOrError
	stderr    bytes.Buffer
	stdout    io.ReadCloser
	done      chan struct{}
	stop      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
}

// Start launches the claude CLI with the given prompt and options.
func (t *SubprocessTransport) Start(ctx context.Context, prompt string, opts *Options) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done != nil {
		return fmt.Errorf("transport already started")
	}
	cliPath := "claude"
	if opts != nil && opts.CLIPath != "" {
		cliPath = opts.CLIPath
	}
	resolved, err := LookPath(cliPath)
	if err != nil {
		return fmt.Errorf("cannot find %s: %w", cliPath, err)
	}
	args := buildArgs(prompt, opts)
	if opts != nil && len(opts.CLIPrefixArgs) > 0 {
		args = append(opts.CLIPrefixArgs, args...)
	}
	cmd := exec.CommandContext(ctx, resolved, args...)
	t.cmd = cmd
	if opts != nil && opts.WorkingDirectory != "" {
		cmd.Dir = opts.WorkingDirectory
	}
	if opts != nil && len(opts.Env) > 0 {
		cmd.Env = cmd.Environ()
		for k, v := range opts.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	t.stdout = stdout
	cmd.Stderr = &t.stderr
	// Cancellation must also unblock a scanner when descendants inherit stdout.
	cmd.Cancel = func() error {
		err := cmd.Process.Kill()
		_ = stdout.Close()
		return err
	}
	// Bound stderr copier cleanup when descendants inherit that descriptor.
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return fmt.Errorf("failed to start claude CLI: %w", err)
	}
	t.ch = make(chan RawLineOrError, 10)
	t.done = make(chan struct{})
	t.stop = make(chan struct{})

	go func() {
		defer close(t.done)
		defer close(t.ch)
		send := func(raw RawLineOrError) bool {
			select {
			case <-ctx.Done():
				return false
			case <-t.stop:
				return false
			default:
			}
			select {
			case t.ch <- raw:
				return true
			case <-ctx.Done():
				return false
			case <-t.stop:
				return false
			}
		}
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if !send(RawLineOrError{Line: bytes.Clone(line)}) {
				break
			}
		}
		scanErr := scanner.Err()
		_ = stdout.Close()
		if scanErr != nil && ctx.Err() == nil {
			// A fatal read error leaves the child with nobody consuming its stdout.
			// Stop it before Wait; explicit Close retains the graceful SIGTERM path.
			select {
			case <-t.stop:
			default:
				_ = cmd.Process.Kill()
			}
		}
		waitErr := cmd.Wait()
		if scanErr != nil {
			send(RawLineOrError{Err: fmt.Errorf("error reading stdout: %w", scanErr)})
		}
		if waitErr != nil {
			exitCode := -1
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
			send(RawLineOrError{Err: fmt.Errorf("CLI process exited with code %d: %w (stderr: %s)", exitCode, waitErr, t.stderr.String())})
		}
	}()
	return nil
}

// Lines returns the channel of raw JSON lines from the CLI.
func (t *SubprocessTransport) Lines() <-chan RawLineOrError {
	return t.ch
}

// Close terminates the CLI process if it is still running.
// It also unblocks delivery when the caller has stopped draining Lines.
func (t *SubprocessTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done == nil {
		return nil
	}
	t.closeOnce.Do(func() { close(t.stop) })
	_ = t.cmd.Process.Signal(syscall.SIGTERM)
	_ = t.stdout.Close()
	select {
	case <-t.done:
		return nil
	case <-time.After(3 * time.Second):
		_ = t.cmd.Process.Kill()
	}
	<-t.done
	return nil
}

// buildArgs constructs the CLI argument list from the prompt and options.
func buildArgs(prompt string, opts *Options) []string {
	args := []string{"--print", "--output-format", "stream-json", "--verbose"}

	if opts == nil {
		args = append(args, "-p", prompt)
		return args
	}

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--system-prompt", opts.SystemPrompt)
	}
	if opts.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.AppendSystemPrompt)
	}
	if opts.MaxThinkingTokens > 0 {
		args = append(args, "--max-thinking-tokens", strconv.Itoa(opts.MaxThinkingTokens))
	}
	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", opts.PermissionMode)
	}
	for _, tool := range opts.AllowedTools {
		args = append(args, "--allowedTools", tool)
	}
	for _, tool := range opts.DisallowedTools {
		args = append(args, "--disallowedTools", tool)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(opts.MaxTurns))
	}
	if opts.SessionID != "" {
		args = append(args, "--session-id", opts.SessionID)
	}
	if opts.MCPConfigPath != "" {
		args = append(args, "--mcp-config", opts.MCPConfigPath)
	}

	args = append(args, "-p", prompt)
	return args
}
