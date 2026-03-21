package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestMockTransportReplay(t *testing.T) {
	mock := &MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"system"}`),
			json.RawMessage(`{"type":"assistant","content":[]}`),
			json.RawMessage(`{"type":"result"}`),
		},
	}

	if err := mock.Start(context.Background(), "test", nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	var lines [][]byte
	for raw := range mock.Lines() {
		if raw.Err != nil {
			t.Fatalf("unexpected error: %v", raw.Err)
		}
		lines = append(lines, raw.Line)
	}

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
}

func TestMockTransportStartError(t *testing.T) {
	mock := &MockTransport{
		StartErr: errors.New("mock error"),
	}

	err := mock.Start(context.Background(), "test", nil)
	if err == nil {
		t.Fatal("expected error from Start")
	}
	if err.Error() != "mock error" {
		t.Errorf("expected 'mock error', got %q", err.Error())
	}
}

func TestMockTransportEmptyLines(t *testing.T) {
	mock := &MockTransport{}

	if err := mock.Start(context.Background(), "test", nil); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	count := 0
	for range mock.Lines() {
		count++
	}

	if count != 0 {
		t.Errorf("expected 0 lines from empty mock, got %d", count)
	}
}

func TestMockTransportClose(t *testing.T) {
	mock := &MockTransport{}
	if err := mock.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}
