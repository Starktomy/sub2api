package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierResourcePostgresOwnershipCAS(t *testing.T) {
	socket := os.Getenv("SUPPLIER_TEST_PG_SOCKET")
	if socket == "" {
		t.Skip("disposable local PostgreSQL not configured")
	}
	require.Contains(t, socket, "/tmp/supplier-pg-")
	db, e := sql.Open("postgres", "host="+socket+" port=55439 user=postgres dbname=postgres sslmode=disable")
	require.NoError(t, e)
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("supplier_resource_%d", time.Now().UnixNano())
	_, e = db.Exec(`CREATE SCHEMA ` + schema + `; SET search_path TO ` + schema)
	require.NoError(t, e)
	defer db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, e = db.Exec(`CREATE TABLE supplier_profiles(id BIGINT PRIMARY KEY); INSERT INTO supplier_profiles VALUES(1),(2); CREATE TABLE accounts(id BIGINT PRIMARY KEY,platform TEXT,credentials JSONB,deleted_at TIMESTAMPTZ)`)
	require.NoError(t, e)
	m, e := os.ReadFile("../../migrations/254_supplier_resources.sql")
	require.NoError(t, e)
	_, e = db.Exec(string(m))
	require.NoError(t, e)
	_, e = db.Exec(string(m))
	require.NoError(t, e)
	probeMigration, e := os.ReadFile("../../migrations/255_supplier_probe_jobs.sql")
	require.NoError(t, e)
	_, e = db.Exec(string(probeMigration))
	require.NoError(t, e)
	admissionMigration, e := os.ReadFile("../../migrations/256_supplier_admission.sql")
	require.NoError(t, e)
	_, e = db.Exec(string(admissionMigration))
	require.NoError(t, e)
	r := &supplierResourceRepository{db}
	v := &service.SupplierResource{ID: "00000000-0000-4000-8000-000000000001", SupplierID: 1, DisplayName: "name", MaskedEmail: "u***@example.test", PlanType: "plus", Envelope: []byte("ciphertext-only"), KeyID: "key-v1", AccountHint: "account-a", IdentityFingerprint: "identity-a", TokenFingerprint: "token-a", TokenExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, r.Create(ctx, 1, v))
	_, e = r.Get(ctx, 2, v.ID)
	require.ErrorIs(t, e, service.ErrSupplierResource)
	_, e = r.Transition(ctx, 2, v.ID, 1, true)
	require.ErrorIs(t, e, service.ErrSupplierResource)
	current, e := r.Get(ctx, 1, v.ID)
	require.NoError(t, e)
	require.Equal(t, int64(1), current.Version)
	_, e = r.Transition(ctx, 1, v.ID, 9, true)
	require.ErrorIs(t, e, service.ErrSupplierResourceVersion)
	v2 := *v
	v2.ID = "00000000-0000-4000-8000-000000000002"
	require.ErrorIs(t, r.Create(ctx, 2, &v2), service.ErrSupplierResourceConflict)
	dis, e := r.Transition(ctx, 1, v.ID, 1, true)
	require.NoError(t, e)
	require.Empty(t, dis.Envelope)
	require.Equal(t, "disconnected", dis.Status)
	require.ErrorIs(t, r.Create(ctx, 2, &v2), service.ErrSupplierResourceConflict)
	require.ErrorIs(t, r.Create(ctx, 1, &v2), service.ErrSupplierResourceConflict)
	reimport, e := r.FindImportResource(ctx, 1, v.IdentityFingerprint)
	require.NoError(t, e)
	require.Equal(t, v.ID, reimport.ID)
	reimport.Envelope = []byte("new-ciphertext")
	stale := *reimport
	require.NoError(t, r.Create(ctx, 1, reimport))
	require.Equal(t, int64(3), reimport.Version)
	require.Equal(t, int64(2), reimport.CredentialVersion)
	require.ErrorIs(t, r.Create(ctx, 1, &stale), service.ErrSupplierResourceVersion)
	_, e = db.Exec(`INSERT INTO accounts VALUES(1,'openai','{"chatgpt_account_id":"platform-account"}',NULL)`)
	require.NoError(t, e)
	v3 := *v
	v3.ID = "00000000-0000-4000-8000-000000000003"
	v3.AccountHint = "platform-account"
	v3.IdentityFingerprint = "platform"
	require.ErrorIs(t, r.Create(ctx, 1, &v3), service.ErrSupplierResourceConflict)
	var credentials string
	require.NoError(t, db.QueryRow(`SELECT credentials::text FROM accounts WHERE id=1`).Scan(&credentials))
	require.NotContains(t, credentials, "access_token")
}
