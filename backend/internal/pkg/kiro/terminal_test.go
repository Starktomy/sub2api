package kiro

import (
	"io"
	"testing"
)

func TestVerifiedTerminalRequiresEndTurnAndCleanEOF(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		end          error
		open         bool
		want         bool
	}{
		{"normal", "END_TURN", io.EOF, false, true},
		{"missing", "", io.EOF, false, false},
		{"unknown", "UNKNOWN", io.EOF, false, false},
		{"truncated", "END_TURN", io.ErrUnexpectedEOF, false, false},
		{"open-tool", "END_TURN", io.EOF, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := new(Attempt)
			if tc.open {
				_, _ = a.Apply(Event{messageType: "event", eventType: "toolUseEvent", raw: []byte(`{"toolUseId":"t","name":"f","input":"{"}`)})
			}
			if tc.reason != "" {
				_, err := a.Apply(Event{messageType: "event", eventType: "metadataEvent", raw: []byte(`{"stopReason":"` + tc.reason + `"}`)})
				if err != nil {
					t.Fatal(err)
				}
			}
			err := a.End(tc.end)
			if a.CompletionVerified() != tc.want || (err == nil) != tc.want {
				t.Fatalf("verified=%t err=%v", a.CompletionVerified(), err)
			}
			if a.End(io.EOF) != err {
				t.Fatal("terminal changed")
			}
		})
	}
}
func TestTerminalRejectsDuplicateAndLaterContent(t *testing.T) {
	for _, event := range []Event{
		{messageType: "event", eventType: "metadataEvent", raw: []byte(`{"stopReason":"END_TURN"}`)},
		{messageType: "event", eventType: "assistantResponseEvent", raw: []byte(`{"content":"late"}`)},
		{messageType: "exception", eventType: "exception", raw: []byte(`{}`)},
	} {
		a := new(Attempt)
		_, _ = a.Apply(Event{messageType: "event", eventType: "metadataEvent", raw: []byte(`{"stopReason":"END_TURN"}`)})
		if _, err := a.Apply(event); err == nil {
			t.Fatal("late event accepted")
		}
		if a.End(io.EOF) == nil || a.CompletionVerified() {
			t.Fatal("late error became success")
		}
	}
}
