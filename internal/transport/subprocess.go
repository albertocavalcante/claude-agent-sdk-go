package transport

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// SubprocessTransport spawns the claude CLI as a subprocess and streams
// raw JSON lines from its stdout.
type SubprocessTransport struct {
	cmd    *exec.Cmd
	ch     chan RawLineOrError
	stderr bytes.Buffer
	done   chan struct{}
	mu     sync.Mutex
}

// Start launches the claude CLI with the given prompt and options.
func (t *SubprocessTransport) Start(ctx context.Context, prompt string, opts *Options) error {
	cliPath := "claude"
	if opts != nil && opts.CLIPath != "" {
		cliPath = opts.CLIPath
	}

	args := buildArgs(prompt, opts)

	// Support package runners (npx, bunx) via CLIPrefixArgs.
	if opts != nil && len(opts.CLIPrefixArgs) > 0 {
		args = append(opts.CLIPrefixArgs, args...)
	}

	t.cmd = exec.CommandContext(ctx, cliPath, args...)

	if opts != nil && opts.WorkingDirectory != "" {
		t.cmd.Dir = opts.WorkingDirectory
	}

	if opts != nil && len(opts.Env) > 0 {
		// Inherit the current environment and add extras.
		t.cmd.Env = t.cmd.Environ()
		for k, v := range opts.Env {
			t.cmd.Env = append(t.cmd.Env, k+"="+v)
		}
	}

	stdout, err := t.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	t.cmd.Stderr = &t.stderr

	if err := t.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start claude CLI: %w", err)
	}

	t.ch = make(chan RawLineOrError, 10)
	t.done = make(chan struct{})

	go func() {
		defer close(t.ch)
		defer close(t.done)

		scanner := bufio.NewScanner(stdout)
		// Allow up to 1MB per line for large tool outputs.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}

			// Copy the line since scanner reuses the buffer.
			lineCopy := make([]byte, len(line))
			copy(lineCopy, line)
			t.ch <- RawLineOrError{Line: lineCopy}
		}

		if err := scanner.Err(); err != nil {
			t.ch <- RawLineOrError{Err: fmt.Errorf("error reading stdout: %w", err)}
		}

		// Wait for process to finish.
		waitErr := t.cmd.Wait()
		if waitErr != nil {
			exitCode := -1
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
			t.ch <- RawLineOrError{
				Err: fmt.Errorf("CLI process exited with code %d: %w (stderr: %s)",
					exitCode, waitErr, t.stderr.String()),
			}
		}
	}()

	return nil
}

// Lines returns the channel of raw JSON lines from the CLI.
func (t *SubprocessTransport) Lines() <-chan RawLineOrError {
	return t.ch
}

// Close terminates the CLI process if it is still running.
func (t *SubprocessTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cmd != nil && t.cmd.Process != nil {
		// Try graceful SIGTERM first, fall back to SIGKILL after timeout.
		_ = t.cmd.Process.Signal(syscall.SIGTERM)

		// Give the process a short window to exit gracefully.
		done := make(chan struct{})
		go func() {
			if t.done != nil {
				<-t.done
			}
			close(done)
		}()

		select {
		case <-done:
			// Process exited gracefully.
			return nil
		case <-time.After(3 * time.Second):
			// Force kill after timeout.
			_ = t.cmd.Process.Kill()
		}
	}

	// Wait for the reader goroutine to finish.
	if t.done != nil {
		<-t.done
	}

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
