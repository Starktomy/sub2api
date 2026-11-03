package credentialcrypto

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type keyRecord struct {
	key     []byte
	keyID   string
	context map[string]string
}
type fakeKMS struct {
	records                     map[string]keyRecord
	generateCalls, decryptCalls int
	fail                        error
	cancel                      context.CancelFunc
	badKey                      bool
	ignoreContext               bool
	lastKey                     []byte
}

func (f *fakeKMS) GenerateDataKey(ctx context.Context, id string, ec map[string]string) ([]byte, []byte, error) {
	f.generateCalls++
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	if f.badKey {
		key = key[:31]
	}
	f.lastKey = key
	wrapped := []byte(fmt.Sprintf("fake-wrapped-%d", f.generateCalls))
	if f.records == nil {
		f.records = make(map[string]keyRecord)
	}
	f.records[string(wrapped)] = keyRecord{bytes.Clone(key), id, ec}
	if f.cancel != nil {
		f.cancel()
	}
	return key, wrapped, f.fail
}
func (f *fakeKMS) Decrypt(ctx context.Context, id string, wrapped []byte, ec map[string]string) ([]byte, error) {
	f.decryptCalls++
	r, ok := f.records[string(wrapped)]
	if !ok || id != r.keyID || (!f.ignoreContext && !reflect.DeepEqual(ec, r.context)) {
		return nil, errors.New("secret adapter detail")
	}
	key := bytes.Clone(r.key)
	if f.badKey {
		key = key[:31]
	}
	f.lastKey = key
	if f.cancel != nil {
		f.cancel()
	}
	return key, f.fail
}
func setup(t *testing.T) (*Codec, *fakeKMS) {
	t.Helper()
	f := &fakeKMS{}
	c, err := New(f, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	return c, f
}
func encryptTest(t *testing.T, c *Codec) []byte {
	t.Helper()
	data, err := c.Encrypt(context.Background(), "account-1", map[string]any{"access_token": "test-only-token", "nested": map[string]any{"refresh": "test-only-refresh"}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func assertWiped(t *testing.T, key []byte) {
	t.Helper()
	if len(key) == 0 || !bytes.Equal(key, make([]byte, len(key))) {
		t.Fatal("plaintext key not wiped")
	}
}
func TestRoundTrip(t *testing.T) {
	c, f := setup(t)
	input := map[string]any{"token": "test-only-token", "nested": map[string]any{"refresh": "test-only-refresh"}, "array": []any{true, nil, float64(42)}, "empty": ""}
	a, err := c.Encrypt(context.Background(), "account-1", input)
	if err != nil {
		t.Fatal(err)
	}
	assertWiped(t, f.lastKey)
	b, err := c.Encrypt(context.Background(), "account-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) || bytes.Equal(f.records["fake-wrapped-1"].key, f.records["fake-wrapped-2"].key) {
		t.Fatal("expected fresh data key and envelope")
	}
	if bytes.Contains(a, []byte("test-only")) || bytes.Contains(a, []byte("refresh")) {
		t.Fatal("plaintext present in envelope")
	}
	got, err := c.Decrypt(context.Background(), "account-1", a)
	if err != nil || !reflect.DeepEqual(got, input) {
		t.Fatalf("roundtrip failed: %v", err)
	}
	assertWiped(t, f.lastKey)
	ec := f.records["fake-wrapped-1"].context
	if !reflect.DeepEqual(ec, map[string]string{"purpose": "sub2api/account-credentials", "version": "1", "account_id": "account-1"}) {
		t.Fatal("incorrect context")
	}
	if _, err := c.Encrypt(context.Background(), "account-1", map[string]any{}); err != nil {
		t.Fatal(err)
	}
}
func TestTamperAndWrongIdentity(t *testing.T) {
	for _, field := range []string{"ciphertext", "nonce", "encrypted_data_key"} {
		t.Run(field, func(t *testing.T) {
			c, f := setup(t)
			data := encryptTest(t, c)
			var e envelope
			if err := json.Unmarshal(data, &e); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "ciphertext":
				e.Ciphertext[0] ^= 1
			case "nonce":
				e.Nonce[0] ^= 1
			case "encrypted_data_key":
				old := f.records[string(e.EncryptedKey)]
				e.EncryptedKey[0] ^= 1
				// Simulate another valid wrapper for the same DEK: AAD must reject.
				f.records[string(e.EncryptedKey)] = old
			}
			data, _ = json.Marshal(e)
			got, err := c.Decrypt(context.Background(), "account-1", data)
			if got != nil || !errors.Is(err, ErrDecrypt) {
				t.Fatalf("tamper accepted: %v", err)
			}
			assertWiped(t, f.lastKey)
		})
	}
	c, f := setup(t)
	data := encryptTest(t, c)
	if got, err := c.Decrypt(context.Background(), "account-2", data); got != nil || !errors.Is(err, ErrKMS) {
		t.Fatal("KMS identity binding failed")
	}
	f.ignoreContext = true
	if got, err := c.Decrypt(context.Background(), "account-2", data); got != nil || !errors.Is(err, ErrDecrypt) {
		t.Fatal("AAD identity binding failed")
	}
	other, _ := New(f, "other-key")
	if got, err := other.Decrypt(context.Background(), "account-1", data); got != nil || err == nil {
		t.Fatal("wrong key accepted")
	}
}
func TestKMSFailuresAndCancellation(t *testing.T) {
	for _, decrypt := range []bool{false, true} {
		for _, mode := range []string{"failure", "cancel-in-flight", "pre-cancel", "deadline", "bad-key"} {
			t.Run(fmt.Sprintf("decrypt=%t/%s", decrypt, mode), func(t *testing.T) {
				c, f := setup(t)
				data := encryptTest(t, c)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				want := ErrKMS
				switch mode {
				case "failure":
					f.fail = errors.New("test-only-secret")
				case "cancel-in-flight":
					f.cancel = cancel
					want = context.Canceled
				case "pre-cancel":
					cancel()
					want = context.Canceled
				case "deadline":
					f.fail = fmt.Errorf("test-only-secret: %w", context.DeadlineExceeded)
					want = context.DeadlineExceeded
				case "bad-key":
					f.badKey = true
				}
				before := f.generateCalls + f.decryptCalls
				var err error
				if decrypt {
					var result map[string]any
					result, err = c.Decrypt(ctx, "account-1", data)
					if result != nil {
						t.Fatal("returned credentials on failure")
					}
				} else {
					var result []byte
					result, err = c.Encrypt(ctx, "account-1", map[string]any{"token": "test-only-secret"})
					if result != nil {
						t.Fatal("returned envelope on failure")
					}
				}
				if !errors.Is(err, want) || strings.Contains(err.Error(), "test-only-secret") {
					t.Fatalf("unexpected error: %v", err)
				}
				if mode == "pre-cancel" && before != f.generateCalls+f.decryptCalls {
					t.Fatal("KMS called despite cancellation")
				}
				assertWiped(t, f.lastKey)
			})
		}
	}
}
func TestRejectMalformedBeforeKMS(t *testing.T) {
	c, f := setup(t)
	valid := encryptTest(t, c)
	var base map[string]any
	if err := json.Unmarshal(valid, &base); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"empty": nil, "plaintext": []byte(`{"token":"test-only"}`), "null": []byte(`null`), "array": []byte(`[]`),
		"truncated": valid[:len(valid)-1], "trailing": append(bytes.Clone(valid), []byte(` {}`)...),
		"duplicate": bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		"oversized": bytes.Repeat([]byte(" "), MaxEnvelopeBytes+1),
	}
	for name, value := range map[string]any{"version": 2, "algorithm": "AES-128-GCM", "nonce": "bad-base64!", "ciphertext": "", "encrypted_data_key": ""} {
		copyMap := make(map[string]any)
		for k, v := range base {
			copyMap[k] = v
		}
		copyMap[name] = value
		cases[name], _ = json.Marshal(copyMap)
	}
	for _, field := range []string{"version", "algorithm", "encrypted_data_key", "nonce", "ciphertext"} {
		cases["null-"+field] = bytes.Replace(valid, mustJSONPair(t, field, base[field]), []byte(fmt.Sprintf(`%q:null`, field)), 1)
	}
	cases["unknown"] = bytes.Replace(valid, []byte(`"version":1`), []byte(`"extra":1,"version":1`), 1)
	cases["case-alias"] = bytes.Replace(valid, []byte(`"version":1`), []byte(`"Version":1`), 1)
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			before := f.decryptCalls
			got, err := c.Decrypt(context.Background(), "account-1", data)
			if got != nil || err == nil || before != f.decryptCalls {
				t.Fatal("invalid envelope accepted or reached KMS")
			}
			if name == "version" && !errors.Is(err, ErrVersion) {
				t.Fatal(err)
			}
			if name == "oversized" && !errors.Is(err, ErrTooLarge) {
				t.Fatal(err)
			}
		})
	}
}
func mustJSONPair(t *testing.T, name string, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(fmt.Sprintf(`%q:%s`, name, encoded))
}
func TestInvalidInputs(t *testing.T) {
	if _, err := New(nil, "key"); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	for _, id := range []string{"", " key", strings.Repeat("x", maxKeyIDBytes+1), string([]byte{255})} {
		if _, err := New(&fakeKMS{}, id); !errors.Is(err, ErrConfiguration) {
			t.Fatal("invalid key ID accepted")
		}
	}
	c, f := setup(t)
	for _, id := range []string{"", " account", strings.Repeat("x", MaxIdentityBytes+1), string([]byte{255})} {
		if _, err := c.Encrypt(context.Background(), id, map[string]any{}); !errors.Is(err, ErrIdentity) {
			t.Fatal("invalid identity accepted")
		}
		if _, err := c.Decrypt(context.Background(), id, nil); !errors.Is(err, ErrIdentity) {
			t.Fatal("invalid identity accepted")
		}
	}
	for _, input := range []map[string]any{nil, {"bad": make(chan int)}, {"secret": strings.Repeat("x", MaxPlaintextBytes+1)}} {
		if got, err := c.Encrypt(context.Background(), "account-1", input); got != nil || err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := c.Encrypt(context.Background(), "account-1", cycle); !errors.Is(err, ErrCredentials) {
		t.Fatal(err)
	}
	if f.generateCalls != 0 || f.decryptCalls != 0 {
		t.Fatal("invalid input reached KMS")
	}
}
func TestMaximumDocument(t *testing.T) {
	c, _ := setup(t)
	input := map[string]any{"x": strings.Repeat("a", MaxPlaintextBytes-len(`{"x":""}`))}
	data, err := c.Encrypt(context.Background(), "account-1", input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decrypt(context.Background(), "account-1", data)
	if err != nil || !reflect.DeepEqual(input, got) {
		t.Fatalf("maximum document failed: %v", err)
	}
}
