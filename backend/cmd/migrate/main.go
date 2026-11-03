// Command migrate applies or checks the embedded PostgreSQL migrations without
// constructing the HTTP server, Redis client, setup wizard, or background jobs.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/lib/pq"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(parent context.Context, args []string, getenv func(string) string, out io.Writer) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(out)
	check := flags.Bool("check", false, "Verify the migration manifest with SELECT only; do not apply migrations")
	timeout := flags.Duration("timeout", 10*time.Minute, "Overall migration/check timeout")
	flags.Usage = func() {
		fmt.Fprintln(out, "Usage: migrate [-check] [-timeout 10m]")
		fmt.Fprintln(out, "Connection: DATABASE_DSN or DATABASE_HOST/PORT/USER/PASSWORD/DBNAME/SSLMODE/SSLROOTCERT.")
		fmt.Fprintln(out, "SSLMODE defaults to verify-full. Do not pass credentials on the command line.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments; use -help")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	connector, err := databaseConnector(getenv)
	if err != nil {
		return err
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	// The existing migration runner reserves a session for its advisory lock.
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	if *check {
		if err := repository.CheckMigrations(ctx, db); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "Schema migration manifest is up to date (read-only check).")
		return err
	}
	if err := repository.ApplyMigrations(ctx, db); err != nil {
		return err
	}
	if err := repository.CheckMigrations(ctx, db); err != nil {
		return fmt.Errorf("verify migrations after apply: %w", err)
	}
	_, err = fmt.Fprintln(out, "Migrations applied and manifest verified. No application bootstrap or background jobs were started.")
	return err
}

// databaseConnector keeps credentials out of command arguments and error text.
// Quoting follows libpq connection-string rules, including passwords containing
// spaces, quotes, and backslashes. TLS is strict by default; local tests must
// explicitly opt out through their connection settings.
func databaseConnector(getenv func(string) string) (*pq.Connector, error) {
	if dsn := getenv("DATABASE_DSN"); strings.TrimSpace(dsn) != "" {
		connector, err := pq.NewConnector(dsn)
		if err != nil {
			return nil, errors.New("invalid DATABASE_DSN; connection details omitted")
		}
		return connector, nil
	}
	get := func(name, fallback string) string {
		if value := getenv(name); value != "" {
			return value
		}
		return fallback
	}
	host := get("DATABASE_HOST", "")
	user := get("DATABASE_USER", "")
	name := get("DATABASE_DBNAME", "")
	if strings.TrimSpace(host) == "" || strings.TrimSpace(user) == "" || strings.TrimSpace(name) == "" {
		return nil, errors.New("DATABASE_HOST, DATABASE_USER, and DATABASE_DBNAME are required (or set DATABASE_DSN)")
	}
	port := get("DATABASE_PORT", "5432")
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return nil, errors.New("DATABASE_PORT must be between 1 and 65535")
	}
	sslmode := get("DATABASE_SSLMODE", "verify-full")
	switch sslmode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return nil, errors.New("DATABASE_SSLMODE must be disable, require, verify-ca, or verify-full")
	}
	database := config.DatabaseConfig{
		Host:        host,
		Port:        number,
		User:        user,
		Password:    getenv("DATABASE_PASSWORD"),
		DBName:      name,
		SSLMode:     sslmode,
		SSLRootCert: getenv("DATABASE_SSLROOTCERT"),
	}
	dsn := database.DSN() + " connect_timeout=10"
	connector, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, errors.New("invalid database connection settings; connection details omitted")
	}
	return connector, nil
}
