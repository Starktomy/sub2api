package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestCheckMigrationsReadOnly(t *testing.T) {
	content := "CREATE TABLE example (id BIGINT PRIMARY KEY);"
	sum := sha256.Sum256([]byte(content))
	checksum := hex.EncodeToString(sum[:])
	fsys := fstest.MapFS{
		"001_example.sql": {Data: []byte("\n" + content + "\n")},
		"002_empty.sql":   {Data: []byte(" \n")},
	}

	for _, tc := range []struct {
		name      string
		rows      *sqlmock.Rows
		queryErr  error
		wantError string
	}{
		{name: "fully applied", rows: sqlmock.NewRows([]string{"filename", "checksum"}).AddRow("001_example.sql", checksum)},
		{name: "future additive migrations allowed", rows: sqlmock.NewRows([]string{"filename", "checksum"}).AddRow("001_example.sql", checksum).AddRow("003_future.sql", "future-checksum")},
		{name: "pending", rows: sqlmock.NewRows([]string{"filename", "checksum"}), wantError: "001_example.sql is pending"},
		{name: "checksum mismatch", rows: sqlmock.NewRows([]string{"filename", "checksum"}).AddRow("001_example.sql", "changed"), wantError: "checksum mismatch"},
		{name: "missing manifest", queryErr: &pq.Error{Code: "42P01"}, wantError: "run the migrate command"},
		{name: "permission denied", queryErr: &pq.Error{Code: "42501"}, wantError: "read schema migration manifest"},
		{name: "row read error", rows: sqlmock.NewRows([]string{"filename", "checksum"}).AddRow("001_example.sql", checksum).RowError(0, errors.New("connection lost")), wantError: "connection lost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			expect := mock.ExpectQuery("^SELECT filename, checksum FROM schema_migrations$")
			if tc.queryErr != nil {
				expect.WillReturnError(tc.queryErr)
			} else {
				expect.WillReturnRows(tc.rows).RowsWillBeClosed()
			}
			err = checkMigrationsFS(context.Background(), db, fsys)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
			} else {
				require.NoError(t, err)
			}
			// Any DDL, bootstrap write, transaction, or advisory lock would be an
			// unexpected operation. Runtime checking must remain SELECT-only.
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCheckMigrationsChecksumCompatibility(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	const name = "054_drop_legacy_cache_columns.sql"
	content, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	mock.ExpectQuery("^SELECT filename, checksum FROM schema_migrations$").
		WillReturnRows(sqlmock.NewRows([]string{"filename", "checksum"}).AddRow(name, "182c193f3359946cf094090cd9e57d5c3fd9abaffbc1e8fc378646b8a6fa12b4")).
		RowsWillBeClosed()
	require.NoError(t, checkMigrationsFS(context.Background(), db, fstest.MapFS{name: {Data: content}}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckMigrationsNilDatabase(t *testing.T) {
	require.ErrorContains(t, CheckMigrations(context.Background(), nil), "nil sql db")
}

func TestInitializeSchemaInvalidModeDoesNotQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.ErrorContains(t, initializeSchema(context.Background(), db, "skip"), "unsupported")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInitEntCheckRequiresConfiguredSecretsBeforeConnecting(t *testing.T) {
	// A check-only runtime must not fall back to persistent secret generation,
	// even when its database is unavailable. Configuration fails first.
	cfg := &config.Config{Database: config.DatabaseConfig{MigrationMode: config.DatabaseMigrationModeCheck}}
	client, db, err := InitEnt(cfg)
	require.Error(t, err)
	require.ErrorContains(t, err, "check mode does not bootstrap secrets")
	require.Nil(t, client)
	require.Nil(t, db)
}

func TestInitEntPostgresCheckHasNoBootstrapWrites(t *testing.T) {
	dsn := os.Getenv("CLOUD_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("set CLOUD_TEST_DATABASE_DSN to test runtime initialization with a SELECT-only role")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	var random [16]byte
	_, err = rand.Read(random[:])
	require.NoError(t, err)
	role := "cloud_manifest_" + hex.EncodeToString(random[:6])
	password := hex.EncodeToString(random[:])
	_, err = admin.Exec("CREATE ROLE " + role + " LOGIN PASSWORD '" + password + "'")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP OWNED BY " + role)
		_, _ = admin.Exec("DROP ROLE " + role)
	})
	_, err = admin.Exec("GRANT USAGE ON SCHEMA public TO " + role)
	require.NoError(t, err)
	_, err = admin.Exec("GRANT SELECT ON schema_migrations TO " + role)
	require.NoError(t, err)
	var name string
	require.NoError(t, admin.QueryRow("SELECT current_database()").Scan(&name))

	// The connection is deliberately limited to the manifest. It cannot read or
	// modify application secrets, users, seeds, or execute schema migrations.
	t.Setenv("DATABASE_HOST", "127.0.0.1")
	t.Setenv("DATABASE_PORT", "5432")
	t.Setenv("DATABASE_USER", role)
	t.Setenv("DATABASE_PASSWORD", password)
	t.Setenv("DATABASE_DBNAME", name)
	t.Setenv("DATABASE_SSLMODE", "disable")
	t.Setenv("DATABASE_MIGRATION_MODE", "check")
	t.Setenv("RUNTIME_ROLE", "all")
	t.Setenv("JWT_SECRET", strings.Repeat("j", 64))
	t.Setenv("TOTP_ENCRYPTION_KEY", strings.Repeat("a", 64))
	t.Setenv("TZ", "UTC")
	cfg, err := config.LoadForBootstrap()
	require.NoError(t, err)
	client, db, err := InitEnt(cfg)
	require.NoError(t, err, "runtime initialization must only read the manifest")
	t.Cleanup(func() { _ = client.Close() })
	_, err = db.Exec("INSERT INTO security_secrets (key, value) VALUES ('must-not-write', 'must-not-write')")
	require.Error(t, err, "the runtime test role must not have write privileges")
	var pgErr *pq.Error
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "42501", string(pgErr.Code))
}

func TestCheckMigrationsPostgresReadOnly(t *testing.T) {
	dsn := os.Getenv("CLOUD_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("set CLOUD_TEST_DATABASE_DSN to run the real PostgreSQL read-only contract")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	require.NoError(t, db.PingContext(context.Background()))
	_, err = db.Exec("SET default_transaction_read_only = on")
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE cloud_check_must_not_write (id BIGINT)")
	require.Error(t, err, "the test session must actually reject writes")
	var pgErr *pq.Error
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "25006", string(pgErr.Code))
	require.NoError(t, CheckMigrations(context.Background(), db), "schema verification must work in a read-only session")
}
