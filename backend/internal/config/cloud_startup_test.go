package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadStartupPolicyBeforeSetup(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("RUNTIME_ROLE", "gateway")
	t.Setenv("DATABASE_MIGRATION_MODE", "check")
	t.Setenv("RUNTIME_SHUTDOWN_TIMEOUT_SECONDS", "100")
	// No JWT/TOTP/DB credentials are needed to refuse an unimplemented role.
	t.Setenv("JWT_SECRET", "")
	t.Setenv("TOTP_ENCRYPTION_KEY", "")
	policy, err := LoadStartupPolicy()
	require.NoError(t, err)
	require.Equal(t, RuntimeRoleGateway, policy.Runtime.Role)
	require.Equal(t, DatabaseMigrationModeCheck, policy.Database.MigrationMode)
	require.Equal(t, 100, policy.Runtime.ShutdownTimeoutSeconds)
	require.ErrorContains(t, policy.Runtime.ValidateRoleSupport(), "not implemented")
}

func TestLoadStartupPolicyFileAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("runtime:\n  role: worker\ndatabase:\n  migration_mode: check\n"), 0o600))
	t.Setenv("CONFIG_FILE", path)
	t.Setenv("RUNTIME_ROLE", "")
	t.Setenv("DATABASE_MIGRATION_MODE", "")
	policy, err := LoadStartupPolicy()
	require.NoError(t, err)
	require.Equal(t, RuntimeRoleWorker, policy.Runtime.Role)
	require.Equal(t, DatabaseMigrationModeCheck, policy.Database.MigrationMode)
	t.Setenv("RUNTIME_ROLE", "all")
	t.Setenv("DATABASE_MIGRATION_MODE", "auto")
	policy, err = LoadStartupPolicy()
	require.NoError(t, err)
	require.NoError(t, policy.Runtime.ValidateRoleSupport())
	require.Equal(t, DatabaseMigrationModeAuto, policy.Database.MigrationMode)
}

func TestLoadStartupPolicyInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"RUNTIME_ROLE", "unknown"},
		{"DATABASE_MIGRATION_MODE", "skip"},
		{"RUNTIME_SHUTDOWN_TIMEOUT_SECONDS", "111"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Setenv("CONFIG_FILE", "")
			t.Setenv("DATA_DIR", t.TempDir())
			t.Setenv("RUNTIME_ROLE", "all")
			t.Setenv("DATABASE_MIGRATION_MODE", "auto")
			t.Setenv("RUNTIME_SHUTDOWN_TIMEOUT_SECONDS", "90")
			t.Setenv(tc.key, tc.value)
			_, err := LoadStartupPolicy()
			require.Error(t, err)
		})
	}
}
