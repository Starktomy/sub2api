package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestRunHelpDoesNotRequireDatabase(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"-help"}, func(string) string {
		t.Fatal("help must not access database configuration")
		return ""
	}, &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), "SSLMODE defaults to verify-full")
}

func TestRunRejectsInvalidArgumentsBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{{"unexpected"}, {"-timeout", "0s"}, {"-unknown"}} {
		err := run(context.Background(), args, func(string) string {
			t.Fatal("invalid flags must not read database configuration")
			return ""
		}, io.Discard)
		require.Error(t, err)
	}
}

func TestDatabaseConnectorValidation(t *testing.T) {
	valid := map[string]string{"DATABASE_HOST": "127.0.0.1", "DATABASE_USER": "runtime", "DATABASE_DBNAME": "sub2api"}
	for _, tc := range []struct {
		name, key, value, wantError string
	}{
		{"host missing", "DATABASE_HOST", "", "DATABASE_HOST"},
		{"user missing", "DATABASE_USER", "", "DATABASE_USER"},
		{"database missing", "DATABASE_DBNAME", "", "DATABASE_DBNAME"},
		{"invalid port", "DATABASE_PORT", "99999", "DATABASE_PORT"},
		{"negative port", "DATABASE_PORT", "-1", "DATABASE_PORT"},
		{"zero port", "DATABASE_PORT", "0", "DATABASE_PORT"},
		{"port injection redacted", "DATABASE_PORT", "5432 password=secret-do-not-print", "DATABASE_PORT"},
		{"invalid TLS mode", "DATABASE_SSLMODE", "prefer", "DATABASE_SSLMODE"},
		{"TLS injection redacted", "DATABASE_SSLMODE", "verify-full password=secret-do-not-print", "DATABASE_SSLMODE"},
		{"invalid DSN redacted", "DATABASE_DSN", "password='secret-do-not-print", "connection details omitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := databaseConnector(func(key string) string {
				if key == tc.key {
					return tc.value
				}
				return valid[key]
			})
			require.ErrorContains(t, err, tc.wantError)
			require.NotContains(t, err.Error(), "secret-do-not-print")
		})
	}
}

func TestDatabaseConnectorEnvironmentLibpqParsing(t *testing.T) {
	for _, tc := range []struct {
		name, password, rootCert string
	}{
		{"default TLS", "secret", ""},
		{"empty password", "", ""},
		{"special credentials and root cert", `quotes' slash\ spaces sslmode=disable`, `/opt/cert bundles/rds'root\cert.pem`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{
				"DATABASE_HOST": "database.example.com", "DATABASE_PORT": "5432",
				"DATABASE_USER": `runtime' user\`, "DATABASE_DBNAME": "sub2api \t' db",
				"DATABASE_PASSWORD": tc.password, "DATABASE_SSLROOTCERT": tc.rootCert,
			}
			connector, err := databaseConnector(func(key string) string { return env[key] })
			require.NoError(t, err)
			params := url.Values{
				"host": {env["DATABASE_HOST"]}, "port": {"5432"},
				"user": {env["DATABASE_USER"]}, "dbname": {env["DATABASE_DBNAME"]},
				"sslmode": {"verify-full"}, "connect_timeout": {"10"},
			}
			if tc.password != "" {
				params.Set("password", tc.password)
			}
			if tc.rootCert != "" {
				params.Set("sslrootcert", tc.rootCert)
			}
			// Compare the real driver's parsed options without opening a socket.
			reference := "postgresql:///?" + params.Encode()
			want, err := pq.NewConnector(reference)
			require.NoError(t, err)
			require.Equal(t, want, connector)
		})
	}
}

func TestDatabaseConnectorRawDSNCompatibility(t *testing.T) {
	for _, dsn := range []string{
		" host=localhost port=5432 user=runtime password='raw\\' password' dbname=sub2api sslmode=disable application_name='raw dsn' ",
		"postgresql://runtime:raw%20%27%5C@localhost:5432/sub2api?sslmode=disable&connect_timeout=3&application_name=raw-dsn",
	} {
		t.Run(dsn[:10], func(t *testing.T) {
			connector, err := databaseConnector(func(key string) string {
				if key == "DATABASE_DSN" {
					return dsn
				}
				t.Fatalf("DATABASE_DSN must take precedence over %s", key)
				return ""
			})
			require.NoError(t, err)
			want, err := pq.NewConnector(dsn)
			require.NoError(t, err)
			require.Equal(t, want, connector)
		})
	}
}

func TestDatabaseConnectorSpecialPassword(t *testing.T) {
	dsn := os.Getenv("CLOUD_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("set CLOUD_TEST_DATABASE_DSN to run real PostgreSQL credential quoting tests")
	}
	adminConnector, err := databaseConnector(func(key string) string {
		if key == "DATABASE_DSN" {
			return dsn
		}
		return ""
	})
	require.NoError(t, err)
	admin := sql.OpenDB(adminConnector)
	defer func() { _ = admin.Close() }()
	var name string
	require.NoError(t, admin.QueryRow("SELECT current_database()").Scan(&name))
	var random [16]byte
	_, err = rand.Read(random[:])
	require.NoError(t, err)
	// Test identities are unique and only created in the explicitly supplied DB.
	role := "cloud_cli_" + hex.EncodeToString(random[:6])
	password := "quotes' spaces and slash\\" + hex.EncodeToString(random[:])
	_, err = admin.Exec("CREATE ROLE " + role + " LOGIN PASSWORD '" + strings.ReplaceAll(password, "'", "''") + "'")
	require.NoError(t, err)
	defer func() { _, _ = admin.Exec("DROP ROLE " + role) }()
	connector, err := databaseConnector(func(key string) string {
		return map[string]string{
			"DATABASE_HOST": "127.0.0.1", "DATABASE_PORT": "5432", "DATABASE_USER": role,
			"DATABASE_PASSWORD": password, "DATABASE_DBNAME": name, "DATABASE_SSLMODE": "disable",
		}[key]
	})
	require.NoError(t, err)
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	require.NoError(t, db.PingContext(context.Background()))
}

func TestDatabaseConnectorDefaultsToVerifiedTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	connector, err := databaseConnector(func(key string) string {
		return map[string]string{
			"DATABASE_HOST": host, "DATABASE_PORT": port,
			"DATABASE_USER": "runtime", "DATABASE_DBNAME": "sub2api",
		}[key]
	})
	require.NoError(t, err)
	packet := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			packet <- nil
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		first := make([]byte, 8)
		if _, err := io.ReadFull(conn, first); err != nil {
			packet <- nil
			return
		}
		packet <- first
		_, _ = conn.Write([]byte{'N'}) // Explicitly refuse SSL.
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = connector.Connect(ctx)
	require.ErrorContains(t, err, "SSL is not enabled")
	select {
	case first := <-packet:
		// PostgreSQL SSLRequest: length=8, request code=80877103.
		require.Equal(t, []byte{0, 0, 0, 8, 4, 210, 22, 47}, first)
	case <-ctx.Done():
		t.Fatal("did not receive the PostgreSQL TLS handshake")
	}
}
