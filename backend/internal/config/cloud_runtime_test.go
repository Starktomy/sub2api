package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRuntimeConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     RuntimeConfig
		want    RuntimeConfig
		wantErr string
	}{
		{
			name: "legacy zero value",
			want: RuntimeConfig{Role: RuntimeRoleAll, ShutdownTimeoutSeconds: 90},
		},
		{
			name: "normalized all",
			cfg:  RuntimeConfig{Role: " ALL ", ShutdownTimeoutSeconds: 1},
			want: RuntimeConfig{Role: RuntimeRoleAll, ShutdownTimeoutSeconds: 1},
		},
		{
			name: "gateway syntax requires explicit opt-in",
			cfg:  RuntimeConfig{Role: " Gateway "},
			want: RuntimeConfig{Role: RuntimeRoleGateway, ShutdownTimeoutSeconds: 90},
		},
		{
			name: "control syntax requires explicit opt-in",
			cfg:  RuntimeConfig{Role: RuntimeRoleControl},
			want: RuntimeConfig{Role: RuntimeRoleControl, ShutdownTimeoutSeconds: 90},
		},
		{
			name: "worker syntax requires explicit opt-in",
			cfg:  RuntimeConfig{Role: RuntimeRoleWorker},
			want: RuntimeConfig{Role: RuntimeRoleWorker, ShutdownTimeoutSeconds: 90},
		},
		{
			name: "maximum shutdown budget",
			cfg:  RuntimeConfig{ShutdownTimeoutSeconds: 110},
			want: RuntimeConfig{Role: RuntimeRoleAll, ShutdownTimeoutSeconds: 110},
		},
		{name: "unknown role", cfg: RuntimeConfig{Role: "scheduler"}, wantErr: "runtime.role"},
		{name: "multiple roles", cfg: RuntimeConfig{Role: "gateway,worker"}, wantErr: "runtime.role"},
		{name: "negative timeout", cfg: RuntimeConfig{ShutdownTimeoutSeconds: -1}, wantErr: "runtime.shutdown_timeout_seconds"},
		{name: "oversized timeout", cfg: RuntimeConfig{ShutdownTimeoutSeconds: 111}, wantErr: "runtime.shutdown_timeout_seconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, tc.cfg)
		})
	}
}

func TestRuntimeConfigValidateRoleSupport(t *testing.T) {
	for _, role := range []string{"", " ", RuntimeRoleAll, " ALL "} {
		t.Run("supported_"+role, func(t *testing.T) {
			require.NoError(t, (RuntimeConfig{Role: role}).ValidateRoleSupport())
		})
	}
	for _, role := range []string{RuntimeRoleGateway, RuntimeRoleControl, RuntimeRoleWorker, " WORKER "} {
		t.Run("disabled_by_default_"+role, func(t *testing.T) {
			// This check must fail even if the caller has not run Validate yet.
			cfg := RuntimeConfig{Role: role}
			require.ErrorContains(t, cfg.ValidateRoleSupport(), "not enabled")
			require.NoError(t, cfg.Validate(), "reserved roles remain parseable")
			require.ErrorContains(t, cfg.ValidateRoleSupport(), "background task isolation")
		})
	}
	require.ErrorContains(t, (RuntimeConfig{Role: "typo"}).ValidateRoleSupport(), "runtime.role")
}

func TestRuntimeExperimentalSplitRolesRequireReadOnlyStartup(t *testing.T) {
	for _, role := range []string{RuntimeRoleGateway, RuntimeRoleControl, RuntimeRoleWorker} {
		runtime := RuntimeConfig{Role: role, EnableSplitRoles: true}
		require.NoError(t, runtime.ValidateRoleSupport())
		require.ErrorContains(t, runtime.ValidateMigrationPolicy(DatabaseMigrationModeAuto), "require database.migration_mode=check")
		require.NoError(t, runtime.ValidateMigrationPolicy(DatabaseMigrationModeCheck))
	}
}

func TestLoadExperimentalRolesRequireStandardMode(t *testing.T) {
	resetCloudRuntimeConfig(t)
	t.Setenv("RUNTIME_ROLE", RuntimeRoleGateway)
	t.Setenv("RUNTIME_ENABLE_SPLIT_ROLES", "true")
	t.Setenv("DATABASE_MIGRATION_MODE", DatabaseMigrationModeCheck)
	t.Setenv("TOTP_ENCRYPTION_KEY", strings.Repeat("a", 64))
	t.Setenv("RUN_MODE", RunModeSimple)
	_, err := Load()
	require.ErrorContains(t, err, "standard run mode")
	t.Setenv("RUN_MODE", RunModeStandard)
	cfg, err := Load()
	require.NoError(t, err)
	require.NoError(t, cfg.Runtime.ValidateRoleSupport())
}

func TestRuntimeConfigShutdownTimeout(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    time.Duration
	}{
		{0, 90 * time.Second},
		{1, time.Second},
		{90, 90 * time.Second},
		{110, 110 * time.Second},
	} {
		require.Equal(t, tc.want, (RuntimeConfig{ShutdownTimeoutSeconds: tc.seconds}).ShutdownTimeout())
	}
}

func TestDatabaseMigrationMode(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    string
		wantErr bool
	}{
		{value: "", want: DatabaseMigrationModeAuto},
		{value: " ", want: DatabaseMigrationModeAuto},
		{value: DatabaseMigrationModeAuto, want: DatabaseMigrationModeAuto},
		{value: " AUTO ", want: DatabaseMigrationModeAuto},
		{value: DatabaseMigrationModeCheck, want: DatabaseMigrationModeCheck},
		{value: " CHECK ", want: DatabaseMigrationModeCheck},
		{value: "apply", want: "apply", wantErr: true},
		{value: "skip", want: "skip", wantErr: true},
		{value: "disabled", want: "disabled", wantErr: true},
		{value: "typo", want: "typo", wantErr: true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			cfg := DatabaseConfig{MigrationMode: tc.value}
			require.Equal(t, tc.want, cfg.EffectiveMigrationMode())
			err := cfg.validateMigrationMode()
			if tc.wantErr {
				require.ErrorContains(t, err, "database.migration_mode must be one of auto, check")
				require.Equal(t, tc.value, cfg.MigrationMode, "invalid modes must not fall back to auto")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.MigrationMode)
		})
	}
}

// Tests that use the package-level viper instance intentionally remain serial,
// following resetViperWithJWTSecret and the other config loading tests.
func resetCloudRuntimeConfig(t *testing.T) {
	t.Helper()
	resetViperWithJWTSecret(t)
	t.Setenv("RUNTIME_ROLE", "")
	t.Setenv("RUNTIME_ENABLE_SPLIT_ROLES", "false")
	t.Setenv("RUNTIME_SHUTDOWN_TIMEOUT_SECONDS", "")
	t.Setenv("DATABASE_MIGRATION_MODE", "")
	t.Setenv("TOTP_ENCRYPTION_KEY", "")
}

func TestLoadCloudRuntimeDefaults(t *testing.T) {
	resetCloudRuntimeConfig(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, RuntimeRoleAll, cfg.Runtime.Role)
	require.Equal(t, 90, cfg.Runtime.ShutdownTimeoutSeconds)
	require.Equal(t, 90*time.Second, cfg.Runtime.ShutdownTimeout())
	require.NoError(t, cfg.Runtime.ValidateRoleSupport())
	require.Equal(t, DatabaseMigrationModeAuto, cfg.Database.MigrationMode)

	// Existing tests and callers that construct Config values directly need no
	// new fields; validation fills the same backwards-compatible defaults.
	cfg.Runtime = RuntimeConfig{}
	cfg.Database.MigrationMode = ""
	require.NoError(t, cfg.Validate())
	require.Equal(t, RuntimeRoleAll, cfg.Runtime.Role)
	require.Equal(t, 90, cfg.Runtime.ShutdownTimeoutSeconds)
	require.Equal(t, DatabaseMigrationModeAuto, cfg.Database.MigrationMode)
}

func TestLoadCloudRuntimeFromEnv(t *testing.T) {
	for _, role := range []string{RuntimeRoleAll, RuntimeRoleGateway, RuntimeRoleControl, RuntimeRoleWorker} {
		t.Run(role, func(t *testing.T) {
			resetCloudRuntimeConfig(t)
			t.Setenv("RUNTIME_ROLE", role)
			t.Setenv("RUNTIME_SHUTDOWN_TIMEOUT_SECONDS", "100")
			t.Setenv("DATABASE_MIGRATION_MODE", " CHECK ")
			t.Setenv("TOTP_ENCRYPTION_KEY", strings.Repeat("a", 64))
			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, role, cfg.Runtime.Role)
			require.Equal(t, 100, cfg.Runtime.ShutdownTimeoutSeconds)
			require.Equal(t, 100*time.Second, cfg.Runtime.ShutdownTimeout())
			require.Equal(t, DatabaseMigrationModeCheck, cfg.Database.MigrationMode)
			if role == RuntimeRoleAll {
				require.NoError(t, cfg.Runtime.ValidateRoleSupport())
			} else {
				require.ErrorContains(t, cfg.Runtime.ValidateRoleSupport(), "not enabled")
			}
		})
	}
}

func TestLoadCloudRuntimeFromFileAndEnvOverride(t *testing.T) {
	resetCloudRuntimeConfig(t)
	t.Setenv("TOTP_ENCRYPTION_KEY", strings.Repeat("a", 64))
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte("runtime:\n  role: control\n  shutdown_timeout_seconds: 75\ndatabase:\n  migration_mode: check\n"), 0o600))
	t.Setenv("CONFIG_FILE", configFile)

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, RuntimeRoleControl, cfg.Runtime.Role)
	require.Equal(t, 75, cfg.Runtime.ShutdownTimeoutSeconds)
	require.Equal(t, DatabaseMigrationModeCheck, cfg.Database.MigrationMode)
	require.ErrorContains(t, cfg.Runtime.ValidateRoleSupport(), "not enabled")

	t.Setenv("RUNTIME_ROLE", "all")
	t.Setenv("RUNTIME_SHUTDOWN_TIMEOUT_SECONDS", "80")
	t.Setenv("DATABASE_MIGRATION_MODE", "auto")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, RuntimeRoleAll, cfg.Runtime.Role)
	require.Equal(t, 80, cfg.Runtime.ShutdownTimeoutSeconds)
	require.Equal(t, DatabaseMigrationModeAuto, cfg.Database.MigrationMode)
}

func TestLoadCloudRuntimeRejectsInvalidEnv(t *testing.T) {
	for _, tc := range []struct {
		key, value, wantErr string
	}{
		{"RUNTIME_ROLE", "scheduler", "runtime.role"},
		{"RUNTIME_SHUTDOWN_TIMEOUT_SECONDS", "-1", "runtime.shutdown_timeout_seconds"},
		{"RUNTIME_SHUTDOWN_TIMEOUT_SECONDS", "111", "runtime.shutdown_timeout_seconds"},
		{"RUNTIME_SHUTDOWN_TIMEOUT_SECONDS", "not-a-number", "runtime.shutdown_timeout_seconds"},
		{"DATABASE_MIGRATION_MODE", "apply", "database.migration_mode"},
		{"DATABASE_MIGRATION_MODE", "skip", "database.migration_mode"},
	} {
		t.Run(tc.key+"_"+tc.value, func(t *testing.T) {
			resetCloudRuntimeConfig(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestCheckModeRequiresSharedSecrets(t *testing.T) {
	for _, loader := range []struct {
		name string
		load func() (*Config, error)
	}{
		{name: "Load", load: Load},
		{name: "LoadForBootstrap", load: LoadForBootstrap},
	} {
		t.Run(loader.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, jwtSecret, totpKey, wantErr string
			}{
				{name: "missing both", wantErr: "jwt.secret (JWT_SECRET) must be explicitly configured"},
				{name: "missing JWT", totpKey: strings.Repeat("a", 64), wantErr: "jwt.secret (JWT_SECRET) must be explicitly configured"},
				{name: "blank JWT", jwtSecret: " \t ", totpKey: strings.Repeat("a", 64), wantErr: "jwt.secret (JWT_SECRET) must be explicitly configured"},
				{name: "missing TOTP", jwtSecret: strings.Repeat("x", 32), wantErr: "totp.encryption_key (TOTP_ENCRYPTION_KEY) must be explicitly configured"},
				{name: "blank TOTP", jwtSecret: strings.Repeat("x", 32), totpKey: " \t ", wantErr: "totp.encryption_key (TOTP_ENCRYPTION_KEY) must be explicitly configured"},
				{name: "explicit shared secrets", jwtSecret: strings.Repeat("x", 32), totpKey: strings.Repeat("a", 64)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					resetCloudRuntimeConfig(t)
					t.Setenv("DATABASE_MIGRATION_MODE", " CHECK ")
					t.Setenv("JWT_SECRET", tc.jwtSecret)
					t.Setenv("TOTP_ENCRYPTION_KEY", tc.totpKey)
					cfg, err := loader.load()
					if tc.wantErr != "" {
						require.ErrorContains(t, err, tc.wantErr)
						require.Nil(t, cfg)
						return
					}
					require.NoError(t, err)
					require.Equal(t, tc.jwtSecret, cfg.JWT.Secret)
					require.Equal(t, tc.totpKey, cfg.Totp.EncryptionKey)
					require.True(t, cfg.Totp.EncryptionKeyConfigured)
				})
			}
		})
	}
}

func TestCheckModeAcceptsSharedSecretsFromFile(t *testing.T) {
	resetCloudRuntimeConfig(t)
	t.Setenv("JWT_SECRET", "")
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	content := "database:\n  migration_mode: check\njwt:\n  secret: " + strings.Repeat("x", 32) + "\ntotp:\n  encryption_key: " + strings.Repeat("a", 64) + "\n"
	require.NoError(t, os.WriteFile(configFile, []byte(content), 0o600))
	t.Setenv("CONFIG_FILE", configFile)

	cfg, err := LoadForBootstrap()
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("x", 32), cfg.JWT.Secret)
	require.Equal(t, strings.Repeat("a", 64), cfg.Totp.EncryptionKey)
	require.True(t, cfg.Totp.EncryptionKeyConfigured)
}

func TestConfigValidateCheckModeRequiresSharedSecrets(t *testing.T) {
	resetCloudRuntimeConfig(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.Totp.EncryptionKeyConfigured)
	require.NotEmpty(t, cfg.Totp.EncryptionKey)

	// Switching a legacy configuration to check must not accept its generated
	// TOTP key merely because the key string happens to be nonempty.
	cfg.Database.MigrationMode = DatabaseMigrationModeCheck
	require.ErrorContains(t, cfg.Validate(), "auto-generated keys are not allowed")
	cfg.Totp.EncryptionKeyConfigured = true
	cfg.Totp.EncryptionKey = " \t "
	require.ErrorContains(t, cfg.Validate(), "totp.encryption_key (TOTP_ENCRYPTION_KEY)")
	cfg.Totp.EncryptionKey = strings.Repeat("a", 64)
	cfg.JWT.Secret = " \t "
	require.ErrorContains(t, cfg.Validate(), "jwt.secret (JWT_SECRET)")
	cfg.JWT.Secret = strings.Repeat("x", 32)
	require.NoError(t, cfg.Validate())
}

func TestAutoModeRetainsSecretBootstrap(t *testing.T) {
	resetCloudRuntimeConfig(t)
	t.Setenv("JWT_SECRET", "")
	cfg, err := LoadForBootstrap()
	require.NoError(t, err)
	require.Empty(t, cfg.JWT.Secret, "the bootstrap placeholder must not escape LoadForBootstrap")
	require.Len(t, cfg.Totp.EncryptionKey, 64)
	require.False(t, cfg.Totp.EncryptionKeyConfigured)
}

func TestConfigValidateCloudRuntime(t *testing.T) {
	resetCloudRuntimeConfig(t)
	cfg, err := Load()
	require.NoError(t, err)

	cfg.Runtime.Role = "scheduler"
	require.ErrorContains(t, cfg.Validate(), "runtime.role")
	cfg.Runtime.Role = RuntimeRoleAll
	cfg.Runtime.ShutdownTimeoutSeconds = 111
	require.ErrorContains(t, cfg.Validate(), "runtime.shutdown_timeout_seconds")
	cfg.Runtime.ShutdownTimeoutSeconds = 90
	cfg.Database.MigrationMode = "apply"
	require.ErrorContains(t, cfg.Validate(), "database.migration_mode")
}
