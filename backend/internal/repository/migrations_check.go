package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/migrations"
)

// CheckMigrations verifies the embedded migration manifest using SELECT only.
// It never creates tracking tables, acquires migration locks, or changes schema.
// Additional database migrations are allowed for expand/contract deployments;
// the deployment pipeline remains responsible for application compatibility.
func CheckMigrations(ctx context.Context, db *sql.DB) error {
	return checkMigrationsFS(ctx, db, migrations.FS)
}

func checkMigrationsFS(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	if db == nil {
		return errors.New("nil sql db")
	}
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}

	rows, err := db.QueryContext(ctx, "SELECT filename, checksum FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("read schema migration manifest (run the migrate command before starting runtime instances): %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[string]string)
	for rows.Next() {
		var name, checksum string
		if err := rows.Scan(&name, &checksum); err != nil {
			return fmt.Errorf("read migration manifest row: %w", err)
		}
		applied[name] = checksum
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read migration manifest: %w", err)
	}

	for _, name := range files {
		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		normalized := strings.TrimSpace(string(content))
		if normalized == "" {
			continue
		}
		existing, ok := applied[name]
		if !ok {
			return fmt.Errorf("migration %s is pending; run the migrate command before starting runtime instances", name)
		}
		sum := sha256.Sum256([]byte(normalized))
		checksum := hex.EncodeToString(sum[:])
		if existing != checksum && !isMigrationChecksumCompatible(name, existing, checksum) {
			return fmt.Errorf("migration %s checksum mismatch (db=%s file=%s)", name, existing, checksum)
		}
	}
	return nil
}

// initializeSchema keeps runtime check mode separate from migration execution.
// The check branch must remain safe for a role with SELECT-only privileges on
// schema_migrations and no DDL permissions.
func initializeSchema(ctx context.Context, db *sql.DB, mode string) error {
	switch mode {
	case config.DatabaseMigrationModeAuto:
		return ApplyMigrations(ctx, db)
	case config.DatabaseMigrationModeCheck:
		return CheckMigrations(ctx, db)
	default:
		return fmt.Errorf("unsupported database migration mode %q", mode)
	}
}
