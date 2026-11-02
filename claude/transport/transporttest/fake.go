// Package transporttest provides Fake, a scripted in-memory
// transport.Transport for tests: feed recorded stdout lines, capture what the
// SDK writes, and assert the order of lifecycle calls.
package transporttest

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport"
)

// Fake is an in-memory Transport. Its zero value is not usable; call New.
// Feed never blocks, so a test can queue a whole recorded session up front
// or drip it in reaction to writes via OnWrite.
type Fake struct {
	mu     sync.Mutex
	wake   chan struct{} // closed and replaced whenever state changes
	in     [][]byte      // stdout lines not yet read
	eof    bool
	exit   transport.ExitStatus
	exited bool

	writes   [][]byte
	consumed int // writes handed out by NextWrite
	events   []string
	onWrite  func(line []byte)

	inputClosed bool
	detached    bool
	closed      bool
}

// New returns an empty Fake.
func New() *Fake { return &Fake{wake: make(chan struct{})} }

// broadcast wakes every waiter. Caller holds mu.
func (f *Fake) broadcast() {
	close(f.wake)
	f.wake = make(chan struct{})
}

// Feed queues lines for ReadLine.
func (f *Fake) Feed(lines ...string) {
	f.mu.Lock()
	for _, l := range lines {
		f.in = append(f.in, []byte(l))
	}
	f.broadcast()
	f.mu.Unlock()
}

// EOF ends stdout after the queued lines and records the process exit status.
func (f *Fake) EOF(st transport.ExitStatus) {
	f.mu.Lock()
	f.eof, f.exit, f.exited = true, st, true
	f.broadcast()
	f.mu.Unlock()
}

// OnWrite registers a callback run synchronously for every written line,
// after it is recorded. It may call Feed. It must not call WriteLine.
func (f *Fake) OnWrite(fn func(line []byte)) {
	f.mu.Lock()
	f.onWrite = fn
	f.mu.Unlock()
}

// Writes returns a copy of every line written so far.
func (f *Fake) Writes() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.writes))
	for i, w := range f.writes {
		out[i] = bytes.Clone(w)
	}
	return out
}

// Events returns the ordered lifecycle log: "start", "write <line>",
// "close_input", "close", "detach".
func (f *Fake) Events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// NextWrite returns the next write not yet returned by NextWrite or
// AwaitWrite, waiting for it if necessary.
func (f *Fake) NextWrite(ctx context.Context) ([]byte, error) {
	return f.AwaitWrite(ctx, func([]byte) bool { return true })
}

// AwaitWrite waits for the first unconsumed write for which match is true,
// consumes everything up to and including it, and returns it.
func (f *Fake) AwaitWrite(ctx context.Context, match func([]byte) bool) ([]byte, error) {
	for {
		f.mu.Lock()
		for i := f.consumed; i < len(f.writes); i++ {
			if match(f.writes[i]) {
				f.consumed = i + 1
				out := bytes.Clone(f.writes[i])
				f.mu.Unlock()
				return out, nil
			}
		}
		wake := f.wake
		f.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Start implements transport.Transport.
func (f *Fake) Start(context.Context) error {
	f.mu.Lock()
	f.events = append(f.events, "start")
	f.mu.Unlock()
	return nil
}

// ReadLine implements transport.Transport.
func (f *Fake) ReadLine(ctx context.Context) ([]byte, error) {
	for {
		f.mu.Lock()
		if len(f.in) > 0 {
			l := f.in[0]
			f.in = f.in[1:]
			f.mu.Unlock()
			return l, nil
		}
		if f.eof || f.closed {
			f.mu.Unlock()
			return nil, io.EOF
		}
		wake := f.wake
		f.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// WriteLine implements transport.Transport.
func (f *Fake) WriteLine(line []byte) error {
	f.mu.Lock()
	if f.detached {
		f.mu.Unlock()
		return transport.ErrDetached
	}
	if f.inputClosed || f.closed {
		f.mu.Unlock()
		return transport.ErrInputClosed
	}
	l := bytes.Clone(line)
	f.writes = append(f.writes, l)
	f.events = append(f.events, "write "+string(l))
	cb := f.onWrite
	f.broadcast()
	f.mu.Unlock()
	if cb != nil {
		cb(l)
	}
	return nil
}

// CloseInput implements transport.Transport.
func (f *Fake) CloseInput() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.inputClosed {
		f.inputClosed = true
		f.events = append(f.events, "close_input")
		f.broadcast()
	}
	return nil
}

// Close implements transport.Transport.
func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		f.events = append(f.events, "close")
		f.exited = true
		f.broadcast()
	}
	return nil
}

// Detach implements transport.Transport: it records the event and rejects
// writes with transport.ErrDetached until Reattach. The queue and write log
// stay intact so a second client can take over.
func (f *Fake) Detach() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detached = true
	f.events = append(f.events, "detach")
	f.broadcast()
	return nil
}

// Wait implements transport.Transport. It returns once EOF or Close was
// called.
func (f *Fake) Wait(ctx context.Context) (transport.ExitStatus, error) {
	for {
		f.mu.Lock()
		if f.exited {
			st := f.exit
			f.mu.Unlock()
			return st, nil
		}
		wake := f.wake
		f.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return transport.ExitStatus{}, ctx.Err()
		}
	}
}

// Reattach lifts the write block set by Detach, as a new owner of the pipes
// would; the events log records "attach".
func (f *Fake) Reattach() {
	f.mu.Lock()
	f.detached = false
	f.events = append(f.events, "attach")
	f.broadcast()
	f.mu.Unlock()
}
