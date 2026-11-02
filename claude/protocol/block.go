package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Block is one content block of an assistant or user message. Every Block
// keeps its original JSON in Raw (empty for blocks built by the caller).
type Block interface {
	BlockType() string
	RawJSON() json.RawMessage
	json.Marshaler
	isBlock()
}

// TextBlock is a text content block.
type TextBlock struct {
	Text string
	Raw  json.RawMessage
}

// ThinkingBlock is an extended-thinking block. Thinking may be empty with
// only a signature present.
type ThinkingBlock struct {
	Thinking  string
	Signature string
	Raw       json.RawMessage
}

// RedactedThinkingBlock is a redacted_thinking block.
type RedactedThinkingBlock struct{ Raw json.RawMessage }

// ToolUseBlock is a tool_use block (also used for server_tool_use blocks; check
// BlockType on the Raw type field if the distinction matters).
type ToolUseBlock struct {
	ID    string
	Name  string
	Input json.RawMessage
	Raw   json.RawMessage
}

// ToolResultBlock is a tool_result block. Content is either a plain string
// or a list of blocks.
type ToolResultBlock struct {
	ToolUseID string
	Content   Content
	IsError   bool
	Raw       json.RawMessage
}

// ImageBlock is an image block; Source is the Messages API image source.
type ImageBlock struct {
	Source json.RawMessage
	Raw    json.RawMessage
}

// UnknownBlock is any block type this package does not model.
type UnknownBlock struct {
	Type string
	Raw  json.RawMessage
}

func (*TextBlock) BlockType() string             { return "text" }
func (*ThinkingBlock) BlockType() string         { return "thinking" }
func (*RedactedThinkingBlock) BlockType() string { return "redacted_thinking" }
func (*ToolUseBlock) BlockType() string          { return "tool_use" }
func (*ToolResultBlock) BlockType() string       { return "tool_result" }
func (*ImageBlock) BlockType() string            { return "image" }
func (b *UnknownBlock) BlockType() string        { return b.Type }

func (b *TextBlock) RawJSON() json.RawMessage             { return b.Raw }
func (b *ThinkingBlock) RawJSON() json.RawMessage         { return b.Raw }
func (b *RedactedThinkingBlock) RawJSON() json.RawMessage { return b.Raw }
func (b *ToolUseBlock) RawJSON() json.RawMessage          { return b.Raw }
func (b *ToolResultBlock) RawJSON() json.RawMessage       { return b.Raw }
func (b *ImageBlock) RawJSON() json.RawMessage            { return b.Raw }
func (b *UnknownBlock) RawJSON() json.RawMessage          { return b.Raw }

func (*TextBlock) isBlock()             {}
func (*ThinkingBlock) isBlock()         {}
func (*RedactedThinkingBlock) isBlock() {}
func (*ToolUseBlock) isBlock()          {}
func (*ToolResultBlock) isBlock()       {}
func (*ImageBlock) isBlock()            {}
func (*UnknownBlock) isBlock()          {}

// MarshalJSON returns Raw when present, otherwise a canonical encoding.
func (b *TextBlock) MarshalJSON() ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{"text", b.Text})
}

// MarshalJSON returns Raw when present, otherwise a canonical encoding.
func (b *ThinkingBlock) MarshalJSON() ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	return json.Marshal(struct {
		Type      string `json:"type"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature,omitempty"`
	}{"thinking", b.Thinking, b.Signature})
}

// MarshalJSON returns Raw, or a minimal block when Raw is empty.
func (b *RedactedThinkingBlock) MarshalJSON() ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	return []byte(`{"type":"redacted_thinking"}`), nil
}

// MarshalJSON returns Raw when present, otherwise a canonical encoding.
func (b *ToolUseBlock) MarshalJSON() ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	in := b.Input
	if len(in) == 0 {
		in = json.RawMessage(`{}`)
	}
	return json.Marshal(struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}{"tool_use", b.ID, b.Name, in})
}

// MarshalJSON returns Raw when present, otherwise a canonical encoding.
func (b *ToolResultBlock) MarshalJSON() ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	return json.Marshal(struct {
		Type      string  `json:"type"`
		ToolUseID string  `json:"tool_use_id"`
		Content   Content `json:"content"`
		IsError   bool    `json:"is_error,omitempty"`
	}{"tool_result", b.ToolUseID, b.Content, b.IsError})
}

// MarshalJSON returns Raw when present, otherwise a canonical encoding.
func (b *ImageBlock) MarshalJSON() ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	return json.Marshal(struct {
		Type   string          `json:"type"`
		Source json.RawMessage `json:"source"`
	}{"image", b.Source})
}

// MarshalJSON returns Raw.
func (b *UnknownBlock) MarshalJSON() ([]byte, error) {
	if len(b.Raw) == 0 {
		return json.Marshal(struct {
			Type string `json:"type"`
		}{b.Type})
	}
	return b.Raw, nil
}

// ParseBlock decodes one content block. It never fails: anything it cannot
// model becomes an *UnknownBlock carrying the original JSON.
func ParseBlock(raw json.RawMessage) Block {
	raw = bytes.Clone(raw)
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return &UnknownBlock{Raw: raw}
	}
	unknown := &UnknownBlock{Type: head.Type, Raw: raw}
	switch head.Type {
	case "text":
		var v struct {
			Text string `json:"text"`
		}
		if unmarshalLoose(raw, &v) != nil {
			return unknown
		}
		return &TextBlock{Text: v.Text, Raw: raw}
	case "thinking":
		var v struct {
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}
		if unmarshalLoose(raw, &v) != nil {
			return unknown
		}
		return &ThinkingBlock{Thinking: v.Thinking, Signature: v.Signature, Raw: raw}
	case "redacted_thinking":
		return &RedactedThinkingBlock{Raw: raw}
	case "tool_use":
		var v struct {
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if unmarshalLoose(raw, &v) != nil {
			return unknown
		}
		return &ToolUseBlock{ID: v.ID, Name: v.Name, Input: v.Input, Raw: raw}
	case "tool_result":
		var v struct {
			ToolUseID string  `json:"tool_use_id"`
			Content   Content `json:"content"`
			IsError   bool    `json:"is_error"`
		}
		if unmarshalLoose(raw, &v) != nil {
			return unknown
		}
		return &ToolResultBlock{ToolUseID: v.ToolUseID, Content: v.Content, IsError: v.IsError, Raw: raw}
	case "image":
		var v struct {
			Source json.RawMessage `json:"source"`
		}
		if unmarshalLoose(raw, &v) != nil {
			return unknown
		}
		return &ImageBlock{Source: v.Source, Raw: raw}
	}
	return unknown
}

// Content is message content: a plain string (IsText) or a list of blocks.
// Tool results and user messages both use it.
type Content struct {
	IsText bool
	Text   string
	Blocks []Block
}

// TextContent returns plain-string content.
func TextContent(s string) Content { return Content{IsText: true, Text: s} }

// BlocksContent returns block-list content.
func BlocksContent(blocks ...Block) Content { return Content{Blocks: blocks} }

// String returns the text of a string content, or the concatenated text of
// its text blocks.
func (c Content) String() string {
	if c.IsText {
		return c.Text
	}
	var sb strings.Builder
	for _, b := range c.Blocks {
		if t, ok := b.(*TextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

// UnmarshalJSON accepts a string, an array of blocks, or null.
func (c *Content) UnmarshalJSON(data []byte) error {
	*c = Content{}
	data = bytes.TrimSpace(data)
	switch {
	case len(data) == 0 || bytes.Equal(data, []byte("null")):
		return nil
	case data[0] == '"':
		c.IsText = true
		return json.Unmarshal(data, &c.Text)
	case data[0] == '[':
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		for _, it := range items {
			c.Blocks = append(c.Blocks, ParseBlock(it))
		}
		return nil
	}
	// Unmodelled shape (object, number): keep it as a single unknown block.
	c.Blocks = []Block{&UnknownBlock{Raw: bytes.Clone(data)}}
	return nil
}

// MarshalJSON encodes a string or a block list.
func (c Content) MarshalJSON() ([]byte, error) {
	if c.IsText {
		return json.Marshal(c.Text)
	}
	if c.Blocks == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(c.Blocks)
}
