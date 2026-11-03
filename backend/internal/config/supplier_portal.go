package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

type SupplierProbeConfig struct {
	Enabled         bool `mapstructure:"enabled"`
	IntervalSeconds int  `mapstructure:"interval_seconds"`
}

// SupplierPortal selects a dedicated control process, never a second gateway.
type SupplierPortalConfig struct {
	VaultKeyFile        string `mapstructure:"vault_key_file"`
	VaultKeyID          string `mapstructure:"vault_key_id"`
	Enabled             bool   `mapstructure:"enabled"`
	Host                string `mapstructure:"host"`
	Origin              string `mapstructure:"origin"`
	JWTSecret           string `mapstructure:"jwt_secret"`
	RegistrationEnabled bool   `mapstructure:"registration_enabled"`
}

func (c *Config) ValidateSupplierPortal() error {
	if c.SupplierProbe.IntervalSeconds == 0 {
		c.SupplierProbe.IntervalSeconds = 600
	}
	if c.SupplierProbe.IntervalSeconds < 300 || c.SupplierProbe.IntervalSeconds > 900 {
		return fmt.Errorf("supplier_probe.interval_seconds must be 300..900")
	}
	if c.SupplierProbe.Enabled && (c.Runtime.Role != RuntimeRoleWorker || !c.Runtime.EnableSplitRoles || c.Database.EffectiveMigrationMode() != DatabaseMigrationModeCheck) {
		return fmt.Errorf("supplier_probe requires opted-in check-only worker")
	}
	if c.SupplierProbe.Enabled && (c.SupplierPortal.VaultKeyFile == "" || c.SupplierPortal.VaultKeyID == "") {
		return fmt.Errorf("supplier_probe requires explicit vault keys")
	}
	s := c.SupplierPortal
	if (s.VaultKeyFile == "") != (s.VaultKeyID == "") {
		return fmt.Errorf("supplier vault file and active key ID must be configured together")
	}
	if s.VaultKeyFile != "" && (!filepath.IsAbs(s.VaultKeyFile) || len(s.VaultKeyID) > 128) {
		return fmt.Errorf("supplier vault requires an absolute key file and bounded key ID")
	}
	if !s.Enabled {
		return nil
	}
	if c.Runtime.Role != RuntimeRoleControl || !c.Runtime.EnableSplitRoles {
		return fmt.Errorf("supplier_portal requires an opted-in control role")
	}
	if c.Database.EffectiveMigrationMode() != DatabaseMigrationModeCheck {
		return fmt.Errorf("supplier_portal requires check-only schema mode")
	}
	if len(strings.TrimSpace(s.JWTSecret)) < 32 || strings.TrimSpace(s.JWTSecret) != s.JWTSecret || s.JWTSecret == c.JWT.Secret {
		return fmt.Errorf("supplier_portal.jwt_secret must be distinct from consumer JWT secret and at least 32 characters")
	}
	u, err := url.Parse(s.Origin)
	if err != nil || u.Scheme != "https" || u.Host != s.Host || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("supplier_portal.origin must be an exact HTTPS origin matching host")
	}
	if s.Host == "" || strings.ContainsAny(s.Host, " /\\@*\r\n") || strings.ToLower(s.Host) != s.Host {
		return fmt.Errorf("supplier_portal.host must be a canonical explicit host")
	}
	hostname := u.Hostname()
	if net.ParseIP(hostname) == nil && !strings.Contains(hostname, ".") {
		return fmt.Errorf("supplier_portal.host must be a fully qualified hostname")
	}
	return nil
}
