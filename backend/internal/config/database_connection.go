package config

import (
	"strconv"
	"strings"
	"unicode"
)

// DSN returns libpq keyword/value connection settings. Every text value is
// escaped independently so credentials cannot inject additional parameters.
func (d *DatabaseConfig) DSN() string {
	parts := []string{
		"host=" + quoteLibpqValue(d.Host),
		"port=" + strconv.Itoa(d.Port),
		"user=" + quoteLibpqValue(d.User),
	}
	// Preserve legacy password-file/environment authentication when no password
	// is configured instead of emitting an explicit empty password.
	if d.Password != "" {
		parts = append(parts, "password="+quoteLibpqValue(d.Password))
	}
	parts = append(parts,
		"dbname="+quoteLibpqValue(d.DBName),
		"sslmode="+quoteLibpqValue(d.SSLMode),
	)
	if d.SSLRootCert != "" {
		parts = append(parts, "sslrootcert="+quoteLibpqValue(d.SSLRootCert))
	}
	return strings.Join(parts, " ")
}

// DSNWithTimezone returns DSN with the legacy default timezone when tz is empty.
func (d *DatabaseConfig) DSNWithTimezone(tz string) string {
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	return d.DSN() + " TimeZone=" + quoteLibpqValue(tz)
}

// quoteLibpqValue keeps simple values unquoted for compatibility. lib/pq treats
// Unicode whitespace as a delimiter; quotes and backslashes must also be escaped
// inside single-quoted values. Empty values must not consume the next keyword.
func quoteLibpqValue(value string) string {
	if value != "" && !strings.ContainsAny(value, "'\\") && strings.IndexFunc(value, unicode.IsSpace) < 0 {
		return value
	}
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
}
