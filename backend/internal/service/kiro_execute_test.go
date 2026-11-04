package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

type kiroJournalFake struct {
	UsageAttemptJournal
	created  bool
	sent     bool
	failSend bool
	facts    []UsageFactInput
}

func (j *kiroJournalFake) Create(context.Context, UsageAttemptInput) error {
	j.created = true
	return nil
}
func (j *kiroJournalFake) Claim(_ context.Context, id, owner string, _ time.Duration) (*UsageAttemptFence, error) {
	return &UsageAttemptFence{AttemptID: id, Owner: owner, Generation: 1}, nil
}
func (j *kiroJournalFake) MarkSendIntent(context.Context, UsageAttemptFence) error {
	if j.failSend || j.sent {
		return ErrUsageJournalFence
	}
	j.sent = true
	return nil
}
func (j *kiroJournalFake) Observe(_ context.Context, _ UsageAttemptFence, f UsageFactInput) (*UsageFactRevision, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	j.facts = append(j.facts, f)
	return &UsageFactRevision{UsageFactInput: f}, nil
}
func kiroWireEvent(event, payload string) []byte {
	header := func(name, value string) []byte {
		b := append([]byte{byte(len(name))}, []byte(name)...)
		b = append(b, 7)
		b = binary.BigEndian.AppendUint16(b, uint16(len(value)))
		return append(b, []byte(value)...)
	}
	h := append(header(":message-type", "event"), header(":event-type", event)...)
	b := binary.BigEndian.AppendUint32(nil, uint32(16+len(h)+len(payload)))
	b = binary.BigEndian.AppendUint32(b, uint32(len(h)))
	b = binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
	b = append(b, h...)
	b = append(b, []byte(payload)...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
}
func TestKiroPrivateMessagesJournalPartialUnknownAndReplayGuard(t *testing.T) {
	journal := &kiroJournalFake{}
	calls := 0
	s, repo := kiroRefreshSetup(t, func(*http.Request) (*http.Response, error) {
		calls++
		require.True(t, journal.sent)
		wire := kiroWireEvent("assistantResponseEvent", `{"content":"fake-visible-text"}`)
		wire = append(wire, kiroWireEvent("meteringEvent", `{"usage":0.25,"unit":"credit","unitPlural":"credits"}`)...)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(wire))}, nil
	})
	// Start with an imported active token to isolate generation execution.
	secret := kiroTestSecret(time.Now(), KiroCredentialIdC)
	secret.SSOAccessToken = "fake-cli-access"
	secret.SSOExpiresAt = time.Now().Add(time.Hour).Unix()
	env, _, err := s.vault.Import(context.Background(), repo.r.Binding, kiroTestJSON(t, secret), time.Now())
	require.NoError(t, err)
	repo.r.Envelope = env
	s.journal = journal
	var output bytes.Buffer
	body := []byte(`{"model":"explicit-model","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"fake-prompt"}]}`)
	result, err := s.ExecutePrivateMessages(context.Background(), 7, kiroDigest([]byte("operation")), body, &output)
	require.ErrorIs(t, err, kiro.ErrIncomplete)
	require.False(t, result.CompletionVerified)
	require.Equal(t, 1, calls)
	require.Contains(t, output.String(), "text_delta")
	require.Contains(t, output.String(), "kiro_completion_unverified")
	require.NotContains(t, output.String(), "input_tokens")
	require.NotContains(t, output.String(), "message_stop")
	require.Len(t, journal.facts, 2)
	require.Equal(t, "provider_reported", journal.facts[0].Source)
	require.Equal(t, 0.25, *journal.facts[0].Metrics[0].Value)
	require.Equal(t, "credits", journal.facts[0].Metrics[0].UnitPlural)
	require.Equal(t, "usage_unknown", journal.facts[1].Outcome)
	require.True(t, journal.facts[1].Partial)
	require.Nil(t, journal.facts[1].Metrics)
	_, err = s.ExecutePrivateMessages(context.Background(), 7, kiroDigest([]byte("operation")), body, &output)
	require.ErrorIs(t, err, ErrUsageJournalFence)
	require.Equal(t, 1, calls)
}
func TestKiroPrivateDoesNotSendWithoutDurableIntent(t *testing.T) {
	s, repo := kiroRefreshSetup(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("must not send")
		return nil, errors.New("blocked")
	})
	secret := kiroTestSecret(time.Now(), KiroCredentialIdC)
	secret.SSOAccessToken = "fake-cli-access"
	secret.SSOExpiresAt = time.Now().Add(time.Hour).Unix()
	env, _, err := s.vault.Import(context.Background(), repo.r.Binding, kiroTestJSON(t, secret), time.Now())
	require.NoError(t, err)
	repo.r.Envelope = env
	s.journal = &kiroJournalFake{failSend: true}
	_, err = s.ExecutePrivate(context.Background(), 7, kiroDigest([]byte("op")), kiro.Request{ConversationState: []byte(`{}`)}, func(kiro.Event) error { return nil })
	require.ErrorIs(t, err, ErrUsageJournalFence)
}

func TestKiroPrivateMessagesResolvedToolsAndEOF(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "truncated"}[truncated], func(t *testing.T) {
			s, repo := kiroRefreshSetup(t, func(*http.Request) (*http.Response, error) {
				wire := kiroWireEvent("toolUseEvent", `{"toolUseId":"tool-1","name":"lookup","input":"{\"q\":"}`)
				wire = append(wire, kiroWireEvent("toolUseEvent", `{"input":"\"x\"}"}`)...)
				wire = append(wire, kiroWireEvent("toolUseEvent", `{"stop":true}`)...)
				if truncated {
					wire = wire[:len(wire)-2]
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(wire))}, nil
			})
			secret := kiroTestSecret(time.Now(), KiroCredentialIdC)
			secret.SSOAccessToken = "fake-cli-access"
			secret.SSOExpiresAt = time.Now().Add(time.Hour).Unix()
			env, _, err := s.vault.Import(context.Background(), repo.r.Binding, kiroTestJSON(t, secret), time.Now())
			require.NoError(t, err)
			repo.r.Envelope = env
			s.journal = &kiroJournalFake{}
			var output bytes.Buffer
			body := []byte(`{"model":"explicit-model","max_tokens":32,"stream":true,"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}],"messages":[{"role":"user","content":"synthetic"}]}`)
			result, err := s.ExecutePrivateMessages(context.Background(), 7, kiroDigest([]byte("tool-op")), body, &output)
			require.Error(t, err)
			require.False(t, result.CompletionVerified)
			require.Equal(t, !truncated, result.TransportEOF)
			require.Contains(t, output.String(), `"name":"lookup"`)
			require.Contains(t, output.String(), "input_json_delta")
			require.NotContains(t, output.String(), "message_stop")
			require.Equal(t, !truncated, bytes.Contains(output.Bytes(), []byte("content_block_stop")))
		})
	}
}
