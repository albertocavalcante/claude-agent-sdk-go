package transport

import (
	"context"
	"encoding/json"
	"sync"
)

// MockTransport replays canned JSON lines for testing.
// Set Lines to the raw JSON lines you want to replay before calling Start.
type MockTransport struct {
	// RawLines holds raw JSON lines to replay.
	RawLines []json.RawMessage

	// StartErr, if non-nil, is returned by Start instead of replaying lines.
	StartErr error

	// StartFunc, if non-nil, is called at the beginning of Start.
	StartFunc func()

	// CloseFunc, if non-nil, is called at the beginning of Close.
	CloseFunc func()

	// SlowMode, when true, makes Start succeed but defers sending lines
	// until Close is called. This simulates a long-running query that
	// blocks until cancelled.
	SlowMode bool

	ch      chan RawLineOrError
	closeCh chan struct{} // signals Close was called in slow mode
	closeOnce sync.Once
}

// Start sends the canned lines to the channel.
func (m *MockTransport) Start(ctx context.Context, _ string, _ *Options) error {
	if m.StartFunc != nil {
		m.StartFunc()
	}
	if m.StartErr != nil {
		return m.StartErr
	}

	m.ch = make(chan RawLineOrError, len(m.RawLines)+1)

	if m.SlowMode {
		m.closeCh = make(chan struct{})
		go func() {
			defer close(m.ch)
			// Block until Close is called or context cancelled.
			select {
			case <-m.closeCh:
			case <-ctx.Done():
			}
		}()
		return nil
	}

	go func() {
		defer close(m.ch)
		for _, line := range m.RawLines {
			select {
			case m.ch <- RawLineOrError{Line: []byte(line)}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

// Lines returns the channel of replayed raw JSON lines.
func (m *MockTransport) Lines() <-chan RawLineOrError {
	return m.ch
}

// Close calls CloseFunc if set and signals slow mode to stop.
func (m *MockTransport) Close() error {
	if m.CloseFunc != nil {
		m.CloseFunc()
	}
	m.closeOnce.Do(func() {
		if m.closeCh != nil {
			close(m.closeCh)
		}
	})
	return nil
}
