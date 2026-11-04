package kiro

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testConfig() TransportConfig                                         { return TransportConfig{Endpoint: "ide", Region: "us-west-2"} }
func testAuth() Auth {
	return Auth{BearerToken: "secret-token", UserAgent: "caller-supplied-test-identity", AmzUserAgent: "caller-supplied-test-identity", InvocationID: "caller-invocation"}
}
func testRequest() Request {
	return Request{ConversationState: []byte(`{"conversationId":"test","currentMessage":{}}`)}
}
func newTestTransport(t *testing.T, rt http.RoundTripper, c TransportConfig) *Transport {
	t.Helper()
	tr, e := NewTransport(&http.Client{Transport: rt}, c)
	if e != nil {
		t.Fatal(e)
	}
	return tr
}

// Endpoint override exists exclusively in this _test.go compilation unit.
func serverTransport(t *testing.T, s *httptest.Server, c TransportConfig) *Transport {
	t.Helper()
	tr, e := NewTransport(s.Client(), c)
	if e != nil {
		t.Fatal(e)
	}
	tr.endpoint = s.URL
	return tr
}

func TestTransportPreflight(t *testing.T) {
	var calls int
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("secret-token") })
	for _, c := range []TransportConfig{{Endpoint: "https://evil.invalid", Region: "us-west-2"}, {Endpoint: "ide", Region: "evil.invalid"}, {Endpoint: "ide", Region: "us-west-2.amazonaws.com@evil.invalid"}} {
		if _, e := NewTransport(&http.Client{Transport: rt}, c); e != ErrEndpoint {
			t.Fatal(e)
		}
	}
	for _, client := range []*http.Client{nil, {}, {Transport: nil}} {
		if _, e := NewTransport(client, testConfig()); e != ErrConfig {
			t.Fatal(e)
		}
	}
	tr := newTestTransport(t, rt, testConfig())
	for _, tc := range []struct {
		auth Auth
		req  Request
		want error
	}{{Auth{BearerToken: "secret-token"}, testRequest(), ErrIdentity}, {testAuth(), Request{}, ErrRequest}, {Auth{BearerToken: "bad\r\nsecret-token"}, testRequest(), ErrAuth}, {testAuth(), Request{ConversationState: []byte(`null`)}, ErrRequest}} {
		r, e := tr.Execute(context.Background(), tc.req, tc.auth, nil)
		if e != tc.want || r.Phase != NotSent {
			t.Fatal(r, e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e := tr.Execute(ctx, testRequest(), testAuth(), nil)
	if e != ErrCanceled || r.Phase != NotSent {
		t.Fatal(r, e)
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}

func TestTransportSingleAttemptAndSafeHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{{401, ErrAuth}, {403, ErrForbidden}, {429, ErrThrottled}, {500, ErrServer}, {503, ErrServer}, {302, ErrHTTP}, {204, ErrHTTP}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			calls := 0
			tr := newTestTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://q.us-west-2.amazonaws.com/generateAssistantResponse" || r.GetBody != nil {
					t.Fatal("unexpected target or replay body")
				}
				if r.Header.Get("Authorization") != "Bearer secret-token" || r.Header.Get("amz-sdk-request") != "attempt=1; max=1" {
					t.Fatal("missing caller material")
				}
				b, _ := io.ReadAll(r.Body)
				if !bytes.Contains(b, []byte(`"conversationId":"test"`)) {
					t.Fatal("envelope changed")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Retry-After": []string{"12"}}, Body: io.NopCloser(strings.NewReader("secret-token prompt"))}, nil
			}), testConfig())
			r, e := tr.Execute(context.Background(), testRequest(), testAuth(), nil)
			if e != tc.want || r.Phase != ResponseStarted || r.RetryAfter != 12*time.Second || !r.RetryAfterPresent || calls != 1 || strings.Contains(e.Error(), "secret-token") {
				t.Fatal(r, e, calls)
			}
		})
	}
}
func TestTransportRedirectCannotLeak(t *testing.T) {
	var leak atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leak.Add(1) }))
	defer target.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		var calls atomic.Int32
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Location", target.URL)
			w.WriteHeader(status)
		}))
		client := origin.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { t.Error("caller redirect callback used"); return nil }
		tr, e := NewTransport(client, testConfig())
		if e != nil {
			t.Fatal(e)
		}
		tr.endpoint = origin.URL
		r, e := tr.Execute(context.Background(), testRequest(), testAuth(), nil)
		origin.Close()
		if e != ErrHTTP || r.StatusCode != status || calls.Load() != 1 || leak.Load() != 0 {
			t.Fatal(r, e, calls.Load(), leak.Load())
		}
		if client.CheckRedirect == nil {
			t.Fatal("caller mutated")
		}
	}
}
func TestTransportStreamsAndPartialFailures(t *testing.T) {
	text := eventFrame("event", "assistantResponseEvent", `{"content":"partial"}`)
	crc := append([]byte(nil), text...)
	crc[len(crc)-1] ^= 1
	for _, tc := range []struct {
		name   string
		wire   []byte
		want   error
		events int
	}{
		{"eof", text, ErrIncomplete, 1},
		{"empty", nil, ErrIncomplete, 0},
		{"crc", crc, ErrFrame, 0},
		{"partial-truncated", append(append([]byte(nil), text...), text[:len(text)-1]...), io.ErrUnexpectedEOF, 1},
		{"partial-exception", append(append([]byte(nil), text...), eventFrame("exception", "", `{"message":"secret-token"}`)...), ErrUpstream, 2},
		{"unknown", eventFrame("event", "future", `opaque`), ErrIncomplete, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			callbacks := 0
			tr := newTestTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(tc.wire))}, nil
			}), testConfig())
			r, e := tr.Execute(context.Background(), testRequest(), testAuth(), func(ev Event) error {
				callbacks++
				if len(ev.Raw()) == 0 {
					t.Fatal("raw event missing")
				}
				return nil
			})
			if e != tc.want || r.Phase != ResponseStarted || r.CompletionVerified || r.TransportEOF != (tc.want == ErrIncomplete) || r.Events != tc.events || callbacks != tc.events || calls != 1 {
				t.Fatal(r, e, callbacks, calls)
			}
		})
	}
}
func TestTransportBoundsAndCallback(t *testing.T) {
	wire := eventFrame("event", "assistantResponseEvent", `{"content":"text"}`)
	for _, tc := range []struct {
		name     string
		c        TransportConfig
		callback func(Event) error
		want     error
	}{
		{"request", TransportConfig{Endpoint: "ide", Region: "us-west-2", MaxRequestBytes: 2}, nil, ErrLimit},
		{"response", TransportConfig{Endpoint: "ide", Region: "us-west-2", MaxResponseBytes: 10}, nil, ErrLimit},
		{"payload", TransportConfig{Endpoint: "ide", Region: "us-west-2", MaxPayloadBytes: 1}, nil, ErrLimit},
		{"events", TransportConfig{Endpoint: "ide", Region: "us-west-2", MaxEvents: 1}, nil, ErrLimit},
		{"callback", testConfig(), func(Event) error { return errors.New("secret-token") }, ErrCallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTestTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(append(append([]byte(nil), wire...), wire...)))}, nil
			}), tc.c)
			_, e := tr.Execute(context.Background(), testRequest(), testAuth(), tc.callback)
			if e != tc.want {
				t.Fatal(e)
			}
		})
	}
}
func TestTransportCancellationTimeoutAndUnknown(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		entered := make(chan struct{})
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			close(entered)
			<-r.Context().Done()
		}))
		c := testConfig()
		if timeout {
			c.Timeout = 30 * time.Millisecond
		}
		tr := serverTransport(t, s, c)
		ctx, cancel := context.WithCancel(context.Background())
		if !timeout {
			go func() { <-entered; cancel() }()
		}
		r, e := tr.Execute(ctx, testRequest(), testAuth(), nil)
		cancel()
		s.Close()
		want := ErrCanceled
		if timeout {
			want = ErrTimeout
		}
		if e != want || r.Phase != OutcomeUnknown {
			t.Fatal(r, e)
		}
	}
	tr := newTestTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret-token prompt") }), testConfig())
	r, e := tr.Execute(context.Background(), testRequest(), testAuth(), nil)
	if e != ErrTransport || r.Phase != OutcomeUnknown {
		t.Fatal(r, e)
	}
}
func TestTransportCancelAfterResponse(t *testing.T) {
	entered := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
		_, _ = w.Write(eventFrame("event", "assistantResponseEvent", `{"content":"partial"}`))
		w.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
	}))
	defer s.Close()
	tr := serverTransport(t, s, testConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, e := tr.Execute(ctx, testRequest(), testAuth(), func(Event) error { cancel(); return nil })
	if e != ErrCanceled || r.Phase != ResponseStarted || r.TransportEOF || r.CompletionVerified {
		t.Fatal(r, e)
	}
}
func TestRetryAfterSanitization(t *testing.T) {
	for _, s := range []string{"secret-token", "-1", "999999999999999999999999999", "9223372037"} {
		if _, ok := retryAfter(s); ok {
			t.Fatal(s)
		}
	}
	if _, ok := retryAfter(time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)); !ok {
		t.Fatal("date")
	}
}

type timeoutBody struct{}

func (timeoutBody) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }
func (timeoutBody) Close() error             { return nil }
func TestTransportBodyTimeoutAndAuthRedaction(t *testing.T) {
	tr := newTestTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: timeoutBody{}}, nil
	}), testConfig())
	r, e := tr.Execute(context.Background(), testRequest(), testAuth(), nil)
	if e != ErrTimeout || r.Phase != ResponseStarted {
		t.Fatal(r, e)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if strings.Contains(fmt.Sprintf(format, testAuth()), "secret-token") {
			t.Fatal("auth formatting leak")
		}
	}
}

func TestTransportToolFragmentsRemainUnverified(t *testing.T) {
	for _, end := range []string{"clean", "truncated", "exception", "cancel"} {
		t.Run(end, func(t *testing.T) {
			wire := eventFrame("event", "assistantResponseEvent", `{"content":"before"}`)
			for _, p := range []string{`{"name":"lookup","toolUseId":"id","input":"{"}`, `{"input":"}"}`, `{"stop":true}`} {
				wire = append(wire, eventFrame("event", "toolUseEvent", p)...)
			}
			switch end {
			case "truncated":
				wire = append(wire, 1, 2)
			case "exception":
				wire = append(wire, eventFrame("exception", "", `{"message":"private"}`)...)
			}
			tr := newTestTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(wire))}, nil
			}), testConfig())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var state Attempt
			var deltas []Delta
			r, err := tr.Execute(ctx, testRequest(), testAuth(), func(e Event) error {
				d, applyErr := state.Apply(e)
				if applyErr == nil {
					deltas = append(deltas, d)
				}
				if end == "cancel" && len(deltas) == 4 {
					cancel()
				}
				return nil
			})
			want := ErrIncomplete
			switch end {
			case "truncated":
				want = io.ErrUnexpectedEOF
			case "exception":
				want = ErrUpstream
			case "cancel":
				want = ErrCanceled
			}
			if err != want || r.CompletionVerified || r.TransportEOF != (end == "clean") || len(deltas) != 4 || deltas[0].Text != "before" || deltas[1].ToolInput != "{" || deltas[2].ToolInput != "}" || !deltas[3].ToolStop {
				t.Fatal(r, err, deltas)
			}
		})
	}
}
