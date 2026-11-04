package kiro

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

// Fixtures follow reference DTO wire fields; all values are synthetic.
func readEvent(t *testing.T, typ, p string) Event {
	t.Helper()
	e, err := decoder(t, bytes.NewReader(eventFrame("event", typ, p)), Limits{}).NextEvent(0)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestActualEventShapes(t *testing.T) {
	e := readEvent(t, "assistantResponseEvent", `{"content":"hello","modelId":"synthetic-model","future":true}`)
	v, ok := e.Assistant()
	if !ok || v.Content != "hello" || v.ModelID != "synthetic-model" {
		t.Fatal(v)
	}
	c, ok := readEvent(t, "contextUsageEvent", `{"contextUsagePercentage":12.5}`).Context()
	if !ok || c.ContextUsagePercentage == nil || *c.ContextUsagePercentage != 12.5 {
		t.Fatal(c)
	}
	m, ok := readEvent(t, "meteringEvent", `{"unit":"credit","unitPlural":"credits","usage":0.25,"cache_read_input_tokens":0,"cache_creation_input_tokens":3,"future":true}`).Metering()
	if !ok || m.Usage == nil || *m.Usage != 0.25 || m.CacheReadInputTokens == nil || *m.CacheReadInputTokens != 0 || m.CacheCreationInputTokens == nil || *m.CacheCreationInputTokens != 3 {
		t.Fatal(m)
	}
	*m.Usage = 999
	again, _ := readEvent(t, "meteringEvent", `{"usage":0.25}`).Metering()
	if *again.Usage != 0.25 {
		t.Fatal("alias")
	}
	unknown := readEvent(t, "futureTerminalEvent", "arbitrary future bytes")
	if string(unknown.Raw()) != "arbitrary future bytes" || unknown.Type() != "futureTerminalEvent" || len(unknown.Headers()) != 2 {
		t.Fatal("lost unknown")
	}
	var a Attempt
	if _, err := a.Apply(unknown); err != nil {
		t.Fatal(err)
	}
	if a.End(io.EOF) != ErrIncomplete {
		t.Fatal("false success")
	}
}
func TestAttemptToolAndText(t *testing.T) {
	var a Attempt
	e := readEvent(t, "assistantResponseEvent", `{"content":"hi"}`)
	d, err := a.Apply(e)
	if err != nil || d.Text != "hi" {
		t.Fatal(d, err)
	}
	for _, p := range []string{`{"name":"lookup","toolUseId":"tool-1","input":"{","stop":false}`, `{"name":"lookup","toolUseId":"tool-1","input":"}","stop":true}`} {
		d, err = a.Apply(readEvent(t, "toolUseEvent", p))
		if err != nil || d.ToolUseID != "tool-1" {
			t.Fatal(d, err)
		}
	}
	if !d.ToolStop {
		t.Fatal("missing stop")
	}
	// A tool stop does not terminate the generation or prevent other tools/text.
	if _, err = a.Apply(e); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Apply(readEvent(t, "toolUseEvent", `{"name":"lookup","toolUseId":"tool-1","input":"again"}`)); err != ErrState {
		t.Fatal(err)
	}
	if _, err = a.Apply(e); err != ErrState {
		t.Fatal("not sticky")
	}
	var b Attempt
	if _, err = b.Apply(readEvent(t, "toolUseEvent", `{"name":"one","toolUseId":"id"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Apply(readEvent(t, "toolUseEvent", `{"name":"two","toolUseId":"id"}`)); err != ErrState {
		t.Fatal(err)
	}
	for _, typ := range []string{"meteringEvent", "contextUsageEvent", "assistantResponseEvent", "toolUseEvent", "unknown"} {
		payload := `{}`
		switch typ {
		case "meteringEvent":
			payload = `{"usage":0}`
		case "toolUseEvent":
			payload = `{"name":"x","toolUseId":"id","stop":true}`
		}
		var s Attempt
		if _, err := s.Apply(readEvent(t, typ, payload)); err != nil {
			t.Fatal(err)
		}
		if s.End(nil) != ErrIncomplete {
			t.Fatal("invented completion", typ)
		}
	}
}
func TestInvalidEventsFailClosed(t *testing.T) {
	for _, tc := range []struct{ typ, p string }{
		{"assistantResponseEvent", `{"content":4}`}, {"assistantResponseEvent", `null`}, {"assistantResponseEvent", `[]`},
		{"toolUseEvent", `{}`}, {"toolUseEvent", `{"name":"x","toolUseId":"x","stop":1}`},
		{"meteringEvent", `{}`}, {"meteringEvent", `{"usage":null}`}, {"meteringEvent", `{"usage":"synthetic-secret"}`},
		{"contextUsageEvent", `{"contextUsagePercentage":"secret"}`},
	} {
		wire := append(eventFrame("event", tc.typ, tc.p), eventFrame("event", "unknown", `{}`)...)
		d := decoder(t, bytes.NewReader(wire), Limits{})
		if _, err := d.NextEvent(0); err != ErrEvent {
			t.Fatal(tc, err)
		}
		if _, err := d.NextEvent(0); err != ErrEvent {
			t.Fatal("recovery")
		}
	}
	for _, h := range [][]byte{nil, header(":message-type", 0, nil), header(":message-type", 7, []byte("future")), header(":message-type", 7, []byte("event"))} {
		if _, err := decoder(t, bytes.NewReader(frame(h, []byte("secret"))), Limits{}).NextEvent(0); err != ErrEvent {
			t.Fatal(err)
		}
	}
	if _, err := decoder(t, bytes.NewReader(eventFrame("event", "unknown", "12345")), Limits{}).NextEvent(4); err != ErrLimit {
		t.Fatal(err)
	}
}
func TestRequestDoesNotRewriteHistory(t *testing.T) {
	state := json.RawMessage(`{"conversationId":"synthetic","currentMessage":{"userInputMessage":{"content":"hi","modelId":"synthetic-model"}},"history":[{"assistantResponseMessage":{"content":"unchanged"}}],"future":42}`)
	r := Request{ConversationState: state, AdditionalModelRequestFields: json.RawMessage(`{"max_tokens":5}`)}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.ConversationState, state) || got.ProfileARN != "" {
		t.Fatal(string(b))
	}
}

func TestAttemptMetadataBound(t *testing.T) {
	var a Attempt
	e := readEvent(t, "toolUseEvent", `{"name":"lookup","toolUseId":"tool"}`)
	a.bytes = 128 * 1024
	if _, err := a.Apply(e); err != ErrLimit {
		t.Fatal(err)
	}
	var b Attempt
	b.tools = make(map[string]toolState)
	for i := 0; i < 1024; i++ {
		b.tools[string(rune(i))] = toolState{name: "x"}
	}
	if _, err := b.Apply(e); err != ErrLimit {
		t.Fatal(err)
	}
	var c Attempt
	if c.End(io.ErrUnexpectedEOF) != io.ErrUnexpectedEOF || c.End(nil) != io.ErrUnexpectedEOF {
		t.Fatal("lost truncation")
	}
}

func TestToolContinuationAndFinalizationOnce(t *testing.T) {
	var a Attempt
	for i, p := range []string{`{"name":"lookup","toolUseId":"id","input":"{"}`, `{"toolUseId":"id","input":"\"n\":1"}`, `{"input":"}"}`, `{"stop":true}`} {
		d, err := a.Apply(readEvent(t, "toolUseEvent", p))
		if err != nil || d.ToolUseID != "id" || d.ToolName != "lookup" || d.ToolStop != (i == 3) {
			t.Fatal(i, d, err)
		}
	}
	if a.End(io.EOF) != ErrIncomplete || a.End(io.ErrUnexpectedEOF) != ErrIncomplete {
		t.Fatal("finalized twice")
	}
	if _, err := a.Apply(readEvent(t, "assistantResponseEvent", `{"content":"late"}`)); err != ErrIncomplete {
		t.Fatal("accepted event after EOF")
	}
	for _, ps := range [][]string{
		{`{"input":"orphan"}`},
		{`{"name":"f","toolUseId":"a"}`, `{"name":"g","toolUseId":"b"}`, `{"input":"ambiguous"}`},
		{`{"name":"f","toolUseId":"a","stop":true}`, `{"toolUseId":"a","input":"late"}`},
		{`{"toolUseId":"unknown","input":"orphan"}`},
	} {
		var b Attempt
		for i, p := range ps {
			_, err := b.Apply(readEvent(t, "toolUseEvent", p))
			if (i == len(ps)-1) != (err == ErrState) {
				t.Fatal(i, err)
			}
		}
	}
}
