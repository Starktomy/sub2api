//go:build unit

package service

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type sharedOAuthClientStub struct {
	openaiOAuthClientStateStub
	fail bool
}

func (s *sharedOAuthClientStub) ExchangeCode(ctx context.Context, code, verifier, redirectURI, proxyURL, clientID string) (*openai.TokenResponse, error) {
	if s.fail {
		atomic.AddInt32(&s.exchangeCalled, 1)
		return nil, errors.New("synthetic upstream failure")
	}
	return s.openaiOAuthClientStateStub.ExchangeCode(ctx, code, verifier, redirectURI, proxyURL, clientID)
}

func TestOpenAIOAuthSharedSessionCallbackAcrossServices(t *testing.T) {
	mr := miniredis.RunT(t)
	client := &sharedOAuthClientStub{}
	newService := func() *OpenAIOAuthService {
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1, DialTimeout: time.Second})
		t.Cleanup(func() { _ = rdb.Close() })
		svc := NewManagedOpenAIOAuthService(nil, client)
		svc.sessionStore = openai.NewManagedRedisSessionStore(rdb)
		t.Cleanup(svc.Stop)
		return svc
	}
	stateFromAuthURL := func(raw string) string {
		parsed, err := url.Parse(raw)
		require.NoError(t, err)
		return parsed.Query().Get("state")
	}
	ctx := context.Background()
	a, b := newService(), newService()
	flow, err := a.GenerateAuthURL(ctx, nil, "", PlatformOpenAI)
	require.NoError(t, err)
	a.Stop()
	restarted := newService()
	_, err = b.ExchangeCode(ctx, &OpenAIExchangeCodeInput{SessionID: flow.SessionID, Code: "synthetic-code", State: "wrong"})
	require.Error(t, err)
	require.Zero(t, atomic.LoadInt32(&client.exchangeCalled))
	var wg sync.WaitGroup
	var winners atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc := b
			if i%2 == 0 {
				svc = restarted
			}
			_, err := svc.ExchangeCode(ctx, &OpenAIExchangeCodeInput{SessionID: flow.SessionID, Code: "synthetic-code", State: stateFromAuthURL(flow.AuthURL)})
			if err == nil {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())
	require.EqualValues(t, 1, atomic.LoadInt32(&client.exchangeCalled))

	flow, err = restarted.GenerateAuthURL(ctx, nil, "", PlatformOpenAI)
	require.NoError(t, err)
	client.fail = true
	input := &OpenAIExchangeCodeInput{SessionID: flow.SessionID, Code: "synthetic-code", State: stateFromAuthURL(flow.AuthURL)}
	_, err = b.ExchangeCode(ctx, input)
	require.Error(t, err)
	_, err = restarted.ExchangeCode(ctx, input)
	require.Error(t, err)
	require.EqualValues(t, 2, atomic.LoadInt32(&client.exchangeCalled), "failed exchange must remain consumed")

	flow, err = restarted.GenerateAuthURL(ctx, nil, "", PlatformOpenAI)
	require.NoError(t, err)
	mr.FastForward(openai.SessionTTL + time.Second)
	_, err = b.ExchangeCode(ctx, &OpenAIExchangeCodeInput{SessionID: flow.SessionID, Code: "synthetic-code", State: stateFromAuthURL(flow.AuthURL)})
	require.Error(t, err)
	require.EqualValues(t, 2, atomic.LoadInt32(&client.exchangeCalled))

	flow, err = restarted.GenerateAuthURL(ctx, nil, "", PlatformOpenAI)
	require.NoError(t, err)
	mr.Close()
	_, err = b.ExchangeCode(ctx, &OpenAIExchangeCodeInput{SessionID: flow.SessionID, Code: "synthetic-code", State: stateFromAuthURL(flow.AuthURL)})
	require.Error(t, err)
	_, err = restarted.GenerateAuthURL(ctx, nil, "", PlatformOpenAI)
	require.Error(t, err)
	require.EqualValues(t, 2, atomic.LoadInt32(&client.exchangeCalled))
}
