package transport

import (
	"bytes"
	"sync"
)

// tailBuffer keeps the last cap bytes written to it and optionally reports
// complete lines. It is an io.Writer that never fails.
type tailBuffer struct {
	mu     sync.Mutex
	cap    int
	buf    []byte
	onLine func(string)
	part   []byte
}

func newTailBuffer(capacity int, onLine func(string)) *tailBuffer {
	return &tailBuffer{cap: capacity, onLine: onLine}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.cap; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	var lines []string
	if t.onLine != nil {
		t.part = append(t.part, p...)
		for {
			i := bytes.IndexByte(t.part, '\n')
			if i < 0 {
				break
			}
			lines = append(lines, string(bytes.TrimRight(t.part[:i], "\r")))
			t.part = t.part[i+1:]
		}
		if len(t.part) > t.cap { // an endless unterminated line: flush it
			lines = append(lines, string(t.part))
			t.part = nil
		}
		t.part = bytes.Clone(t.part)
	}
	t.mu.Unlock()
	for _, l := range lines {
		t.onLine(l)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
