package repository

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierProbeDurablePostgres(t *testing.T) {
	socket := os.Getenv("SUPPLIER_TEST_PG_SOCKET")
	if socket == "" {
		t.Skip("disposable PG required")
	}
	require.Contains(t, socket, "/tmp/supplier-pg-")
	db, e := sql.Open("postgres", "host="+socket+" port=55439 user=postgres dbname=postgres sslmode=disable")
	require.NoError(t, e)
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, e = db.Exec(`CREATE SCHEMA supplier_probe_fixture; SET search_path TO supplier_probe_fixture;
 CREATE TABLE users(id BIGINT PRIMARY KEY,status TEXT NOT NULL DEFAULT 'active',deleted_at TIMESTAMPTZ);
 INSERT INTO users(id) VALUES(1);
 CREATE TABLE supplier_profiles(id BIGINT PRIMARY KEY,user_id BIGINT REFERENCES users(id),status TEXT NOT NULL DEFAULT 'active'); INSERT INTO supplier_profiles(id,user_id) VALUES(1,1);
 CREATE TABLE accounts(id BIGINT PRIMARY KEY);
 CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE groups(id BIGINT PRIMARY KEY,status TEXT,deleted_at TIMESTAMPTZ,platform TEXT)`)
	require.NoError(t, e)
	defer db.Exec(`DROP SCHEMA supplier_probe_fixture CASCADE`)
	for _, file := range []string{"254_supplier_resources.sql", "255_supplier_probe_jobs.sql", "255_supplier_probe_jobs.sql", "256_supplier_admission.sql", "258_supplier_supply_controls.sql", "260_supplier_identity_verification.sql", "261_supplier_resource_email.sql"} {
		m, e := os.ReadFile("../../migrations/" + file)
		require.NoError(t, e)
		_, e = db.Exec(string(m))
		require.NoError(t, e)
	}
	id := "00000000-0000-4000-8000-000000000001"
	_, e = db.Exec(`INSERT INTO supplier_resources(id,supplier_id,display_name,identity_fingerprint,account_hint,token_fingerprint,key_id,envelope,masked_email,token_expires_at,status,auto_sync_enabled,probe_state) VALUES($1,1,'name','fp','account','tok','k','cipher','masked',CURRENT_TIMESTAMP+INTERVAL '1 hour','paused',TRUE,'pending')`, id)
	require.NoError(t, e)
	r := &supplierProbeRepository{db}
	first, e := r.Claim(ctx, "00000000-0000-4000-8000-000000000011")
	require.NoError(t, e)
	require.NotNil(t, first)
	none, e := r.Claim(ctx, "00000000-0000-4000-8000-000000000012")
	require.NoError(t, e)
	require.Nil(t, none)
	_, e = db.Exec(`UPDATE supplier_probe_jobs SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second'`)
	require.NoError(t, e)
	second, e := r.Claim(ctx, "00000000-0000-4000-8000-000000000012")
	require.NoError(t, e)
	require.Greater(t, second.Generation, first.Generation)
	require.NoError(t, r.Finish(ctx, first, &service.SupplierResourceUsage{PhoneVerified: true, EmailMatched: true, PlanType: "old"}, "", time.Minute, false))
	var usage sql.NullString
	require.NoError(t, db.QueryRow(`SELECT usage::text FROM supplier_resources`).Scan(&usage))
	require.False(t, usage.Valid)
	// Credential rotation cannot accept an old claim.
	_, e = db.Exec(`UPDATE supplier_resources SET credential_version=credential_version+1`)
	require.NoError(t, e)
	require.NoError(t, r.Finish(ctx, second, &service.SupplierResourceUsage{PhoneVerified: true, EmailMatched: true, PlanType: "old"}, "", time.Minute, false))
	require.NoError(t, db.QueryRow(`SELECT usage::text FROM supplier_resources`).Scan(&usage))
	require.False(t, usage.Valid)
	_, e = db.Exec(`UPDATE supplier_resources SET credential_version=1`)
	require.NoError(t, e)
	require.NoError(t, r.Finish(ctx, second, nil, "unauthorized", time.Minute, true))
	var state string
	var enabled bool
	require.NoError(t, db.QueryRow(`SELECT probe_state,auto_sync_enabled FROM supplier_resources`).Scan(&state, &enabled))
	require.Equal(t, "reauthorization_required", state)
	require.False(t, enabled)
	_, e = db.Exec(`UPDATE supplier_resources SET auto_sync_enabled=TRUE,probe_state='pending'`)
	require.NoError(t, e)
	claim, e := r.Claim(ctx, "00000000-0000-4000-8000-000000000013")
	require.NoError(t, e)
	require.NotNil(t, claim)
	repo := &supplierResourceRepository{db}
	paused, e := repo.Transition(ctx, 1, id, claim.Resource.Version, false)
	require.NoError(t, e)
	require.True(t, paused.AutoSyncEnabled)
	require.False(t, paused.SupplyEnabled)
	require.NoError(t, r.Finish(ctx, claim, &service.SupplierResourceUsage{PhoneVerified: true, EmailMatched: true, PlanType: "stale"}, "", time.Minute, false))
	require.NoError(t, db.QueryRow(`SELECT usage::text FROM supplier_resources`).Scan(&usage))
	require.True(t, usage.Valid) // Pausing supply does not cancel the read-only probe.
	require.NoError(t, db.QueryRow(`SELECT supply_enabled FROM supplier_resources`).Scan(&enabled))
	require.False(t, enabled)
	_, e = db.Exec(`UPDATE supplier_probe_jobs SET next_run_at=CURRENT_TIMESTAMP`)
	require.NoError(t, e)
	current, e := repo.Get(ctx, 1, id)
	require.NoError(t, e)
	resumed, e := r.Resume(ctx, 1, id, current.Version)
	require.NoError(t, e)
	require.True(t, resumed.AutoSyncEnabled)
	claim2, e := r.Claim(ctx, "00000000-0000-4000-8000-000000000014")
	require.NoError(t, e)
	require.NotNil(t, claim2)
	require.NoError(t, r.Finish(ctx, claim2, nil, "rate_limited", 2*time.Minute, false))
	var wait bool
	require.NoError(t, db.QueryRow(`SELECT next_probe_at>CURRENT_TIMESTAMP+INTERVAL '100 seconds' FROM supplier_resources`).Scan(&wait))
	require.True(t, wait)
	_, e = db.Exec(`UPDATE supplier_probe_jobs SET next_run_at=CURRENT_TIMESTAMP`)
	require.NoError(t, e)
	claim3, e := r.Claim(ctx, "00000000-0000-4000-8000-000000000015")
	require.NoError(t, e)
	require.NotNil(t, claim3)
	require.NoError(t, r.Finish(ctx, claim3, &service.SupplierResourceUsage{PhoneVerified: true, EmailMatched: true, PlanType: "plus", FetchedAt: time.Now().Unix()}, "", 10*time.Minute, false))
	require.NoError(t, db.QueryRow(`SELECT probe_state FROM supplier_resources`).Scan(&state))
	require.Equal(t, "fresh", state)
	_, e = db.Exec(`UPDATE supplier_resources SET token_expires_at=CURRENT_TIMESTAMP-INTERVAL '1 second'`)
	require.NoError(t, e)
	require.NoError(t, r.Reconcile(ctx))
	require.NoError(t, db.QueryRow(`SELECT probe_state,auto_sync_enabled FROM supplier_resources`).Scan(&state, &enabled))
	require.Equal(t, "reauthorization_required", state)
	require.False(t, enabled)
}
