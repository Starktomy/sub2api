package kiro

import "testing"

func TestCLIMetadataAccessor(t *testing.T) {
	e := Event{messageType: "event", eventType: "metadataEvent", raw: []byte(`{"stopReason":"unverified-provider-value"}`)}
	m, ok := e.Metadata()
	if !ok || m.StopReason != "unverified-provider-value" {
		t.Fatal("metadata lost")
	}
	m.StopReason = "changed"
	again, _ := e.Metadata()
	if again.StopReason != "unverified-provider-value" {
		t.Fatal("metadata aliased")
	}
	if _, ok := (Event{messageType: "event", eventType: "unknown"}).Metadata(); ok {
		t.Fatal("unknown interpreted as metadata")
	}
}
