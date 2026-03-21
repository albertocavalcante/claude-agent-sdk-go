package claude

import "encoding/json"

// ContentBlock is the interface implemented by all content block types
// within an AssistantMessage.
type ContentBlock interface {
	// BlockType returns the type identifier for this content block
	// (e.g. "text", "tool_use", "tool_result", "thinking").
	BlockType() string
}

// TextBlock represents a text content block.
type TextBlock struct {
	Text string `json:"text"`
}

// BlockType returns "text".
func (b *TextBlock) BlockType() string { return "text" }

// ToolUseBlock represents a tool invocation by the model.
type ToolUseBlock struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// BlockType returns "tool_use".
func (b *ToolUseBlock) BlockType() string { return "tool_use" }

// ToolResultBlock represents the result of a tool invocation.
type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// BlockType returns "tool_result".
func (b *ToolResultBlock) BlockType() string { return "tool_result" }

// ThinkingBlock represents an extended thinking content block.
type ThinkingBlock struct {
	Thinking string `json:"thinking"`
}

// BlockType returns "thinking".
func (b *ThinkingBlock) BlockType() string { return "thinking" }

// rawContentBlock is used for intermediate JSON unmarshalling of content blocks.
type rawContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// parseContentBlock converts a rawContentBlock into a typed ContentBlock.
// Unknown block types are silently skipped (returns nil).
func parseContentBlock(raw rawContentBlock) ContentBlock {
	switch raw.Type {
	case "text":
		return &TextBlock{Text: raw.Text}
	case "tool_use":
		return &ToolUseBlock{
			ID:    raw.ID,
			Name:  raw.Name,
			Input: raw.Input,
		}
	case "tool_result":
		return &ToolResultBlock{
			ToolUseID: raw.ToolUseID,
			Content:   raw.Content,
			IsError:   raw.IsError,
		}
	case "thinking":
		return &ThinkingBlock{Thinking: raw.Thinking}
	default:
		// Unknown content block types are silently skipped.
		return nil
	}
}
