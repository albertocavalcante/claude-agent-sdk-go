package transporttest

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport"
)

var _ transport.Transport = (*Fake)(nil)

func TestFeedAndRead(t *testing.T) {
	f := New()
	if err := f.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.Feed(`{"a":1}`, `{"b":2}`)
	for _, want := range []string{`{"a":1}`, `{"b":2}`} {
		got, err := f.ReadLine(context.Background())
		if err != nil || string(got) != want {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := f.ReadLine(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want deadline, got %v", err)
	}
	f.Feed("late")
	if got, _ := f.ReadLine(context.Background()); string(got) != "late" {
		t.Errorf("line lost after a cancelled read: %q", got)
	}
	f.EOF(transport.ExitStatus{ExitCode: 1})
	if _, err := f.ReadLine(context.Background()); !errors.Is(err, io.EOF) {
		t.Errorf("want EOF, got %v", err)
	}
	st, err := f.Wait(context.Background())
	if err != nil || st.ExitCode != 1 {
		t.Errorf("Wait = %+v %v", st, err)
	}
}

func TestWritesAndEvents(t *testing.T) {
	f := New()
	_ = f.Start(context.Background())
	_ = f.WriteLine([]byte(`{"x":1}`))
	_ = f.CloseInput()
	if err := f.WriteLine([]byte("y")); !errors.Is(err, transport.ErrInputClosed) {
		t.Errorf("write after CloseInput: %v", err)
	}
	_ = f.Close()
	ev := f.Events()
	want := []string{"start", `write {"x":1}`, "close_input", "close"}
	if len(ev) != len(want) {
		t.Fatalf("events = %v", ev)
	}
	for i := range want {
		if ev[i] != want[i] {
			t.Errorf("event %d = %q want %q", i, ev[i], want[i])
		}
	}
	if w := f.Writes(); len(w) != 1 || string(w[0]) != `{"x":1}` {
		t.Errorf("writes = %q", w)
	}
}

func TestAwaitWrite(t *testing.T) {
	f := New()
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = f.WriteLine([]byte("one"))
		_ = f.WriteLine([]byte("two"))
	}()
	got, err := f.NextWrite(context.Background())
	if err != nil || string(got) != "one" {
		t.Fatalf("%q %v", got, err)
	}
	got, err = f.AwaitWrite(context.Background(), func(b []byte) bool { return string(b) == "two" })
	if err != nil || string(got) != "two" {
		t.Fatalf("%q %v", got, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := f.NextWrite(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want deadline, got %v", err)
	}
}

func TestOnWriteReacts(t *testing.T) {
	f := New()
	f.OnWrite(func(line []byte) {
		if string(line) == "ping" {
			f.Feed("pong")
		}
	})
	_ = f.WriteLine([]byte("ping"))
	got, _ := f.ReadLine(context.Background())
	if string(got) != "pong" {
		t.Errorf("%q", got)
	}
}

func TestDetachLeavesTransportUsable(t *testing.T) {
	f := New()
	if err := f.Detach(); err != nil {
		t.Fatal(err)
	}
	f.Feed("x")
	if got, err := f.ReadLine(context.Background()); err != nil || string(got) != "x" {
		t.Errorf("%q %v", got, err)
	}
	if f.Events()[0] != "detach" {
		t.Errorf("events %v", f.Events())
	}
}

func TestWritesAfterDetachAreRejectedUntilReattach(t *testing.T) {
	f := New()
	_ = f.Detach()
	if err := f.WriteLine([]byte("x")); !errors.Is(err, transport.ErrDetached) {
		t.Errorf("write after Detach = %v", err)
	}
	f.Reattach()
	if err := f.WriteLine([]byte("x")); err != nil {
		t.Errorf("write after Reattach = %v", err)
	}
}

func TestCloseUnblocksReader(t *testing.T) {
	f := New()
	done := make(chan error, 1)
	go func() { _, err := f.ReadLine(context.Background()); done <- err }()
	time.Sleep(20 * time.Millisecond)
	_ = f.Close()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Errorf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader not unblocked")
	}
}
