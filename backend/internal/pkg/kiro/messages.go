// Copyright (c) 2026 hank9999. MIT; see LICENSE.
// DTO mapping references kiro.rs 8fb3dd691d1cedf437bd6a4e9ef9f60b3f217fdd,
// src/kiro/model/requests/{conversation,tool}.rs. No history repair is copied.
package kiro

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

func strictJSON(raw []byte, dst any) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	return d.Decode(dst) == nil && d.Decode(new(any)) == io.EOF
}

type messageBlock struct {
	Type      string          `json:"type"`
	Text      *string         `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   *bool           `json:"is_error,omitempty"`
}

// textParts preserves text order, without adding separators. Other semantics,
// including cache controls and citations, fail closed.
func textParts(raw json.RawMessage) ([]map[string]any, string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil && string(bytes.TrimSpace(raw)) != "null" {
		return []map[string]any{{"text": s}}, s, true
	}
	var blocks []messageBlock
	if !strictJSON(raw, &blocks) || len(blocks) == 0 || len(blocks) > 1024 {
		return nil, "", false
	}
	parts := make([]map[string]any, 0, len(blocks))
	var text strings.Builder
	for _, b := range blocks {
		if b.Type != "text" || b.Text == nil || b.ID != "" || b.Name != "" || b.Input != nil || b.ToolUseID != "" || b.Content != nil || b.IsError != nil {
			return nil, "", false
		}
		parts = append(parts, map[string]any{"text": *b.Text})
		text.WriteString(*b.Text)
	}
	return parts, text.String(), true
}

func objectJSON(raw json.RawMessage) bool {
	b := bytes.TrimSpace(raw)
	return len(b) > 0 && b[0] == '{' && json.Valid(b)
}

// ConvertMessagesText retains its original API but also supports client tool
// schemas, assistant tool history, and matching user text tool results. Mixed
// text/tool blocks are rejected: Kiro's separate content/tool arrays cannot
// represent their ordering losslessly. System remains unsupported: the reference
// represents it by inventing a user/assistant pair, not a native system field.
// Models and IDs are never aliased, truncated, synthesized or repaired.
func ConvertMessagesText(body []byte, conversationID string) (Request, error) {
	var output Request
	if len(body) == 0 || len(body) > 1<<20 || !utf8.Valid(body) || len(conversationID) == 0 || len(conversationID) > 128 || !utf8.ValidString(conversationID) {
		return output, ErrRequest
	}
	var in struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Stream    bool   `json:"stream"`
		Tools     []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if !strictJSON(body, &in) || in.Model == "" || len(in.Model) > 128 || in.MaxTokens < 1 || in.MaxTokens > 32768 || len(in.Messages) == 0 || len(in.Messages) > 128 || len(in.Tools) > 128 {
		return output, ErrRequest
	}
	tools := []any{}
	names := map[string]bool{}
	for _, t := range in.Tools {
		if t.Name == "" || len(t.Name) > 128 || names[t.Name] || !objectJSON(t.InputSchema) {
			return output, ErrRequest
		}
		names[t.Name] = true
		tools = append(tools, map[string]any{"toolSpecification": map[string]any{"name": t.Name, "description": t.Description, "inputSchema": map[string]any{"json": t.InputSchema}}})
	}
	history := []any{}
	var current map[string]any
	pending := map[string]bool{}
	seen := map[string]bool{}
	for i, m := range in.Messages {
		if (i%2 == 0 && m.Role != "user") || (i%2 == 1 && m.Role != "assistant") {
			return output, ErrRequest
		}
		context := map[string]any{}
		msg := map[string]any{}
		_, text, isText := textParts(m.Content)
		if isText {
			if text == "" || len(pending) > 0 {
				return output, ErrRequest
			}
			msg["content"] = text
		} else {
			var blocks []messageBlock
			if !strictJSON(m.Content, &blocks) || len(blocks) == 0 || len(blocks) > 128 {
				return output, ErrRequest
			}
			entries := []any{}
			for _, b := range blocks {
				if b.Text != nil {
					return output, ErrRequest
				}
				if m.Role == "assistant" {
					if b.Type != "tool_use" || b.ID == "" || len(b.ID) > 256 || b.Name == "" || len(b.Name) > 128 || !objectJSON(b.Input) || seen[b.ID] || b.ToolUseID != "" || b.Content != nil || b.IsError != nil {
						return output, ErrRequest
					}
					seen[b.ID] = true
					pending[b.ID] = true
					entries = append(entries, map[string]any{"toolUseId": b.ID, "name": b.Name, "input": b.Input})
				} else {
					if b.Type != "tool_result" || !pending[b.ToolUseID] || b.ID != "" || b.Name != "" || b.Input != nil {
						return output, ErrRequest
					}
					parts, _, ok := textParts(b.Content)
					if !ok {
						return output, ErrRequest
					}
					status := "success"
					isError := b.IsError != nil && *b.IsError
					if isError {
						status = "error"
					}
					entries = append(entries, map[string]any{"toolUseId": b.ToolUseID, "content": parts, "status": status, "isError": isError})
					delete(pending, b.ToolUseID)
				}
			}
			msg["content"] = ""
			if m.Role == "assistant" {
				msg["toolUses"] = entries
			} else {
				if len(pending) != 0 {
					return output, ErrRequest
				}
				context["toolResults"] = entries
			}
		}
		if m.Role == "user" {
			msg["origin"] = "KIRO_CLI"
			msg["modelId"] = in.Model
			msg["userInputMessageContext"] = context
			if i == len(in.Messages)-1 {
				if len(tools) > 0 {
					context["tools"] = tools
				}
				current = map[string]any{"userInputMessage": msg}
			} else {
				history = append(history, map[string]any{"userInputMessage": msg})
			}
		} else {
			history = append(history, map[string]any{"assistantResponseMessage": msg})
		}
	}
	if current == nil || len(pending) != 0 {
		return output, ErrRequest
	}
	state := map[string]any{"conversationId": conversationID, "currentMessage": current, "chatTriggerType": "MANUAL"}
	if len(history) > 0 {
		state["history"] = history
	}
	output.ConversationState, _ = json.Marshal(state)
	// Fork extension only; provider enforcement remains unverified.
	output.AdditionalModelRequestFields, _ = json.Marshal(map[string]any{"maxTokens": in.MaxTokens})
	return output, nil
}
