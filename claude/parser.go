package claude

import (
	"encoding/json"
)

// rawEnvelope is used for intermediate JSON unmarshalling to inspect the type field.
type rawEnvelope struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
}

// rawAssistantMessage is the JSON shape of an assistant message.
type rawAssistantMessage struct {
	Content    []json.RawMessage `json:"content"`
	Model      string            `json:"model,omitempty"`
	StopReason string            `json:"stop_reason,omitempty"`
}

// rawResultMessage is the JSON shape of a result message.
type rawResultMessage struct {
	IsError      bool     `json:"is_error,omitempty"`
	Duration     float64  `json:"duration_ms,omitempty"`
	Cost         float64  `json:"cost_usd,omitempty"`
	InputTokens  int      `json:"input_tokens,omitempty"`
	OutputTokens int      `json:"output_tokens,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	NumTurns     int      `json:"num_turns,omitempty"`
	TotalCost    *float64 `json:"total_cost_usd,omitempty"`
	Usage        *struct {
		InputTokens  int `json:"input_tokens,omitempty"`
		OutputTokens int `json:"output_tokens,omitempty"`
	} `json:"usage,omitempty"`
}

// rawSystemMessage is the JSON shape of a system message.
type rawSystemMessage struct {
	Subtype string `json:"subtype,omitempty"`
}

// rawUserMessage is the JSON shape of a user message.
type rawUserMessage struct {
	Content json.RawMessage `json:"content"`
}

// ParseMessage parses a single JSON line from the CLI into a typed Message.
// Unknown message types are returned as *UnknownMessage rather than
// producing an error, ensuring forward compatibility.
func ParseMessage(data []byte) (Message, error) {
	var envelope rawEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, &ProtocolError{
			Message: "failed to parse JSON line: " + err.Error(),
			Raw:     cloneBytes(data),
		}
	}

	switch envelope.Type {
	case "assistant":
		return parseAssistantMessage(data)
	case "user":
		return parseUserMessage(data)
	case "result":
		return parseResultMessage(data)
	case "system":
		return parseSystemMessage(data)
	default:
		return &UnknownMessage{
			RawType: envelope.Type,
			Raw:     json.RawMessage(cloneBytes(data)),
		}, nil
	}
}

func parseAssistantMessage(data []byte) (*AssistantMessage, error) {
	var raw struct {
		rawAssistantMessage
		Message *rawAssistantMessage `json:"message"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &ProtocolError{
			Message: "failed to parse assistant message: " + err.Error(),
			Raw:     cloneBytes(data),
		}
	}
	// CLI events wrap the model message. Retain support for the original
	// flattened representation, with nested fields taking precedence.
	if raw.Message != nil {
		raw.rawAssistantMessage = *raw.Message
	}

	msg := &AssistantMessage{
		Model:      raw.Model,
		StopReason: raw.StopReason,
	}

	for _, block := range raw.Content {
		cb, err := parseContentBlockFromJSON(block)
		if err != nil {
			return nil, &ProtocolError{
				Message: "failed to parse content block: " + err.Error(),
				Raw:     cloneBytes(block),
			}
		}
		msg.Content = append(msg.Content, cb)
	}

	return msg, nil
}

func parseUserMessage(data []byte) (*UserMessage, error) {
	var raw struct {
		rawUserMessage
		Message *rawUserMessage `json:"message"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &ProtocolError{
			Message: "failed to parse user message: " + err.Error(),
			Raw:     cloneBytes(data),
		}
	}
	if raw.Message != nil {
		raw.rawUserMessage = *raw.Message
	}

	msg := &UserMessage{}
	if len(raw.Content) == 0 {
		return msg, nil
	}
	// User turns may contain plain text instead of a content-block array.
	if raw.Content[0] == '"' {
		var text string
		if err := json.Unmarshal(raw.Content, &text); err != nil {
			return nil, &ProtocolError{Message: "failed to parse user content: " + err.Error(), Raw: cloneBytes(data)}
		}
		msg.Content = []ContentBlock{&TextBlock{Text: text}}
		return msg, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw.Content, &blocks); err != nil {
		return nil, &ProtocolError{Message: "failed to parse user content: " + err.Error(), Raw: cloneBytes(data)}
	}

	for _, block := range blocks {
		cb, err := parseContentBlockFromJSON(block)
		if err != nil {
			return nil, &ProtocolError{
				Message: "failed to parse content block: " + err.Error(),
				Raw:     cloneBytes(block),
			}
		}
		msg.Content = append(msg.Content, cb)
	}

	return msg, nil
}

func parseResultMessage(data []byte) (*ResultMessage, error) {
	var raw rawResultMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &ProtocolError{
			Message: "failed to parse result message: " + err.Error(),
			Raw:     cloneBytes(data),
		}
	}
	if raw.TotalCost != nil {
		raw.Cost = *raw.TotalCost
	}
	if raw.Usage != nil {
		raw.InputTokens = raw.Usage.InputTokens
		raw.OutputTokens = raw.Usage.OutputTokens
	}

	return &ResultMessage{
		IsError:      raw.IsError,
		Duration:     raw.Duration,
		Cost:         raw.Cost,
		InputTokens:  raw.InputTokens,
		OutputTokens: raw.OutputTokens,
		SessionID:    raw.SessionID,
		NumTurns:     raw.NumTurns,
	}, nil
}

func parseSystemMessage(data []byte) (*SystemMessage, error) {
	var raw rawSystemMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &ProtocolError{
			Message: "failed to parse system message: " + err.Error(),
			Raw:     cloneBytes(data),
		}
	}

	return &SystemMessage{
		Subtype: raw.Subtype,
		Raw:     json.RawMessage(cloneBytes(data)),
	}, nil
}

// cloneBytes returns a copy of the byte slice.
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	c := make([]byte, len(b))
	copy(c, b)
	return c
}
