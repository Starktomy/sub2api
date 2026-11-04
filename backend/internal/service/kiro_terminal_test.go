package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKiroMessagesVerifiedEndTurnWithoutInventedUsage(t *testing.T) {
	s, repo := kiroRefreshSetup(t, func(*http.Request) (*http.Response, error) {
		wire := kiroWireEvent("assistantResponseEvent", `{"content":"OK"}`)
		wire = append(wire, kiroWireEvent("metadataEvent", `{"stopReason":"END_TURN"}`)...)
		wire = append(wire, kiroWireEvent("meteringEvent", `{"usage":0.01,"unit":"credit","unitPlural":"credits"}`)...)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(wire))}, nil
	})
	now := time.Now()
	secret := kiroTestSecret(now, KiroCredentialIdC)
	secret.SSOAccessToken = "fake-cli-access"
	secret.SSOExpiresAt = now.Add(time.Hour).Unix()
	var err error
	repo.r.Envelope, _, err = s.vault.Import(context.Background(), repo.r.Binding, kiroTestJSON(t, secret), now)
	require.NoError(t, err)
	j := &kiroJournalFake{}
	s.journal = j
	var out bytes.Buffer
	result, err := s.ExecutePrivateMessages(context.Background(), 7, kiroDigest([]byte("verified-end-turn")), []byte(`{"model":"m","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"x"}]}`), &out)
	require.NoError(t, err)
	require.True(t, result.CompletionVerified)
	require.Equal(t, "end_turn", result.StopReason)
	text := out.String()
	for _, event := range []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"} {
		require.Contains(t, text, "event: "+event)
	}
	require.NotContains(t, text, "event: error")
	require.NotContains(t, text, "input_tokens")
	require.NotContains(t, text, "output_tokens")
	require.Contains(t, text, `"stop_reason":"end_turn"`)
	require.Equal(t, "usage_unknown", j.facts[len(j.facts)-1].Outcome)
}
