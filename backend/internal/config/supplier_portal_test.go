package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestSupplierPortalConfig(t *testing.T) {
	base := Config{Runtime: RuntimeConfig{Role: RuntimeRoleControl, EnableSplitRoles: true}, Database: DatabaseConfig{MigrationMode: DatabaseMigrationModeCheck}, JWT: JWTConfig{Secret: strings.Repeat("c", 32)}, SupplierPortal: SupplierPortalConfig{Enabled: true, Host: "supplier.example.test", Origin: "https://supplier.example.test", JWTSecret: strings.Repeat("s", 32)}}
	require.NoError(t, base.ValidateSupplierPortal())
	for _, mut := range []func(*Config){func(c *Config) { c.SupplierPortal.JWTSecret = c.JWT.Secret }, func(c *Config) { c.SupplierPortal.JWTSecret = "short" }, func(c *Config) { c.SupplierPortal.Origin = "http://supplier.example.test" }, func(c *Config) { c.SupplierPortal.Origin = "https://other.example.test" }, func(c *Config) { c.SupplierPortal.Host = "*" }, func(c *Config) { c.Runtime.Role = RuntimeRoleAll }, func(c *Config) { c.Database.MigrationMode = DatabaseMigrationModeAuto }} {
		c := base
		mut(&c)
		require.Error(t, c.ValidateSupplierPortal())
	}
	base.SupplierPortal.Enabled = false
	require.NoError(t, base.ValidateSupplierPortal())
}

func TestSupplierPortalEnvironmentDefaultsRegistered(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()
	t.Setenv("SUPPLIER_PORTAL_ENABLED", "true")
	t.Setenv("SUPPLIER_PORTAL_HOST", "supplier.example.test")
	t.Setenv("SUPPLIER_PORTAL_JWT_SECRET", strings.Repeat("s", 32))
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	var cfg Config
	require.NoError(t, viper.Unmarshal(&cfg))
	require.True(t, cfg.SupplierPortal.Enabled)
	require.Equal(t, "supplier.example.test", cfg.SupplierPortal.Host)
	require.Len(t, cfg.SupplierPortal.JWTSecret, 32)
}

func TestSupplierProbeIndependentWorkerConfig(t *testing.T) {
	c := Config{Runtime: RuntimeConfig{Role: RuntimeRoleWorker, EnableSplitRoles: true}, Database: DatabaseConfig{MigrationMode: DatabaseMigrationModeCheck}, SupplierProbe: SupplierProbeConfig{Enabled: true}, SupplierPortal: SupplierPortalConfig{VaultKeyFile: "/operator/keys", VaultKeyID: "v1"}}
	require.NoError(t, c.ValidateSupplierPortal())
	require.Equal(t, 600, c.SupplierProbe.IntervalSeconds)
	c.Runtime.Role = RuntimeRoleControl
	require.Error(t, c.ValidateSupplierPortal())
	c.Runtime.Role = RuntimeRoleWorker
	c.SupplierProbe.IntervalSeconds = 10
	require.Error(t, c.ValidateSupplierPortal())
}
