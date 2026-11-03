package repository

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Opt-in disposable local PostgreSQL only; never applies migrations to a managed DB.
func TestSupplierPostgresConcurrency(t *testing.T) {
	socket := os.Getenv("SUPPLIER_TEST_PG_SOCKET")
	if socket == "" {
		t.Skip("disposable local PostgreSQL not configured")
	}
	require.Contains(t, socket, "/tmp/supplier-pg-")
	db, err := sql.Open("postgres", "host="+socket+" port=55439 user=postgres dbname=postgres sslmode=disable")
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(3)
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `CREATE TABLE users(id BIGINT PRIMARY KEY,email TEXT,status TEXT,deleted_at TIMESTAMPTZ); INSERT INTO users(id,email,status) VALUES(7,'s@example.com','active'),(8,'other@example.com','active')`)
	require.NoError(t, err)
	defer db.ExecContext(ctx, `DROP TABLE supplier_profiles; DROP TABLE users`)
	migration, err := os.ReadFile("../../migrations/253_supplier_profiles.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	// Forward migrations are required to be repeatable.
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	r := &supplierRepository{db}
	var wg sync.WaitGroup
	results := make(chan *service.SupplierProfile, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := r.Enroll(ctx, 7, "s@example.com", "name")
			results <- p
			failures <- e
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		require.NoError(t, e)
	}
	for p := range results {
		require.Equal(t, int64(1), p.Version)
		require.Equal(t, int64(7), p.UserID)
	}
	p, err := r.GetProfile(ctx, 7)
	require.NoError(t, err)
	var won, lost int
	var mu sync.Mutex
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := r.UpdateProfile(ctx, 7, "edit", p.Version)
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				won++
			} else if e == service.ErrSupplierVersion {
				lost++
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, won)
	require.Equal(t, 1, lost)
	_, err = r.GetProfile(ctx, 8)
	require.ErrorIs(t, err, service.ErrSupplierNotEnrolled)
	_, err = r.UpdateProfile(ctx, 8, "attack", 1)
	require.ErrorIs(t, err, service.ErrSupplierNotEnrolled)
	_, err = db.ExecContext(ctx, `UPDATE users SET email='changed@example.com' WHERE id=7`)
	require.NoError(t, err)
	_, err = r.GetProfile(ctx, 7)
	require.ErrorIs(t, err, service.ErrSupplierEmailChanged)
	_, err = r.Enroll(ctx, 7, "s@example.com", "name")
	require.ErrorIs(t, err, service.ErrSupplierEmailChanged)
	renewed, err := r.Enroll(ctx, 7, "changed@example.com", "new")
	require.NoError(t, err)
	require.Equal(t, int64(3), renewed.Version)
	require.Equal(t, "changed@example.com", renewed.VerifiedEmail)
}
