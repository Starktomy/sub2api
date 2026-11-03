package refreshpolicy

import (
	"context"
	"testing"
)

func TestSingleAttemptIsExplicitAndPreservesCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	marked := WithSingleAttempt(parent)
	if SingleAttempt(parent) || !SingleAttempt(marked) {
		t.Fatal("policy must be explicit and local to derived context")
	}
	cancel()
	if marked.Err() != context.Canceled {
		t.Fatal("policy detached parent cancellation")
	}
}
