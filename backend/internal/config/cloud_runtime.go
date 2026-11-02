package config

import (
	"fmt"
	"strings"
	"time"
)

const (
	RuntimeRoleAll     = "all"
	RuntimeRoleGateway = "gateway"
	RuntimeRoleControl = "control"
	RuntimeRoleWorker  = "worker"

	DatabaseMigrationModeAuto  = "auto"
	DatabaseMigrationModeCheck = "check"

	DefaultRuntimeShutdownTimeoutSeconds = 90
	// MaxRuntimeShutdownTimeoutSeconds caps the HTTP shutdown phase below ECS's
	// 120-second stop timeout. Cleanup still needs its own bounded lifecycle;
	// this setting alone does not bound the complete process shutdown.
	MaxRuntimeShutdownTimeoutSeconds = 110
)

// RuntimeConfig selects the process role and HTTP shutdown budget. Only the
// legacy all-in-one role is implemented; other role names are reserved until
// their dependency wiring and background tasks are actually isolated.
type RuntimeConfig struct {
	Role                   string `mapstructure:"role"`
	ShutdownTimeoutSeconds int    `mapstructure:"shutdown_timeout_seconds"`
}

// Validate normalizes defaults and validates configuration syntax. Executables
// must also call ValidateRoleSupport before setup, dependency wiring, or workers
// are started; accepting a role name here does not mean it is implemented.
func (c *RuntimeConfig) Validate() error {
	role, err := normalizeRuntimeRole(c.Role)
	if err != nil {
		return err
	}
	c.Role = role
	if c.ShutdownTimeoutSeconds == 0 {
		c.ShutdownTimeoutSeconds = DefaultRuntimeShutdownTimeoutSeconds
	}
	if c.ShutdownTimeoutSeconds < 1 || c.ShutdownTimeoutSeconds > MaxRuntimeShutdownTimeoutSeconds {
		return fmt.Errorf("runtime.shutdown_timeout_seconds must be between 1 and %d (0 uses the default %d)", MaxRuntimeShutdownTimeoutSeconds, DefaultRuntimeShutdownTimeoutSeconds)
	}
	return nil
}

// ValidateRoleSupport fails closed for roles whose isolation is not implemented.
// It also accepts a zero-valued RuntimeConfig so legacy callers remain all-in-one.
func (c RuntimeConfig) ValidateRoleSupport() error {
	role, err := normalizeRuntimeRole(c.Role)
	if err != nil {
		return err
	}
	if role != RuntimeRoleAll {
		return fmt.Errorf("runtime.role=%q is not implemented: role-specific dependency wiring and background task isolation are unavailable; use %q", role, RuntimeRoleAll)
	}
	return nil
}

// ShutdownTimeout returns the HTTP shutdown budget. A zero-valued configuration
// gets the same default as Load; other values must have passed Validate.
func (c RuntimeConfig) ShutdownTimeout() time.Duration {
	seconds := c.ShutdownTimeoutSeconds
	if seconds == 0 {
		seconds = DefaultRuntimeShutdownTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func normalizeRuntimeRole(value string) (string, error) {
	role := strings.ToLower(strings.TrimSpace(value))
	switch role {
	case "", RuntimeRoleAll:
		return RuntimeRoleAll, nil
	case RuntimeRoleGateway, RuntimeRoleControl, RuntimeRoleWorker:
		return role, nil
	default:
		return "", fmt.Errorf("runtime.role must be one of all, gateway, control, worker (got %q)", value)
	}
}

// EffectiveMigrationMode preserves automatic migrations for zero-valued legacy
// configurations. Unknown values are not silently downgraded to auto.
func (d DatabaseConfig) EffectiveMigrationMode() string {
	mode := strings.ToLower(strings.TrimSpace(d.MigrationMode))
	if mode == "" {
		return DatabaseMigrationModeAuto
	}
	return mode
}

func (d *DatabaseConfig) validateMigrationMode() error {
	mode := d.EffectiveMigrationMode()
	switch mode {
	case DatabaseMigrationModeAuto, DatabaseMigrationModeCheck:
		d.MigrationMode = mode
		return nil
	default:
		return fmt.Errorf("database.migration_mode must be one of auto, check (got %q); use the migrate command to apply migrations separately", d.MigrationMode)
	}
}

// validateCheckModeSharedSecrets is called both before legacy secret fallbacks
// in load and from Validate for configurations constructed or modified in code.
func (c *Config) validateCheckModeSharedSecrets() error {
	if c.Database.EffectiveMigrationMode() != DatabaseMigrationModeCheck {
		return nil
	}
	if strings.TrimSpace(c.JWT.Secret) == "" {
		return fmt.Errorf("jwt.secret (JWT_SECRET) must be explicitly configured when database.migration_mode=check; shared secret bootstrap is disabled")
	}
	if strings.TrimSpace(c.Totp.EncryptionKey) == "" || !c.Totp.EncryptionKeyConfigured {
		return fmt.Errorf("totp.encryption_key (TOTP_ENCRYPTION_KEY) must be explicitly configured when database.migration_mode=check; auto-generated keys are not allowed")
	}
	return nil
}
