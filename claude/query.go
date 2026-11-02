package claude

import (
	"context"
	"iter"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
)

// Query runs one prompt to completion on a fresh Client and yields every
// message. The Client is closed when the sequence ends or the consumer stops.
//
// Input is closed right after the prompt, unless Options.CanUseTool or
// Options.Hooks are set: those need stdin open (the CLI fails later control
// requests with "Stream closed"), so input is closed only at the end of the
// run, when system/session_state_changed reports idle after the result.
func Query(ctx context.Context, prompt string, opts Options) iter.Seq2[protocol.Message, error] {
	return func(yield func(protocol.Message, error) bool) {
		c, err := NewClient(ctx, opts)
		if err != nil {
			yield(nil, err)
			return
		}
		defer func() { _ = c.Close() }()
		if err := c.Send(ctx, UserText(prompt)); err != nil {
			yield(nil, err)
			return
		}
		needsInput := opts.CanUseTool != nil || len(opts.Hooks) > 0
		var once sync.Once
		closeInput := func() { once.Do(func() { _ = c.CloseInput() }) }
		var fallback *time.Timer
		defer func() {
			if fallback != nil {
				fallback.Stop()
			}
		}()
		if !needsInput {
			closeInput()
		}
		sawResult := false
		for m, err := range c.Messages(ctx) {
			if err == nil {
				switch v := m.(type) {
				case *protocol.Result:
					sawResult = true
					// Without an idle frame (session state reporting turned
					// off) input still closes once the background-wait ceiling
					// has passed.
					if needsInput && fallback == nil {
						if d := bgWaitCeiling(opts.Env); d > 0 {
							fallback = time.AfterFunc(d, closeInput)
						}
					}
				case *protocol.SessionStateChanged:
					if v.State == "idle" && sawResult {
						closeInput()
					}
				}
			}
			if !yield(m, err) {
				return
			}
		}
	}
}

// bgWaitCeiling reads CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS (default 600000;
// 0 means unlimited) from env, falling back to the process environment.
func bgWaitCeiling(env []string) time.Duration {
	v := os.Getenv("CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS")
	for _, e := range env {
		if x, ok := strings.CutPrefix(e, "CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS="); ok {
			v = x
		}
	}
	ms := 600000
	if v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}
