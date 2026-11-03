package credentialcrypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// LocalKeyWrapper is an explicit operator-managed local wrapping keyring, not AWS KMS.
// File: {"keys":{"operator-key-id":"64 hex characters"}}. Keep old IDs for
// decryption after changing the active ID. No automatic re-encryption or fallback.
type LocalKeyWrapper struct{ keys map[string][]byte }

func LoadLocalKeyWrapper(path string) (*LocalKeyWrapper, error) {
	fail := func() (*LocalKeyWrapper, error) { return nil, ErrConfiguration }
	if !filepath.IsAbs(path) {
		return fail()
	}
	// Traverse every parent through directory FDs: no symlink parent may
	// redirect key reads; shared writable parents must use sticky-bit semantics.
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	parent, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fail()
	}
	for _, part := range parts[:len(parts)-1] {
		next, e := syscall.Openat(parent, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(parent)
		if e != nil {
			return fail()
		}
		parent = next
		var st syscall.Stat_t
		if syscall.Fstat(parent, &st) != nil || st.Mode&0022 != 0 && st.Mode&syscall.S_ISVTX == 0 {
			syscall.Close(parent)
			return fail()
		}
	}
	fd, err := syscall.Openat(parent, parts[len(parts)-1], syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	syscall.Close(parent)
	if err != nil {
		return fail()
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 16384 {
		return fail()
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fail()
	}
	b, err := io.ReadAll(io.LimitReader(f, 16385))
	defer clear(b)
	if err != nil || len(b) > 16384 {
		return fail()
	}
	var input struct {
		Keys map[string]string `json:"keys"`
	}
	if json.Unmarshal(b, &input) != nil || len(input.Keys) == 0 || len(input.Keys) > 16 {
		return fail()
	}
	w := &LocalKeyWrapper{keys: map[string][]byte{}}
	for id, text := range input.Keys {
		key, err := hex.DecodeString(text)
		if err != nil || len(key) != 32 || len(id) == 0 || len(id) > 128 {
			return fail()
		}
		w.keys[id] = key
	}
	return w, nil
}
func (w *LocalKeyWrapper) gcm(id string) (cipher.AEAD, error) {
	key := w.keys[id]
	if len(key) != 32 {
		return nil, ErrConfiguration
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}
func (w *LocalKeyWrapper) GenerateDataKey(ctx context.Context, id string, ec map[string]string) ([]byte, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	g, e := w.gcm(id)
	if e != nil {
		return nil, nil, e
	}
	key := make([]byte, 32)
	if _, e = rand.Read(key); e != nil {
		return nil, nil, e
	}
	n := make([]byte, g.NonceSize())
	if _, e = rand.Read(n); e != nil {
		clear(key)
		return nil, nil, e
	}
	aad, _ := json.Marshal(ec)
	return key, g.Seal(n, n, key, aad), nil
}
func (w *LocalKeyWrapper) Decrypt(ctx context.Context, id string, wrapped []byte, ec map[string]string) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	g, e := w.gcm(id)
	if e != nil {
		return nil, e
	}
	if len(wrapped) < g.NonceSize()+g.Overhead() {
		return nil, errors.New("invalid wrapped key")
	}
	aad, _ := json.Marshal(ec)
	return g.Open(nil, wrapped[:g.NonceSize()], wrapped[g.NonceSize():], aad)
}

func (w *LocalKeyWrapper) HasKey(id string) bool { return w != nil && len(w.keys[id]) == 32 }
