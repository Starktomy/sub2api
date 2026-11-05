package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Opt-in disposable PostgreSQL only: no credentials, external network or upstream calls.
func TestSupplierAdmissionPostgres(t *testing.T) {
	socket := os.Getenv("SUPPLIER_TEST_PG_SOCKET")
	if socket == "" {
		t.Skip("disposable PG required")
	}
	require.Contains(t, socket, "/tmp/supplier-pg-")
	db, err := sql.Open("postgres", "host="+socket+" port=55439 user=postgres dbname=postgres sslmode=disable")
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("admission_%d", time.Now().UnixNano())
	_, err = db.Exec(`CREATE SCHEMA ` + schema + `; SET search_path TO ` + schema)
	require.NoError(t, err)
	defer db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	// Core tables and supplier tables use actual migration definitions. Only the
	// later independently-added Account/group columns are supplied explicitly.
	for _, file := range []string{"001_init.sql", "253_supplier_profiles.sql", "254_supplier_resources.sql", "255_supplier_probe_jobs.sql", "036_scheduler_outbox.sql", "152_scheduler_outbox_dedup_key.sql", "153_scheduler_outbox_pending_dedup_key_index_notx.sql", "256_supplier_admission.sql", "258_supplier_supply_controls.sql", "258_supplier_supply_controls.sql"} {
		b, e := os.ReadFile("../../migrations/" + file)
		require.NoError(t, e)
		_, e = db.Exec(string(b))
		require.NoError(t, e, file)
	}
	_, err = db.Exec(`ALTER TABLE accounts ADD COLUMN schedulable BOOLEAN NOT NULL DEFAULT TRUE, ADD COLUMN expires_at TIMESTAMPTZ, ADD COLUMN auto_pause_on_expired BOOLEAN NOT NULL DEFAULT TRUE;
 ALTER TABLE groups ADD COLUMN platform VARCHAR(50) NOT NULL DEFAULT 'openai';
 CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 INSERT INTO users(id,email,password_hash) VALUES(1,'supplier@example.invalid','not-a-password');
 INSERT INTO supplier_profiles(id,user_id,display_name,verified_email) VALUES(1,1,'supplier','supplier@example.invalid');
 INSERT INTO groups(id,name,platform) VALUES(1,'openai','openai'),(2,'composite','composite');
 INSERT INTO settings VALUES('supplier_operating_policy_v1','{"version":1,"auto_admission":true,"group_ids":[1],"fee_mode":"standard_cost","fee_ratio_bps":1000}');
 INSERT INTO supplier_resources(id,supplier_id,display_name,identity_fingerprint,account_hint,token_fingerprint,key_id,envelope,masked_email,token_expires_at,status,auto_sync_enabled) VALUES('00000000-0000-4000-8000-000000000001',1,'resource','fp','account-metadata','token-fp','key','encrypted','masked',CURRENT_TIMESTAMP+INTERVAL '1 hour','paused',TRUE)`)
	require.NoError(t, err)
	ctx := context.Background()
	reconcile := func() { t.Helper(); require.NoError(t, reconcileSupplierAdmission(ctx, db)) }
	check := func(want bool, group int64) {
		t.Helper()
		var enabled bool
		var count int
		require.NoError(t, db.QueryRow(`SELECT schedulable FROM accounts`).Scan(&enabled))
		require.Equal(t, want, enabled)
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM account_groups WHERE group_id=$1`, group).Scan(&count))
		if want {
			require.Equal(t, 1, count)
		} else {
			require.Zero(t, count)
		}
	}
	reconcile()
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&count))
	require.Zero(t, count)
	verify := func() {
		t.Helper()
		_, e := db.Exec(`UPDATE supplier_resources SET probe_state='fresh',probe_error_code=NULL,usage=jsonb_build_object('rate_limit',jsonb_build_object('allowed',true,'limit_reached',false,'primary_window',jsonb_build_object('used_percent',10,'limit_window_seconds',18000,'reset_at',extract(epoch FROM CURRENT_TIMESTAMP+INTERVAL '1 hour')::bigint),'secondary_window',jsonb_build_object('used_percent',20,'limit_window_seconds',604800,'reset_at',extract(epoch FROM CURRENT_TIMESTAMP+INTERVAL '1 day')::bigint))),last_probe_at=CURRENT_TIMESTAMP,last_usage_at=CURRENT_TIMESTAMP`)
		require.NoError(t, e)
	}
	verify()
	reconcile()
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&count))
	require.Zero(t, count, "verified legacy resources must explicitly reauthorize")
	_, err = db.Exec(`UPDATE supplier_resources SET consent_version='all_available_continuous_v1',authorization_version=1,authorization_expires_at=token_expires_at,supply_enable_pending=TRUE`)
	require.NoError(t, err)
	verify()
	reconcile()
	check(true, 1)
	var account int64
	require.NoError(t, db.QueryRow(`SELECT account_id FROM supplier_account_bindings`).Scan(&account))
	var secretFree bool
	require.NoError(t, db.QueryRow(`SELECT credentials ? 'supplier_resource_id' AND credentials ? 'chatgpt_account_id' AND NOT credentials ? 'access_token' AND NOT credentials ? 'refresh_token' FROM accounts`).Scan(&secretFree))
	require.True(t, secretFree)
	resourceRepo := &supplierResourceRepository{db: db}
	resourceID := "00000000-0000-4000-8000-000000000001"
	assertExecution := func(allowed bool) {
		t.Helper()
		resource, e := resourceRepo.GetExecutionResource(ctx, account, resourceID)
		if allowed {
			require.NoError(t, e)
			require.NotNil(t, resource)
			require.Equal(t, "account-metadata", resource.AccountHint)
		} else {
			require.ErrorIs(t, e, service.ErrSupplierResource)
			require.Nil(t, resource)
		}
	}
	assertExecution(true)
	_, groupErr := resourceRepo.GetExecutionResource(context.WithValue(ctx, ctxkey.Group, &service.Group{ID: 999}), account, resourceID)
	require.ErrorIs(t, groupErr, service.ErrSupplierResource)
	_, groupErr = resourceRepo.GetExecutionResource(context.WithValue(ctx, ctxkey.Group, &service.Group{ID: 1}), account, resourceID)
	require.NoError(t, groupErr)
	var goodQuota string
	require.NoError(t, db.QueryRow(`SELECT usage::text FROM supplier_resources`).Scan(&goodQuota))
	for _, tc := range []struct{ name, invalidate, restore string }{
		{"authorization expiry", `UPDATE supplier_resources SET authorization_version=authorization_version+1,authorization_expires_at=CURRENT_TIMESTAMP+INTERVAL '1 microsecond'`, `UPDATE supplier_resources SET authorization_version=authorization_version+1,authorization_expires_at=token_expires_at`},
		{"supplier pause", `UPDATE supplier_resources SET supply_enabled=FALSE`, `UPDATE supplier_resources SET supply_enabled=TRUE`},
		{"admin prohibition", `UPDATE supplier_resources SET admin_disabled=TRUE`, `UPDATE supplier_resources SET admin_disabled=FALSE`},
		{"limit reached", `UPDATE supplier_resources SET usage=jsonb_set(usage,'{rate_limit,limit_reached}','true')`, `UPDATE supplier_resources SET usage=jsonb_set(usage,'{rate_limit,limit_reached}','false')`},
		{"used one hundred", `UPDATE supplier_resources SET usage=jsonb_set(usage,'{rate_limit,primary_window,used_percent}','100')`, `UPDATE supplier_resources SET usage=jsonb_set(usage,'{rate_limit,primary_window,used_percent}','10')`},
		{"negative quota", `UPDATE supplier_resources SET usage=jsonb_set(usage,'{rate_limit,primary_window,used_percent}','-1')`, `UPDATE supplier_resources SET usage=jsonb_set(usage,'{rate_limit,primary_window,used_percent}','10')`},
		{"unknown quota", `UPDATE supplier_resources SET usage='{}'`, `UPDATE supplier_resources SET usage='` + goodQuota + `'::jsonb`},
		{"policy version zero", `UPDATE settings SET value=jsonb_set(value::jsonb,'{version}','0')::text`, `UPDATE settings SET value=jsonb_set(value::jsonb,'{version}','1')::text`},
		{"future probe", `UPDATE supplier_resources SET last_verified_at=CURRENT_TIMESTAMP+INTERVAL '1 hour'`, `UPDATE supplier_resources SET last_verified_at=last_usage_at`},
		{"future usage", `UPDATE supplier_resources SET last_usage_at=CURRENT_TIMESTAMP+INTERVAL '1 hour'`, `UPDATE supplier_resources SET last_usage_at=last_probe_at`},
		{"wrong account metadata", `UPDATE accounts SET credentials=jsonb_set(credentials,'{chatgpt_account_id}','"wrong-account"')`, `UPDATE accounts SET credentials=jsonb_set(credentials,'{chatgpt_account_id}','"account-metadata"')`},
		{"stale probe", `UPDATE supplier_resources SET last_verified_at=CURRENT_TIMESTAMP-INTERVAL '16 minutes'`, `UPDATE supplier_resources SET last_verified_at=last_usage_at`},
	} {
		t.Run("execution rejects "+tc.name, func(t *testing.T) {
			_, e := db.Exec(tc.invalidate)
			require.NoError(t, e)
			assertExecution(false)
			// Account and scheduler snapshot have not been reconciled: execution
			// must reject from the current durable state alone.
			check(true, 1)
			_, e = db.Exec(tc.restore)
			require.NoError(t, e)
			assertExecution(true)
		})
	}
	reconcile()
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&count))
	require.Equal(t, 1, count)
	// Ordinary running probe preserves last successful eligibility; its eventual
	// terminal failure still blocks immediately, without waiting for reconciliation.
	probes := &supplierProbeRepository{db}
	claim, e := probes.Claim(ctx, "00000000-0000-4000-8000-000000000021")
	require.NoError(t, e)
	require.NotNil(t, claim)
	assertExecution(true)
	paused, e := resourceRepo.Transition(ctx, 1, resourceID, claim.Resource.Version, false)
	require.NoError(t, e)
	require.True(t, paused.AutoSyncEnabled)
	require.False(t, paused.SupplyEnabled)
	check(false, 1)
	assertExecution(false)
	require.NoError(t, probes.Finish(ctx, claim, claim.Resource.Usage, "", time.Minute, false))
	current, e := resourceRepo.Get(ctx, 1, resourceID)
	require.NoError(t, e)
	require.False(t, current.SupplyEnabled, "late verification cannot undo pause")
	_, e = resourceRepo.StartSupply(ctx, 2, resourceID, current.Version)
	require.ErrorIs(t, e, service.ErrSupplierResource)
	_, e = resourceRepo.StartSupply(ctx, 1, resourceID, current.Version-1)
	require.ErrorIs(t, e, service.ErrSupplierResourceVersion)
	started, e := resourceRepo.StartSupply(ctx, 1, resourceID, current.Version)
	require.NoError(t, e)
	require.True(t, started.SupplyEnabled)
	reconcile()
	check(true, 1)
	assertExecution(true)
	disabled, e := resourceRepo.AdminSupplyControl(ctx, resourceID, started.Version, true)
	require.NoError(t, e)
	require.Empty(t, disabled.Envelope)
	require.Empty(t, disabled.AccountHint)
	require.True(t, disabled.AdminDisabled)
	require.True(t, disabled.SupplyEnabled)
	check(false, 1)
	assertExecution(false)
	// Rotating under an admin prohibition cannot remove it or enable before verify.
	rotation, e := resourceRepo.Get(ctx, 1, resourceID)
	require.NoError(t, e)
	require.NoError(t, resourceRepo.Create(ctx, 1, rotation))
	require.True(t, rotation.AdminDisabled)
	require.False(t, rotation.SupplyEnabled)
	verify()
	reconcile()
	check(false, 1)
	current, e = resourceRepo.Get(ctx, 1, resourceID)
	require.NoError(t, e)
	require.True(t, current.SupplyEnabled)
	_, e = resourceRepo.AdminSupplyControl(ctx, resourceID, current.Version, false)
	require.NoError(t, e)
	reconcile()
	check(true, 1)
	assertExecution(true)
	// Read-only resume never grants a paused supplier choice or another authorization.
	current, e = resourceRepo.Get(ctx, 1, resourceID)
	require.NoError(t, e)
	paused, e = resourceRepo.Transition(ctx, 1, resourceID, current.Version, false)
	require.NoError(t, e)
	resumed, e := probes.Resume(ctx, 1, resourceID, paused.Version)
	require.NoError(t, e)
	require.False(t, resumed.SupplyEnabled)
	require.Equal(t, paused.AuthorizationVersion, resumed.AuthorizationVersion)
	verify()
	current, e = resourceRepo.Get(ctx, 1, resourceID)
	require.NoError(t, e)
	require.False(t, current.SupplyEnabled)
	_, e = resourceRepo.StartSupply(ctx, 1, resourceID, current.Version)
	require.NoError(t, e)
	reconcile()
	check(true, 1)
	_, err = db.Exec(`UPDATE settings SET value=jsonb_set(value::jsonb,'{group_ids}','[2]')::text`)
	require.NoError(t, err)
	reconcile()
	check(true, 2)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM account_groups WHERE group_id=1`).Scan(&count))
	require.Zero(t, count)
	for _, tc := range []struct{ name, disable, restore string }{
		{"stale", `UPDATE supplier_resources SET last_usage_at=CURRENT_TIMESTAMP-INTERVAL '16 minutes'`, `UPDATE supplier_resources SET last_usage_at=last_probe_at`},
		{"disabled owner", `UPDATE users SET status='disabled'`, `UPDATE users SET status='active'`},
		{"disabled policy", `UPDATE settings SET value=jsonb_set(value::jsonb,'{auto_admission}','false')::text`, `UPDATE settings SET value=jsonb_set(value::jsonb,'{auto_admission}','true')::text`},
		{"invalid group", `UPDATE groups SET platform='gemini' WHERE id=2`, `UPDATE groups SET platform='composite' WHERE id=2`},
		{"rotated unverified", `UPDATE supplier_resources SET credential_version=credential_version+1`, `UPDATE supplier_resources SET probe_state='fresh',usage=jsonb_build_object('rate_limit',jsonb_build_object('allowed',true,'limit_reached',false,'primary_window',jsonb_build_object('used_percent',10,'limit_window_seconds',18000,'reset_at',extract(epoch FROM CURRENT_TIMESTAMP+INTERVAL '1 hour')::bigint),'secondary_window',jsonb_build_object('used_percent',20,'limit_window_seconds',604800,'reset_at',extract(epoch FROM CURRENT_TIMESTAMP+INTERVAL '1 day')::bigint))),last_probe_at=CURRENT_TIMESTAMP,last_usage_at=CURRENT_TIMESTAMP`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, e := db.Exec(tc.disable)
			require.NoError(t, e)
			reconcile()
			check(false, 2)
			_, e = db.Exec(tc.restore)
			require.NoError(t, e)
			reconcile()
			check(true, 2)
		})
	}
	current, e = resourceRepo.Get(ctx, 1, resourceID)
	require.NoError(t, e)
	require.NoError(t, resourceRepo.RecordProbeFailure(ctx, 1, resourceID, current.CredentialVersion, "unauthorized", true))
	assertExecution(false)
	check(false, 2)
	require.NoError(t, resourceRepo.RecordProbeFailure(ctx, 1, resourceID, current.CredentialVersion, "upstream_unavailable", false))
	current, e = resourceRepo.Get(ctx, 1, resourceID)
	require.NoError(t, e)
	require.Equal(t, "reauthorization_required", current.ProbeState, "late transient failure cannot erase terminal unauthorized")
	require.NoError(t, e)
	_, e = resourceRepo.StartSupply(ctx, 1, resourceID, current.Version)
	require.ErrorIs(t, e, service.ErrSupplierSupplyUnavailable)
	require.NoError(t, resourceRepo.Create(ctx, 1, current))
	verify()
	reconcile()
	check(true, 2)
	_, err = db.Exec(`UPDATE supplier_account_bindings SET supplier_id=2`)
	require.Error(t, err)
	current, e = resourceRepo.Get(ctx, 1, resourceID)
	require.NoError(t, e)
	disconnected, e := resourceRepo.Transition(ctx, 1, resourceID, current.Version, true)
	require.NoError(t, e)
	require.NotNil(t, disconnected.AuthorizationRevokedAt)
	require.Empty(t, disconnected.Envelope)
	check(false, 2)
	var eventCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM supplier_authorization_events WHERE event='revoked'`).Scan(&eventCount))
	require.Equal(t, 1, eventCount)
	_, e = db.Exec(`UPDATE supplier_authorization_events SET consent_version=consent_version`)
	require.Error(t, e)
	_, e = db.Exec(`DELETE FROM supplier_authorization_events`)
	require.Error(t, e)
	assertExecution(false)
	reconcile()
	check(false, 2)
	var preserved int64
	require.NoError(t, db.QueryRow(`SELECT account_id FROM supplier_account_bindings`).Scan(&preserved))
	require.Equal(t, account, preserved)
	// Reconnect an admitted identity without creating another resource/Account.
	reimport, e := resourceRepo.FindImportResource(ctx, 1, "fp")
	require.NoError(t, e)
	require.Equal(t, resourceID, reimport.ID)
	reimport.Envelope = []byte("new-encrypted-envelope")
	reimport.TokenFingerprint = "new-token-fp"
	reimport.TokenExpiresAt = time.Now().Add(time.Hour)
	oldVersion, oldCredential := reimport.Version, reimport.CredentialVersion
	stale := *reimport
	// Only the exact binding is exempted; an unrelated legacy Account with the
	// same upstream identity must still block credential replacement.
	_, e = db.Exec(`INSERT INTO accounts(id,name,platform,type,credentials) VALUES(900,'legacy duplicate','openai','oauth','{"chatgpt_account_id":"account-metadata"}')`)
	require.NoError(t, e)
	require.ErrorIs(t, resourceRepo.Create(ctx, 1, reimport), service.ErrSupplierResourceConflict)
	_, e = db.Exec(`DELETE FROM accounts WHERE id=900`)
	require.NoError(t, e)
	require.NoError(t, resourceRepo.Create(ctx, 1, reimport))
	require.Equal(t, oldVersion+1, reimport.Version)
	require.Equal(t, oldCredential+1, reimport.CredentialVersion)
	require.ErrorIs(t, resourceRepo.Create(ctx, 1, &stale), service.ErrSupplierResourceVersion)
	assertExecution(false)
	reconcile()
	check(false, 2)
	var verified sql.NullInt64
	require.NoError(t, db.QueryRow(`SELECT verified_credential_version FROM supplier_resources`).Scan(&verified))
	require.False(t, verified.Valid)
	var queuedVersion int64
	var queuedState string
	require.NoError(t, db.QueryRow(`SELECT credential_version,state FROM supplier_probe_jobs`).Scan(&queuedVersion, &queuedState))
	require.Equal(t, reimport.CredentialVersion, queuedVersion)
	require.Equal(t, "pending", queuedState)
	verify()
	reconcile()
	check(true, 2)
	assertExecution(true)
	require.NoError(t, db.QueryRow(`SELECT account_id FROM supplier_account_bindings`).Scan(&preserved))
	require.Equal(t, account, preserved)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM supplier_resources`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM scheduler_outbox`).Scan(&count))
	require.Greater(t, count, 1)
}
