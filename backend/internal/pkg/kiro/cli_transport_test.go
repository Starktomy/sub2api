package kiro

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTransportCLIProtocol(t *testing.T) {
	calls := 0
	tr := newTestTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://runtime.us-east-1.kiro.dev/" || r.Method != "POST" {
			t.Fatal("wrong CLI endpoint")
		}
		if r.Header.Get("X-Amz-Target") != "AmazonCodeWhispererStreamingService.GenerateAssistantResponse" || r.Header.Get("Content-Type") != "application/x-amz-json-1.0" {
			t.Fatal("wrong CLI RPC")
		}
		if r.Header.Get("amz-sdk-request") != "attempt=1; max=1" || r.Header.Get("x-amzn-kiro-agent-mode") != "" || r.GetBody != nil {
			t.Fatal("retry or IDE headers leaked")
		}
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(""))}, nil
	}), TransportConfig{Endpoint: "cli", Region: "us-east-1"})
	_, err := tr.Execute(context.Background(), testRequest(), testAuth(), func(Event) error { return nil })
	if err != ErrAuth || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if _, err := NewTransport(&http.Client{Transport: roundTripFunc(nil)}, TransportConfig{Endpoint: "cli", Region: "us-west-2"}); err != ErrEndpoint {
		t.Fatal("unverified CLI region accepted")
	}
}
