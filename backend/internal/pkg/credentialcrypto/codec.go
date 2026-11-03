// Package credentialcrypto provides opt-in, whole-document envelope encryption.
// It has no runtime registration, storage integration, network client, or plaintext
// fallback. Callers must supply a trusted account identity, not one read from an
// untrusted envelope. Account identities and encryption context are not secrets:
// a future KMS adapter may expose context in audit logs.
package credentialcrypto

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	Version              = 1
	Algorithm            = "AES-256-GCM"
	MaxPlaintextBytes    = 1 << 20
	MaxEnvelopeBytes     = 2 << 20
	MaxEncryptedKeyBytes = 16 << 10
	MaxIdentityBytes     = 256
	maxKeyIDBytes        = 2048
)

var (
	ErrConfiguration = errors.New("credentialcrypto: invalid configuration")
	ErrIdentity      = errors.New("credentialcrypto: invalid account identity")
	ErrCredentials   = errors.New("credentialcrypto: invalid credentials document")
	ErrTooLarge      = errors.New("credentialcrypto: size limit exceeded")
	ErrEnvelope      = errors.New("credentialcrypto: invalid envelope")
	ErrVersion       = errors.New("credentialcrypto: unsupported envelope version")
	ErrKMS           = errors.New("credentialcrypto: key operation failed")
	ErrEncrypt       = errors.New("credentialcrypto: encryption failed")
	ErrDecrypt       = errors.New("credentialcrypto: decryption failed")
)

// KMS generates a fresh, cryptographically random 32-byte plaintext data key and
// its wrapped representation for every GenerateDataKey call. Decrypt must enforce
// both keyID and an exact encryption-context match. It must honor ctx and must not
// log keys or credentials. Returned plaintext slices transfer ownership to the
// codec, which overwrites them even when an operation returns an error. Adapters
// must not retain or reuse those slices. Implementations must be concurrency-safe
// if a Codec is shared. No AWS SDK dependency or implementation is supplied here.
type KMS interface {
	GenerateDataKey(ctx context.Context, keyID string, encryptionContext map[string]string) (plaintext, encrypted []byte, err error)
	Decrypt(ctx context.Context, keyID string, encrypted []byte, encryptionContext map[string]string) (plaintext []byte, err error)
}

// Codec is immutable; construction alone does not enable encryption anywhere.
type Codec struct {
	kms   KMS
	keyID string
}

func New(kms KMS, keyID string) (*Codec, error) {
	if kms == nil || len(keyID) == 0 || len(keyID) > maxKeyIDBytes || !utf8.ValidString(keyID) || strings.TrimSpace(keyID) != keyID {
		return nil, ErrConfiguration
	}
	return &Codec{kms: kms, keyID: keyID}, nil
}

// envelope uses standard padded base64 for byte slices through encoding/json.
// Neither account identity nor plaintext credentials are stored in the envelope.
type envelope struct {
	Version      int    `json:"version"`
	Algorithm    string `json:"algorithm"`
	EncryptedKey []byte `json:"encrypted_data_key"`
	Nonce        []byte `json:"nonce"`
	Ciphertext   []byte `json:"ciphertext"`
}

func binding(accountID string) (map[string]string, error) {
	if len(accountID) == 0 || len(accountID) > MaxIdentityBytes || !utf8.ValidString(accountID) || strings.TrimSpace(accountID) != accountID {
		return nil, ErrIdentity
	}
	return map[string]string{
		"purpose":    "sub2api/account-credentials",
		"version":    "1",
		"account_id": accountID,
	}, nil
}

func (c *Codec) aad(accountID string, encryptedKey []byte) []byte {
	// Struct encoding avoids delimiter ambiguity. Authenticate the wrapped key
	// too, even if a KMS implementation accepts multiple wrappers of one DEK.
	data, _ := json.Marshal(struct {
		Purpose      string
		Version      int
		Algorithm    string
		AccountID    string
		KeyID        string
		EncryptedKey []byte
	}{"sub2api/account-credentials", Version, Algorithm, accountID, c.keyID, encryptedKey})
	return data
}

// Encrypt encrypts the complete JSON object, including nested and unknown fields.
// Standard encoding/json semantics apply (including float64 numbers on decode).
// Nil maps are rejected rather than silently encoding a null document. Callers
// must not mutate credentials concurrently or supply untrusted MarshalJSON code.
func (c *Codec) Encrypt(ctx context.Context, accountID string, credentials map[string]any) ([]byte, error) {
	ec, err := binding(accountID)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if credentials == nil {
		return nil, ErrCredentials
	}
	plaintext, err := json.Marshal(credentials)
	defer wipe(plaintext)
	if err != nil {
		return nil, ErrCredentials
	}
	if len(plaintext) > MaxPlaintextBytes {
		return nil, ErrTooLarge
	}
	key, wrapped, err := c.kms.GenerateDataKey(ctx, c.keyID, ec)
	defer wipe(key)
	if err != nil {
		return nil, keyError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(key) != 32 || len(wrapped) == 0 || len(wrapped) > MaxEncryptedKeyBytes {
		return nil, ErrKMS
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, ErrEncrypt
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrEncrypt
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, c.aad(accountID, wrapped))
	result, err := json.Marshal(envelope{Version, Algorithm, wrapped, nonce, ciphertext})
	if err != nil {
		return nil, ErrEncrypt
	}
	if len(result) > MaxEnvelopeBytes {
		return nil, ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Decrypt accepts only a valid versioned encrypted envelope. Every failure returns
// a nil document; it never attempts to interpret input as plaintext credentials.
func (c *Codec) Decrypt(ctx context.Context, accountID string, data []byte) (map[string]any, error) {
	ec, err := binding(accountID)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e, err := parseEnvelope(data)
	if err != nil {
		return nil, err
	}
	key, err := c.kms.Decrypt(ctx, c.keyID, e.EncryptedKey, ec)
	defer wipe(key)
	if err != nil {
		return nil, keyError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, ErrKMS
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, ErrDecrypt
	}
	plaintext, err := gcm.Open(nil, e.Nonce, e.Ciphertext, c.aad(accountID, e.EncryptedKey))
	defer wipe(plaintext)
	if err != nil {
		return nil, ErrDecrypt
	}
	var credentials map[string]any
	if err := json.Unmarshal(plaintext, &credentials); err != nil || credentials == nil {
		return nil, ErrCredentials
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return credentials, nil
}

func parseEnvelope(data []byte) (envelope, error) {
	var e envelope
	if len(data) > MaxEnvelopeBytes {
		return e, ErrTooLarge
	}
	// Tokenize the object to reject duplicates, case aliases, unknown fields,
	// missing fields and trailing JSON before any KMS call or base64 decoding.
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return e, ErrEnvelope
	}
	fields := make(map[string]json.RawMessage, 5)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return e, ErrEnvelope
		}
		name, ok := token.(string)
		if !ok {
			return e, ErrEnvelope
		}
		switch name {
		case "version", "algorithm", "encrypted_data_key", "nonce", "ciphertext":
		default:
			return e, ErrEnvelope
		}
		if _, exists := fields[name]; exists {
			return e, ErrEnvelope
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(value, []byte("null")) {
			return e, ErrEnvelope
		}
		fields[name] = value
	}
	if _, err := d.Token(); err != nil {
		return e, ErrEnvelope
	}
	if _, err := d.Token(); err != io.EOF || len(fields) != 5 {
		return e, ErrEnvelope
	}
	if json.Unmarshal(data, &e) != nil {
		return e, ErrEnvelope
	}
	if e.Version != Version {
		return e, ErrVersion
	}
	if e.Algorithm != Algorithm || len(e.EncryptedKey) == 0 || len(e.EncryptedKey) > MaxEncryptedKeyBytes || len(e.Nonce) != 12 || len(e.Ciphertext) < 16 || len(e.Ciphertext) > MaxPlaintextBytes+16 {
		return e, ErrEnvelope
	}
	return e, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func keyError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	// Never wrap adapter errors: these may contain sensitive request material.
	return ErrKMS
}

func wipe(data []byte) { clear(data) }
