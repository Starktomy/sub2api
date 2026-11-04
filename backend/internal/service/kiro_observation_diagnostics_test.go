package service

import (
	"strings"
	"testing"
)

func TestKiroObservationCodeRedaction(t *testing.T) {
	for _, tc := range []struct{ header, body, want string }{
		{"ValidationException", `{"message":"secret-token"}`, "ValidationException"},
		{"", `{"__type":"aws#AccessDeniedException","message":"secret-token"}`, "AccessDeniedException"},
		{"", `{"message":"secret-token"}`, "unclassified"},
		{"arbitrary-secret", `not-json-secret`, "unclassified"},
	} {
		got := kiroObservationCode(tc.header, []byte(tc.body))
		if got != tc.want || strings.Contains(got, "secret") {
			t.Fatalf("unsafe diagnostic category: %q", got)
		}
	}
}

func TestKiroObservationReasonRedaction(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"message":"profileArn is required; secret-value"}`, "profilearn:required"},
		{`{"message":"Invalid origin secret-value"}`, "origin:invalid"},
		{`{"message":"secret-value"}`, "unclassified"},
		{`{"message":"Account secret-value is not supported"}`, "not_supported"},
		{`invalid-secret`, "unclassified"},
	} {
		got := kiroObservationReason([]byte(tc.body))
		if got != tc.want || strings.Contains(got, "secret") {
			t.Fatalf("unsafe reason: %q", got)
		}
	}
}
