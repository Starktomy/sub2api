package kiro

import (
	"encoding/json"
	"testing"
)

func TestConvertMessagesTextPreservesOrderAndBound(t *testing.T) {
	r, err := ConvertMessagesText([]byte(`{"model":"explicit-model","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"one"},{"role":"assistant","content":"two"},{"role":"user","content":[{"type":"text","text":"three"}]}]}`), "conversation")
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if json.Unmarshal(r.ConversationState, &state) != nil {
		t.Fatal("invalid state")
	}
	history := state["history"].([]any)
	if len(history) != 2 {
		t.Fatal("history rewritten")
	}
	current := state["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
	if current["content"] != "three" || current["modelId"] != "explicit-model" {
		t.Fatal("content changed")
	}
	if string(r.AdditionalModelRequestFields) != `{"maxTokens":32}` {
		t.Fatal("bound not represented")
	}
}
func TestConvertMessagesTextRejectsUnsupportedSemantics(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","max_tokens":32,"system":"never silently drop","messages":[{"role":"user","content":"x"}]}`,
		`{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"x"},{"role":"user","content":"y"}]}`,
		`{"model":"m","max_tokens":32,"messages":[{"role":"assistant","content":"x"}]}`,
		`{"model":"m","max_tokens":32,"messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`,
		`{"model":"m","max_tokens":0,"messages":[{"role":"user","content":"x"}]}`,
	} {
		if _, err := ConvertMessagesText([]byte(body), "c"); err == nil {
			t.Fatalf("accepted unsupported request")
		}
	}
}

func TestConvertToolHistory(t *testing.T) {
	body := []byte(`{"model":"unchanged","max_tokens":32,"tools":[{"name":"lookup","description":"exact description","input_schema":{"type":"object","properties":{"n":{"type":"integer"}}}}],"messages":[{"role":"user","content":"question"},{"role":"assistant","content":[{"type":"tool_use","id":"id-1","name":"lookup","input":{"n":9007199254740993}},{"type":"tool_use","id":"id-2","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"id-2","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"is_error":true},{"type":"tool_result","tool_use_id":"id-1","content":"answer"}]}]}`)
	r, err := ConvertMessagesText(body, "conversation")
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		History []struct {
			Assistant struct {
				ToolUses []struct {
					ID    string          `json:"toolUseId"`
					Input json.RawMessage `json:"input"`
				} `json:"toolUses"`
			} `json:"assistantResponseMessage"`
		} `json:"history"`
		Current struct {
			User struct {
				Context struct {
					Results []struct {
						ID      string `json:"toolUseId"`
						Status  string `json:"status"`
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"toolResults"`
					Tools []json.RawMessage `json:"tools"`
				} `json:"userInputMessageContext"`
			} `json:"userInputMessage"`
		} `json:"currentMessage"`
	}
	if err = json.Unmarshal(r.ConversationState, &state); err != nil {
		t.Fatal(err)
	}
	uses := state.History[1].Assistant.ToolUses
	results := state.Current.User.Context.Results
	if len(uses) != 2 || uses[0].ID != "id-1" || string(uses[0].Input) != `{"n":9007199254740993}` || len(results) != 2 || results[0].ID != "id-2" || results[0].Status != "error" || results[0].Content[0].Text != "a" || results[0].Content[1].Text != "b" || len(state.Current.User.Context.Tools) != 1 {
		t.Fatal("tool order/content changed")
	}
}

func TestConvertToolHistoryFailsClosed(t *testing.T) {
	prefix := `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"q"},{"role":"assistant","content":`
	for _, tail := range []string{
		`[{"type":"tool_use","id":"a","name":"f","input":{}}]},{"role":"user","content":"missing result"}]}`,
		`[{"type":"tool_use","id":"a","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"orphan"}]}]}`,
		`[{"type":"tool_use","id":"a","name":"f","input":{}},{"type":"tool_use","id":"a","name":"f","input":{}}]},{"role":"user","content":"x"}]}`,
		`[{"type":"tool_use","id":"a","name":"f","input":{}},{"type":"text","text":"mixed"}]},{"role":"user","content":"x"}]}`,
		`[{"type":"tool_use","id":"a","name":"f","input":[]}]},{"role":"user","content":"x"}]}`,
		`[{"type":"tool_use","id":"a","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"x"},{"type":"tool_result","tool_use_id":"a","content":"duplicate"}]}]}`,
		`[{"type":"tool_use","id":"a","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"image","source":{}}]}]}]}`,
	} {
		if _, err := ConvertMessagesText([]byte(prefix+tail), "c"); err != ErrRequest {
			t.Fatal("accepted invalid tool history", err)
		}
	}
	for _, body := range []string{
		`{"model":"m","max_tokens":1,"tools":[{"name":"f","input_schema":{}},{"name":"f","input_schema":{}}],"messages":[{"role":"user","content":"x"}]}`,
		`{"model":"m","max_tokens":1,"tools":[{"name":"f","input_schema":{},"strict":true}],"messages":[{"role":"user","content":"x"}]}`,
		`{"model":"m","max_tokens":1,"system":[{"type":"text","text":"system"}],"messages":[{"role":"user","content":"x"}]}`,
		`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{}}]}]}`,
	} {
		if _, err := ConvertMessagesText([]byte(body), "c"); err != ErrRequest {
			t.Fatal("accepted unsupported semantics", err)
		}
	}
}

func FuzzConvertMessages(f *testing.F) {
	f.Add([]byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := ConvertMessagesText(b, "c")
		if err == nil && (!json.Valid(r.ConversationState) || !json.Valid(r.AdditionalModelRequestFields)) {
			t.Fatal("invalid converted JSON")
		}
	})
}

func TestConvertBoundsAndNull(t *testing.T) {
	valid := []byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`)
	for _, id := range []string{"", string(make([]byte, 129)), string([]byte{0xff})} {
		if _, err := ConvertMessagesText(valid, id); err != ErrRequest {
			t.Fatal("invalid conversation ID")
		}
	}
	for _, body := range [][]byte{nil, make([]byte, (1<<20)+1), []byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content": null }]}`), append(valid, []byte(` {}`)...), []byte(`{"model":"m","max_tokens":32769,"messages":[{"role":"user","content":"x"}]}`)} {
		if _, err := ConvertMessagesText(body, "c"); err != ErrRequest {
			t.Fatal("accepted invalid/bound", err)
		}
	}
}
