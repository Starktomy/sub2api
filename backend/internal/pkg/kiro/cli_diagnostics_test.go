package kiro

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCLIDiagnosticRedaction(t *testing.T) {
	for _, tc := range []struct{ body, code, field string }{
		{`{"__type":"ValidationException","message":"additionalModelRequestFields rejected secret-canary"}`, "ValidationException", "additionalmodelrequestfields"},
		{`{"__type":"secret-canary","message":"secret-canary"}`, "", ""},
	} {
		tr := newTestTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		}), TransportConfig{Endpoint: "cli", Region: "us-east-1"})
		r, err := tr.Execute(context.Background(), testRequest(), testAuth(), func(Event) error { return nil })
		if err != ErrHTTP || r.ErrorCode != tc.code || r.ErrorField != tc.field {
			t.Fatal("unexpected diagnostic")
		}
		if strings.Contains(r.ErrorCode+r.ErrorField, "secret") {
			t.Fatal("diagnostic leak")
		}
	}
}
