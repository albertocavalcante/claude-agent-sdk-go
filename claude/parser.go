package claude

import (
	"encoding/json"
	"log"
)

// rawEnvelope is used for intermediate JSON unmarshalling to inspect the type field.
type rawEnvelope struct {
	Type string `json:"type"`
}

// rawAssistantMessage is the JSON shape of an assistant message.
type rawAssistantMessage struct {
	Content    []rawContentBlock `json:"content"`
	Model      string            `json:"model,omitempty"`
	StopReason string            `json:"stop_reason,omitempty"`
}

// rawResultMessage is the JSON shape of a result message.
type rawResultMessage struct {
	IsError      bool    `json:"is_error,omitempty"`
	Duration     float64 `json:"duration_ms,omitempty"`
	Cost         float64 `json:"cost_usd,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	SessionID    string  `json:"session_id,omitempty"`
	NumTurns     int     `json:"num_turns,omitempty"`
}

// rawSystemMessage is the JSON shape of a system message.
type rawSystemMessage struct {
	Subtype string `json:"subtype,omitempty"`
}

// rawUserMessage is the JSON shape of a user message.
type rawUserMessage struct {
	Content []rawContentBlock `json:"content"`
}

// ParseMessage parses a single JSON line from the CLI into a typed Message.
// Unknown message types are returned as *UnknownMessage rather than
// producing an error, ensuring forward compatibility.
func ParseMessage(data []byte) (Message, error) {
	var envelope rawEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, &ProtocolError{
			Message: "failed to parse JSON line: " + err.Error(),
			Raw:     data,
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
		log.Printf("claude-agent-sdk: unknown message type %q, wrapping as UnknownMessage", envelope.Type)
		raw := make(json.RawMessage, len(data))
		copy(raw, data)
		return &UnknownMessage{
			RawType: envelope.Type,
			Raw:     raw,
		}, nil
	}
}

func parseAssistantMessage(data []byte) (*AssistantMessage, error) {
	var raw rawAssistantMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &ProtocolError{
			Message: "failed to parse assistant message: " + err.Error(),
			Raw:     data,
		}
	}

	msg := &AssistantMessage{
		Model:      raw.Model,
		StopReason: raw.StopReason,
	}

	for _, block := range raw.Content {
		cb := parseContentBlock(block)
		if cb != nil {
			msg.Content = append(msg.Content, cb)
		}
	}

	return msg, nil
}

func parseUserMessage(data []byte) (*UserMessage, error) {
	var raw rawUserMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &ProtocolError{
			Message: "failed to parse user message: " + err.Error(),
			Raw:     data,
		}
	}

	msg := &UserMessage{}

	for _, block := range raw.Content {
		cb := parseContentBlock(block)
		if cb != nil {
			msg.Content = append(msg.Content, cb)
		}
	}

	return msg, nil
}

func parseResultMessage(data []byte) (*ResultMessage, error) {
	var raw rawResultMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &ProtocolError{
			Message: "failed to parse result message: " + err.Error(),
			Raw:     data,
		}
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
			Raw:     data,
		}
	}

	rawCopy := make(json.RawMessage, len(data))
	copy(rawCopy, data)

	return &SystemMessage{
		Subtype: raw.Subtype,
		Raw:     rawCopy,
	}, nil
}
