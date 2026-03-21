package transport

import (
	"context"
	"encoding/json"
)

// MockTransport replays canned JSON lines for testing.
// Set Lines to the raw JSON lines you want to replay before calling Start.
type MockTransport struct {
	// RawLines holds raw JSON lines to replay.
	RawLines []json.RawMessage

	// StartErr, if non-nil, is returned by Start instead of replaying lines.
	StartErr error

	ch chan RawLineOrError
}

// Start sends the canned lines to the channel.
func (m *MockTransport) Start(_ context.Context, _ string, _ *Options) error {
	if m.StartErr != nil {
		return m.StartErr
	}

	m.ch = make(chan RawLineOrError, len(m.RawLines)+1)

	go func() {
		defer close(m.ch)
		for _, line := range m.RawLines {
			m.ch <- RawLineOrError{Line: []byte(line)}
		}
	}()

	return nil
}

// Lines returns the channel of replayed raw JSON lines.
func (m *MockTransport) Lines() <-chan RawLineOrError {
	return m.ch
}

// Close is a no-op for MockTransport.
func (m *MockTransport) Close() error {
	return nil
}
