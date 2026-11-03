package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestQuoteLibpqValue(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
	}{
		{"simple", "secret", "secret"},
		{"timezone", "Asia/Shanghai", "Asia/Shanghai"},
		{"equals", "a=b", "a=b"},
		{"double quotes", `a"b`, `a"b`},
		{"empty", "", "''"},
		{"space", "a b", "'a b'"},
		{"leading and trailing spaces", " a ", "' a '"},
		{"tab", "a\tb", "'a\tb'"},
		{"newline", "a\nb", "'a\nb'"},
		{"carriage return", "a\rb", "'a\rb'"},
		{"vertical tab", "a\vb", "'a\vb'"},
		{"form feed", "a\fb", "'a\fb'"},
		{"unicode whitespace", "a b", "'a b'"},
		{"quote", "a'b", `'a\'b'`},
		{"backslash", `a\b`, `'a\\b'`},
		{"injection", `x'\ sslmode=disable`, `'x\'\\ sslmode=disable'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, quoteLibpqValue(tc.value))
		})
	}
}

func TestDatabaseDSNLibpqRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, value string
	}{
		{"simple", "simple"},
		{"space", " leading trailing "},
		{"quotes", `quote'and"double`},
		{"backslashes", `path\with\trailing\`},
		{"whitespace", "tabs\tnewlines\nand\r\v\f spaces"},
		{"connection parameter injection", `value'\ sslmode=disable password=attacker`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, field := range []string{"host", "user", "password", "dbname", "sslmode", "sslrootcert", "TimeZone"} {
				t.Run(field, func(t *testing.T) {
					database := DatabaseConfig{
						Host: "database.example.com", Port: 5432, User: "runtime", Password: "secret",
						DBName: "sub2api", SSLMode: "verify-full", SSLRootCert: "/opt/certs/rds.pem",
					}
					timezone := "UTC"
					switch field {
					case "host":
						database.Host = tc.value
					case "user":
						database.User = tc.value
					case "password":
						database.Password = tc.value
					case "dbname":
						database.DBName = tc.value
					case "sslmode":
						database.SSLMode = tc.value
					case "sslrootcert":
						database.SSLRootCert = tc.value
					case "TimeZone":
						timezone = tc.value
					}
					// Use lib/pq's independent URL parser as the escaping oracle.
					// NewConnector parses only: no database or network is needed.
					params := url.Values{
						"host": {database.Host}, "port": {strconv.Itoa(database.Port)},
						"user": {database.User}, "password": {database.Password},
						"dbname": {database.DBName}, "sslmode": {database.SSLMode},
						"sslrootcert": {database.SSLRootCert},
					}
					for _, withTimezone := range []bool{false, true} {
						dsn := database.DSN()
						if withTimezone {
							dsn = database.DSNWithTimezone(timezone)
							params.Set("TimeZone", timezone)
						}
						reference := "postgresql:///?" + params.Encode()
						want, err := pq.NewConnector(reference)
						require.NoError(t, err)
						got, err := pq.NewConnector(dsn)
						require.NoError(t, err)
						require.Equal(t, want, got, "parsed connection settings must preserve each field")
					}
				})
			}
		})
	}
}

func TestDatabaseDSNOptionalFieldsAndLegacyFormat(t *testing.T) {
	database := DatabaseConfig{Host: "localhost", Port: 5432, User: "runtime", DBName: "sub2api", SSLMode: "prefer"}
	base := "host=localhost port=5432 user=runtime dbname=sub2api sslmode=prefer"
	require.Equal(t, base, database.DSN())
	require.Equal(t, base+" TimeZone=Asia/Shanghai", database.DSNWithTimezone(""))
	require.Equal(t, base+" TimeZone=UTC", database.DSNWithTimezone("UTC"))
	database.Password = "secret"
	database.SSLRootCert = "/opt/certs/rds.pem"
	require.Equal(t, "host=localhost port=5432 user=runtime password=secret dbname=sub2api sslmode=prefer sslrootcert=/opt/certs/rds.pem", database.DSN())
	require.Contains(t, database.DSNWithTimezone("UTC"), "sslrootcert=/opt/certs/rds.pem TimeZone=UTC")

	// Explicitly quote empty text fields so they cannot consume the next key.
	database = DatabaseConfig{Port: 5432}
	require.Equal(t, "host='' port=5432 user='' dbname='' sslmode=''", database.DSN())
	_, err := pq.NewConnector(database.DSN())
	require.NoError(t, err)
}

func TestLoadDatabaseSSLRootCert(t *testing.T) {
	for _, source := range []string{"default", "file", "environment overrides file"} {
		t.Run(source, func(t *testing.T) {
			resetCloudRuntimeConfig(t)
			t.Setenv("DATABASE_SSLROOTCERT", "")
			t.Setenv("DATABASE_SSLMODE", "")
			wantRootCert := ""
			wantMode := "prefer"
			if source != "default" {
				path := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte("database:\n  sslmode: verify-full\n  sslrootcert: /opt/certs/rds.pem\n"), 0600))
				t.Setenv("CONFIG_FILE", path)
				wantRootCert = "/opt/certs/rds.pem"
				wantMode = "verify-full"
			}
			if source == "environment overrides file" {
				wantRootCert = `/opt/cert bundles/rds'root\cert.pem`
				t.Setenv("DATABASE_SSLROOTCERT", wantRootCert)
			}
			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, wantRootCert, cfg.Database.SSLRootCert)
			require.Equal(t, wantMode, cfg.Database.SSLMode)
			require.Equal(t, DatabaseMigrationModeAuto, cfg.Database.EffectiveMigrationMode())
		})
	}
}

func TestValidateDatabasePort(t *testing.T) {
	resetCloudRuntimeConfig(t)
	cfg, err := Load()
	require.NoError(t, err)
	cfg.Database.Password = "secret-do-not-print"
	for _, port := range []int{-1, 0, 1, 5432, 65535, 65536} {
		t.Run(strconv.Itoa(port), func(t *testing.T) {
			candidate := *cfg
			candidate.Database.Port = port
			err := candidate.Validate()
			if port >= 1 && port <= 65535 {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "database.port must be between 1 and 65535")
			require.NotContains(t, err.Error(), cfg.Database.Password)
		})
	}
}
