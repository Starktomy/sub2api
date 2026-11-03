package credentialcrypto

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalWrapperPermissionsBindingAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring")
	require.NoError(t, os.WriteFile(path, []byte(`{"keys":{"old":"`+strings.Repeat("11", 32)+`","new":"`+strings.Repeat("22", 32)+`"}}`), 0600))
	w, e := LoadLocalKeyWrapper(path)
	require.NoError(t, e)
	old, e := New(w, "old")
	require.NoError(t, e)
	blob, e := old.Encrypt(context.Background(), "supplier-resource/v1/1/uuid", map[string]any{"access_token": "synthetic"})
	require.NoError(t, e)
	_, e = old.Decrypt(context.Background(), "supplier-resource/v1/2/uuid", blob)
	require.Error(t, e)
	fresh, _ := New(w, "new")
	_, e = fresh.Decrypt(context.Background(), "supplier-resource/v1/1/uuid", blob)
	require.Error(t, e)
	plain, e := old.Decrypt(context.Background(), "supplier-resource/v1/1/uuid", blob)
	require.NoError(t, e)
	require.Equal(t, "synthetic", plain["access_token"])
	blob[len(blob)/2] ^= 1
	_, e = old.Decrypt(context.Background(), "supplier-resource/v1/1/uuid", blob)
	require.Error(t, e)
	require.NoError(t, os.Chmod(path, 0644))
	_, e = LoadLocalKeyWrapper(path)
	require.Error(t, e)
	require.NoError(t, os.Chmod(path, 0600))
	link := path + "link"
	require.NoError(t, os.Symlink(path, link))
	_, e = LoadLocalKeyWrapper(link)
	require.Error(t, e)
}

func TestLocalWrapperRejectsSymlinkParent(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	require.NoError(t, os.Mkdir(real, 0700))
	path := filepath.Join(real, "key")
	require.NoError(t, os.WriteFile(path, []byte(`{"keys":{"k":"`+strings.Repeat("11", 32)+`"}}`), 0600))
	require.NoError(t, os.Symlink(real, filepath.Join(dir, "link")))
	_, e := LoadLocalKeyWrapper(filepath.Join(dir, "link", "key"))
	require.Error(t, e)
}
