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
	for _, file := range []string{"001_init.sql", "253_supplier_profiles.sql", "254_supplier_resources.sql", "255_supplier_probe_jobs.sql", "036_scheduler_outbox.sql", "152_scheduler_outbox_dedup_key.sql", "153_scheduler_outbox_pending_dedup_key_index_notx.sql", "256_supplier_admission.sql", "256_supplier_admission.sql"} {
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
		_, e := db.Exec(`UPDATE supplier_resources SET probe_state='fresh',probe_error_code=NULL,usage='{}',last_probe_at=CURRENT_TIMESTAMP,last_usage_at=CURRENT_TIMESTAMP`)
		require.NoError(t, e)
	}
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
	for _, tc := range []struct{ name, invalidate, restore string }{
		{"policy version zero", `UPDATE settings SET value=jsonb_set(value::jsonb,'{version}','0')::text`, `UPDATE settings SET value=jsonb_set(value::jsonb,'{version}','1')::text`},
		{"future probe", `UPDATE supplier_resources SET last_probe_at=CURRENT_TIMESTAMP+INTERVAL '1 hour'`, `UPDATE supplier_resources SET last_probe_at=last_usage_at`},
		{"future usage", `UPDATE supplier_resources SET last_usage_at=CURRENT_TIMESTAMP+INTERVAL '1 hour'`, `UPDATE supplier_resources SET last_usage_at=last_probe_at`},
		{"wrong account metadata", `UPDATE accounts SET credentials=jsonb_set(credentials,'{chatgpt_account_id}','"wrong-account"')`, `UPDATE accounts SET credentials=jsonb_set(credentials,'{chatgpt_account_id}','"account-metadata"')`},
		{"stale probe", `UPDATE supplier_resources SET last_probe_at=CURRENT_TIMESTAMP-INTERVAL '16 minutes'`, `UPDATE supplier_resources SET last_probe_at=last_usage_at`},
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
		{"rotated unverified", `UPDATE supplier_resources SET credential_version=credential_version+1`, `UPDATE supplier_resources SET probe_state='fresh',usage='{}',last_probe_at=CURRENT_TIMESTAMP,last_usage_at=CURRENT_TIMESTAMP`},
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
	_, err = db.Exec(`UPDATE supplier_account_bindings SET supplier_id=2`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE supplier_resources SET status='disconnected',envelope=NULL`)
	require.NoError(t, err)
	check(true, 2) // Deliberately stale scheduler/account eligibility.
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
