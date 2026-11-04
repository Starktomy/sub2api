package service

import (
	"context"
	"encoding/json"
	"io"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/google/uuid"
)

// ExecutePrivateMessages bridges supported Messages text/tool requests to
// native Kiro and resolved streaming deltas. It does not expose a public route or
// emit fabricated token counts, stop_reason=end_turn, message_stop or success.
// Unknown completion ends with an explicit error event, even after partial text.
func (s *KiroNativeService) ExecutePrivateMessages(ctx context.Context, accountID int64, operationID string, body []byte, w io.Writer) (kiro.Result, error) {
	input, err := kiro.ConvertMessagesText(body, uuid.NewString())
	if err != nil {
		return kiro.Result{}, err
	}
	emit := func(name string, value any) error {
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, "event: "+name+"\ndata: "+string(b)+"\n\n")
		if f, ok := w.(interface{ Flush() }); ok {
			f.Flush()
		}
		return err
	}
	var attempt kiro.Attempt
	indexes := map[string]int{}
	nextIndex := 0
	textIndex := -1
	active := -1
	activeTool := false
	closeBlock := func() error {
		if active < 0 {
			return nil
		}
		i := active
		active = -1
		return emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	result, err := s.ExecutePrivate(ctx, accountID, operationID, input, func(e kiro.Event) error {
		d, err := attempt.Apply(e)
		if err != nil {
			return err
		}
		if d.Text != "" {
			if activeTool {
				return kiro.ErrState
			}
			if textIndex < 0 || active != textIndex {
				if err := closeBlock(); err != nil {
					return err
				}
				textIndex = nextIndex
				nextIndex++
				active = textIndex
				if err := emit("content_block_start", map[string]any{"type": "content_block_start", "index": textIndex, "content_block": map[string]any{"type": "text", "text": ""}}); err != nil {
					return err
				}
			}
			if active != textIndex {
				return kiro.ErrState
			}
			return emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": textIndex, "delta": map[string]any{"type": "text_delta", "text": d.Text}})
		}
		if d.ToolUseID != "" {
			i, exists := indexes[d.ToolUseID]
			if !exists {
				if activeTool {
					return kiro.ErrState
				}
				if err := closeBlock(); err != nil {
					return err
				}
				i = nextIndex
				nextIndex++
				indexes[d.ToolUseID] = i
				active = i
				activeTool = true
				if err := emit("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": d.ToolUseID, "name": d.ToolName, "input": map[string]any{}}}); err != nil {
					return err
				}
			}
			if active != i {
				return kiro.ErrState
			}
			if d.ToolInput != "" {
				if err := emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": d.ToolInput}}); err != nil {
					return err
				}
			}
			if d.ToolStop {
				activeTool = false
				return closeBlock()
			}
		}
		return nil
	})
	if err != nil {
		_ = emit("error", map[string]any{"type": "error", "error": map[string]any{"type": "kiro_completion_unverified", "message": "Native Kiro completion or usage could not be verified; do not replay automatically"}})
	}
	return result, err
}
