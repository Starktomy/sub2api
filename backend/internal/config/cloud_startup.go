package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// StartupPolicy is the small non-secret subset needed before deciding whether
// the legacy setup wizard may run. It does not load/generate credentials or
// initialize the global Viper configuration used by the application.
type StartupPolicy struct {
	Runtime  RuntimeConfig `mapstructure:"runtime"`
	Database struct {
		MigrationMode string `mapstructure:"migration_mode"`
	} `mapstructure:"database"`
}

func LoadStartupPolicy() (StartupPolicy, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	configureConfigSource(v.SetConfigFile, v.AddConfigPath)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("runtime.role", RuntimeRoleAll)
	v.SetDefault("runtime.shutdown_timeout_seconds", DefaultRuntimeShutdownTimeoutSeconds)
	v.SetDefault("database.migration_mode", DatabaseMigrationModeAuto)
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return StartupPolicy{}, fmt.Errorf("read startup policy: %w", err)
		}
	}
	var policy StartupPolicy
	if err := v.Unmarshal(&policy); err != nil {
		return StartupPolicy{}, fmt.Errorf("read startup policy: %w", err)
	}
	if err := policy.Runtime.Validate(); err != nil {
		return StartupPolicy{}, err
	}
	database := DatabaseConfig{MigrationMode: policy.Database.MigrationMode}
	if err := database.validateMigrationMode(); err != nil {
		return StartupPolicy{}, err
	}
	policy.Database.MigrationMode = database.MigrationMode
	return policy, nil
}
