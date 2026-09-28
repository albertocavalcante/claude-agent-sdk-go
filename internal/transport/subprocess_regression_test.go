package transport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSubprocessHelperProcess(t *testing.T) {
	mode := os.Getenv("SDK_BUG_FAKE_PROCESS")
	if mode == "" {
		return
	}
	if mode == "idle" {
		fmt.Println(`{"type":"future"}`)
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if mode == "error" || mode == "buffered-error" {
		count := 1
		if mode == "buffered-error" {
			count, _ = strconv.Atoi(os.Getenv("SDK_FAKE_BUFFER_SIZE"))
		}
		for i := 0; i < count; i++ {
			fmt.Println(`{"type":"future"}`)
		}
		fmt.Fprintln(os.Stderr, "fake CLI failure")
		os.Exit(23)
	}
	if mode == "overlong" {
		fmt.Println(`{"type":"future","content":"` + strings.Repeat("x", 2<<20) + `"}`)
		os.Exit(0)
	}
	for i := 0; i < 200; i++ {
		fmt.Println(`{"type":"future"}`)
	}
	os.Exit(0)
}
func TestSubprocessCancellationWithFullBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := &SubprocessTransport{}
	err := tr.Start(ctx, "", &Options{CLIPath: os.Args[0], CLIPrefixArgs: []string{"-test.run=^TestSubprocessHelperProcess$", "--"}, Env: map[string]string{"SDK_BUG_FAKE_PROCESS": "flood"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for len(tr.ch) < cap(tr.ch) {
		select {
		case <-deadline:
			t.Fatal("fake CLI did not fill transport buffer")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	closed := make(chan struct{})
	go func() { _ = tr.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(4 * time.Second):
		t.Error("Close blocked for 4s after cancellation: transport reader is stuck sending into full buffer")
	}
	// Draining is test cleanup only; callers expect cancel/Close to release resources without it.
	for range tr.Lines() {
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close still blocked after test drained buffer")
	}
}
func TestSubprocessOversizedLineTerminatesStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := &SubprocessTransport{}
	err := tr.Start(ctx, "", &Options{CLIPath: os.Args[0], CLIPrefixArgs: []string{"-test.run=^TestSubprocessHelperProcess$", "--"}, Env: map[string]string{"SDK_BUG_FAKE_PROCESS": "overlong"}})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	select {
	case raw := <-tr.Lines():
		if raw.Err == nil || !strings.Contains(raw.Err.Error(), "token too long") {
			t.Fatalf("expected oversized line error: %#v", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no oversized line error")
	}
	deadline := time.After(time.Second)
	for {
		select {
		case raw, ok := <-tr.Lines():
			if !ok {
				if tr.cmd.ProcessState == nil {
					t.Error("stream closed before the subprocess was reaped")
				}
				return
			}
			if raw.Err == nil {
				t.Errorf("unexpected output after scanner failure: %s", raw.Line)
			}
		case <-deadline:
			t.Error("stream stays open after scanner failure: fake process blocked writing unread stdout")
			cancel()
			for range tr.Lines() {
			}
			return
		}
	}
}

func TestSubprocessCloseWithFullBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := &SubprocessTransport{}
	err := tr.Start(ctx, "", &Options{CLIPath: os.Args[0], CLIPrefixArgs: []string{"-test.run=^TestSubprocessHelperProcess$", "--"}, Env: map[string]string{"SDK_BUG_FAKE_PROCESS": "flood"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for len(tr.ch) < cap(tr.ch) {
		select {
		case <-deadline:
			t.Fatal("fake CLI did not fill transport buffer")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	closed := make(chan struct{})
	go func() { _ = tr.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(4 * time.Second):
		t.Error("Close blocked for 4s during Close: transport reader is stuck sending into full buffer")
	}
	// Draining is test cleanup only; callers expect cancel/Close to release resources without it.
	for range tr.Lines() {
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close still blocked after test drained buffer")
	}
}

func startHelperTransport(t *testing.T, ctx context.Context, mode string) *SubprocessTransport {
	t.Helper()
	tr := &SubprocessTransport{}
	opts := &Options{CLIPath: os.Args[0], CLIPrefixArgs: []string{"-test.run=^TestSubprocessHelperProcess$", "--"}, Env: map[string]string{"SDK_BUG_FAKE_PROCESS": mode, "SDK_FAKE_BUFFER_SIZE": "10"}}
	if err := tr.Start(ctx, "", opts); err != nil {
		t.Fatal(err)
	}
	return tr
}
func TestSubprocessCancellationWithoutNewOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := startHelperTransport(t, ctx, "idle")
	select {
	case <-tr.Lines():
	case <-time.After(2 * time.Second):
		t.Fatal("missing helper readiness message")
	}
	cancel()
	select {
	case <-tr.done:
	case <-time.After(2 * time.Second):
		t.Error("idle scanner did not finish on cancellation")
	}
	_ = tr.Close()
}
func TestSubprocessCancellationDuringExitErrorDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := startHelperTransport(t, ctx, "buffered-error")
	deadline := time.Now().Add(2 * time.Second)
	for !errors.Is(tr.cmd.Process.Signal(syscall.Signal(0)), os.ErrProcessDone) {
		if time.Now().After(deadline) {
			cancel()
			for range tr.Lines() {
			}
			t.Fatal("helper process was not reaped")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-tr.done:
	case <-time.After(time.Second):
		t.Error("cancelled transport blocked delivering process exit error")
	}
	for range tr.Lines() {
	}
	_ = tr.Close()
}
func TestSubprocessReportsExitStatusAndStderr(t *testing.T) {
	tr := startHelperTransport(t, context.Background(), "error")
	defer tr.Close()
	var lines int
	var exitErr *exec.ExitError
	for raw := range tr.Lines() {
		if raw.Err == nil {
			lines++
			continue
		}
		if !errors.As(raw.Err, &exitErr) || exitErr.ExitCode() != 23 || !strings.Contains(raw.Err.Error(), "fake CLI failure") {
			t.Fatalf("missing exit status/stderr: %v", raw.Err)
		}
	}
	if lines != 1 || exitErr == nil {
		t.Fatalf("got %d lines and exit error %v", lines, exitErr)
	}
}
