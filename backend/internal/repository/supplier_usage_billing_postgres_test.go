package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Opt-in disposable Unix-socket PostgreSQL ONLY. Every connection is scoped to
// this test's schema, allowing realistic concurrent workers without live data.
func supplierDispatchTestDB(t *testing.T) *sql.DB {
	t.Helper()
	socket := os.Getenv("SUPPLIER_TEST_PG_SOCKET")
	if socket == "" {
		t.Skip("disposable PG required")
	}
	require.Contains(t, socket, "/tmp/supplier-pg-")
	dsn := "host=" + socket + " port=55439 user=postgres dbname=postgres sslmode=disable"
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	admin.SetMaxOpenConns(1)
	schema := fmt.Sprintf("supplier_dispatch_%d", time.Now().UnixNano())
	_, err = admin.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn+" search_path="+schema)
	require.NoError(t, err)
	db.SetMaxOpenConns(3)
	t.Cleanup(func() { db.Close(); admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() })
	for _, file := range []string{"001_init.sql", "003_subscription.sql", "007_add_user_allowed_groups.sql", "012_add_user_subscription_soft_delete.sql", "032_add_api_key_ip_restriction.sql", "045_add_api_key_quota.sql", "064_add_api_key_rate_limits.sql", "071_add_usage_billing_dedup.sql", "073_add_usage_billing_dedup_archive.sql", "142_user_platform_quotas.sql", "184_auth_cache_invalidation_outbox.sql", "253_supplier_profiles.sql", "254_supplier_resources.sql", "255_supplier_probe_jobs.sql", "256_supplier_admission.sql", "257_supplier_usage.sql", "258_supplier_supply_controls.sql", "259_supplier_dispatch.sql", "259_supplier_dispatch.sql"} {
		b, e := os.ReadFile("../../migrations/" + file)
		require.NoError(t, e)
		_, e = db.Exec(string(b))
		require.NoError(t, e, file)
	}
	_, err = db.Exec(`ALTER TABLE accounts ADD COLUMN schedulable BOOLEAN NOT NULL DEFAULT TRUE,ADD COLUMN expires_at TIMESTAMPTZ,ADD COLUMN auto_pause_on_expired BOOLEAN NOT NULL DEFAULT TRUE;
 CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 INSERT INTO users(id,email,password_hash,balance) VALUES(1,'supplier@example.invalid','not-a-password',10),(2,'customer@example.invalid','not-a-password',10);
 INSERT INTO supplier_profiles(id,user_id,display_name,verified_email) VALUES(1,1,'supplier','supplier@example.invalid');
 INSERT INTO groups(id,name,platform) VALUES(1,'openai','openai');
 INSERT INTO api_keys(id,user_id,group_id,key,name) VALUES(1,2,1,'not-a-usable-key','fixture');
 INSERT INTO accounts(id,name,platform,type,credentials,extra) VALUES(1,'supplier','openai','oauth','{"supplier_resource_id":"00000000-0000-4000-8000-000000000001","chatgpt_account_id":"account-metadata"}','{"supplier_resource_id":"00000000-0000-4000-8000-000000000001","supplier_id":"1"}');
 INSERT INTO account_groups(account_id,group_id) VALUES(1,1);
 INSERT INTO supplier_resources(id,supplier_id,display_name,identity_fingerprint,account_hint,token_fingerprint,key_id,envelope,masked_email,token_expires_at,status,auto_sync_enabled,consent_version,authorization_version,authorization_expires_at,supply_enable_pending)
 VALUES('00000000-0000-4000-8000-000000000001',1,'resource','identity','account-metadata','token-fingerprint','key-version','encrypted-only','masked',CURRENT_TIMESTAMP+INTERVAL '1 hour','paused',TRUE,'all_available_continuous_v1',1,CURRENT_TIMESTAMP+INTERVAL '1 hour',TRUE);
 UPDATE supplier_resources SET probe_state='fresh',usage=jsonb_build_object('rate_limit',jsonb_build_object('allowed',true,'limit_reached',false,'primary_window',jsonb_build_object('used_percent',10,'limit_window_seconds',18000,'reset_at',extract(epoch FROM CURRENT_TIMESTAMP+INTERVAL '1 hour')::bigint),'secondary_window',jsonb_build_object('used_percent',20,'limit_window_seconds',604800,'reset_at',extract(epoch FROM CURRENT_TIMESTAMP+INTERVAL '1 day')::bigint))),last_probe_at=CURRENT_TIMESTAMP,last_usage_at=CURRENT_TIMESTAMP;
 INSERT INTO supplier_account_bindings(resource_id,account_id,supplier_id,credential_version) VALUES('00000000-0000-4000-8000-000000000001',1,1,1);
 INSERT INTO settings VALUES('supplier_operating_policy_v1','{"auto_admission":true,"group_ids":[1],"fee_mode":"standard_cost","fee_ratio_bps":7000,"version":9}')`)
	require.NoError(t, err)
	return db
}
func supplierDispatchTestContext() context.Context {
	ctx := context.WithValue(context.Background(), ctxkey.Group, &service.Group{ID: 1})
	return context.WithValue(ctx, ctxkey.UserID, int64(2))
}
func supplierDispatchTestRequest() service.SupplierDispatchRequest {
	return service.SupplierDispatchRequest{AttemptID: uuid.NewString(), RequestID: uuid.NewString(), ResourceID: "00000000-0000-4000-8000-000000000001", AccountID: 1, UserID: 2, APIKeyID: 1, GroupID: 1, Model: "gpt-5.2"}
}
func supplierDispatchTestObservation() service.SupplierUsageObservation {
	return service.SupplierUsageObservation{Usage: service.OpenAIUsage{InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 8}, UsagePresent: true, Complete: true, Source: "native_json", Terminal: "json", Model: "gpt-5.2"}
}
func supplierDispatchTestBilling(s service.SupplierDispatchSnapshot) *service.UsageBillingCommand {
	return &service.UsageBillingCommand{RequestID: "supplier:" + s.AttemptID, SupplierAttemptID: s.AttemptID, SupplierSnapshotDigest: s.Digest, APIKeyID: s.APIKeyID, UserID: s.UserID, AccountID: s.AccountID, AccountType: service.AccountTypeOAuth, Model: s.Model, SupplierCostProvided: true, StandardCost: 0.000078125, CustomerCharge: 0.2, BalanceCost: 0.2, InputTokens: 92, OutputTokens: 20, CacheReadTokens: 8}
}

func TestSupplierUsageBillingPostgres(t *testing.T) {
	db := supplierDispatchTestDB(t)
	r := &usageBillingRepository{db: db}
	ctx := supplierDispatchTestContext()
	req := supplierDispatchTestRequest()
	accepted, err := r.AcceptSupplierDispatch(ctx, req)
	require.NoError(t, err)
	require.Equal(t, int64(1), accepted.Resource.AccountID)
	require.Equal(t, []byte("encrypted-only"), accepted.Resource.Envelope)
	require.Equal(t, 7000, accepted.Snapshot.FeePolicy.RatioBPS)
	// A ratio update after acceptance cannot reprice this attempt.
	_, err = db.Exec(`UPDATE settings SET value=jsonb_set(jsonb_set(value::jsonb,'{fee_ratio_bps}','3000'),'{version}','10')::text`)
	require.NoError(t, err)
	require.NoError(t, r.MarkSupplierDispatched(ctx, req.AttemptID, accepted.Snapshot.Digest))
	require.ErrorIs(t, r.MarkSupplierDispatched(ctx, req.AttemptID, accepted.Snapshot.Digest), service.ErrSupplierDispatchConflict)
	o := supplierDispatchTestObservation()
	require.NoError(t, r.ObserveSupplierUsage(ctx, req.AttemptID, accepted.Snapshot.Digest, o))
	require.NoError(t, r.ObserveSupplierUsage(ctx, req.AttemptID, accepted.Snapshot.Digest, o))
	cmd := supplierDispatchTestBilling(accepted.Snapshot)
	require.NoError(t, r.StageSupplierBilling(ctx, cmd))
	require.NoError(t, r.StageSupplierBilling(ctx, cmd))
	changed := *cmd
	changed.StandardCost++
	require.ErrorIs(t, r.StageSupplierBilling(ctx, &changed), service.ErrSupplierDispatchConflict)
	// Rotate credentials and revoke authorization after observation. Billing uses
	// immutable acceptance, not the current binding or policy.
	_, err = db.Exec(`UPDATE supplier_resources SET credential_version=2,authorization_revoked_at=CURRENT_TIMESTAMP,supply_enabled=FALSE; UPDATE supplier_account_bindings SET credential_version=2; DELETE FROM settings`)
	require.NoError(t, err)
	// A failed supplier fact insert must roll back the debit, dedup and state.
	_, err = db.Exec(`CREATE FUNCTION reject_supplier_test_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$; CREATE TRIGGER reject_supplier_test_insert BEFORE INSERT ON supplier_usage FOR EACH ROW EXECUTE FUNCTION reject_supplier_test_insert()`)
	require.NoError(t, err)
	_, err = r.Apply(ctx, cmd)
	require.Error(t, err)
	var balance, state string
	var count int
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=2`).Scan(&balance))
	require.Equal(t, "10.00000000", balance)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM usage_billing_dedup`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT state FROM supplier_dispatches WHERE attempt_id=$1`, req.AttemptID).Scan(&state))
	require.Equal(t, "pending", state)
	_, err = db.Exec(`DROP TRIGGER reject_supplier_test_insert ON supplier_usage`)
	require.NoError(t, err)
	// A restarted worker receives no price/credential dependencies and replays the
	// persisted normalized command, committing all facts atomically once.
	recovered, err := (&usageBillingRepository{db: db}).RecoverSupplierBilling(ctx, 20)
	require.NoError(t, err)
	require.Equal(t, 1, recovered.Replayed)
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=2`).Scan(&balance))
	require.Equal(t, "9.80000000", balance)
	var base, fee string
	var version, credential, auth int64
	require.NoError(t, db.QueryRow(`SELECT fee_base_amount::text,fee_amount::text,policy_version,credential_version,authorization_version FROM supplier_usage`).Scan(&base, &fee, &version, &credential, &auth))
	require.Equal(t, "0.00007813", base)
	require.Equal(t, "0.00005469", fee)
	require.Equal(t, int64(9), version)
	require.Equal(t, int64(1), credential)
	require.Equal(t, int64(1), auth)
	got, err := r.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, got.Applied)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM supplier_usage`).Scan(&count))
	require.Equal(t, 1, count)
	repairs, err := r.ListSupplierBillingRepairs(ctx, 20)
	require.NoError(t, err)
	require.Len(t, repairs, 1)
	require.NoError(t, r.CompleteSupplierBillingRepair(ctx, req.AttemptID))
	require.NoError(t, r.CompleteSupplierBillingRepair(ctx, req.AttemptID))
	repairs, err = r.ListSupplierBillingRepairs(ctx, 20)
	require.NoError(t, err)
	require.Empty(t, repairs)
	var pending bool
	require.NoError(t, db.QueryRow(`SELECT repair_pending FROM supplier_dispatches`).Scan(&pending))
	require.True(t, pending)
	_, err = db.Exec(`UPDATE supplier_dispatches SET repair_available_at=CURRENT_TIMESTAMP-INTERVAL '1 second'`)
	require.NoError(t, err)
	repairs, err = r.ListSupplierBillingRepairs(ctx, 20)
	require.NoError(t, err)
	require.Len(t, repairs, 1)
	require.NoError(t, r.CompleteSupplierBillingRepair(ctx, req.AttemptID))
	require.NoError(t, db.QueryRow(`SELECT repair_pending FROM supplier_dispatches`).Scan(&pending))
	require.False(t, pending)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to('not-a-usable-key','UTF8')),'hex')`).Scan(&count))
	require.Positive(t, count)
	for _, query := range []string{`UPDATE supplier_usage SET fee_amount=0`, `DELETE FROM supplier_usage`, `UPDATE supplier_dispatches SET snapshot='{}'`, `UPDATE supplier_dispatches SET command_payload='{}'`, `DELETE FROM supplier_dispatch_observations`} {
		_, err = db.Exec(query)
		require.ErrorContains(t, err, "immutable")
	}
	var journal string
	require.NoError(t, db.QueryRow(`SELECT snapshot::text||observation::text||command_payload FROM supplier_dispatches`).Scan(&journal))
	for _, secret := range []string{"encrypted-only", "not-a-usable-key", "token-fingerprint", "account-metadata"} {
		require.NotContains(t, journal, secret)
	}
}

func TestSupplierDispatchPostgresUnknownAndConflicts(t *testing.T) {
	db := supplierDispatchTestDB(t)
	r := &usageBillingRepository{db: db}
	ctx := supplierDispatchTestContext()
	req := supplierDispatchTestRequest()
	a, err := r.AcceptSupplierDispatch(ctx, req)
	require.NoError(t, err)
	require.NoError(t, r.MarkSupplierDispatched(ctx, req.AttemptID, a.Snapshot.Digest))
	o := supplierDispatchTestObservation()
	o.Complete = false
	o.Terminal = "response.incomplete"
	require.NoError(t, r.ObserveSupplierUsage(ctx, req.AttemptID, a.Snapshot.Digest, o))
	require.ErrorIs(t, r.StageSupplierBilling(ctx, supplierDispatchTestBilling(a.Snapshot)), service.ErrSupplierAccountingPending)
	got, err := r.RecoverSupplierBilling(ctx, 20)
	require.NoError(t, err)
	require.Zero(t, got.Replayed)
	o = supplierDispatchTestObservation()
	require.NoError(t, r.ObserveSupplierUsage(ctx, req.AttemptID, a.Snapshot.Digest, o))
	cmd := supplierDispatchTestBilling(a.Snapshot)
	require.NoError(t, r.StageSupplierBilling(ctx, cmd))
	o.Usage.OutputTokens++
	require.ErrorIs(t, r.ObserveSupplierUsage(ctx, req.AttemptID, a.Snapshot.Digest, o), service.ErrSupplierDispatchConflict)
	_, err = r.Apply(ctx, cmd)
	require.ErrorIs(t, err, service.ErrSupplierAccountingPending)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM supplier_dispatch_observations`).Scan(&count))
	require.Equal(t, 3, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM supplier_usage`).Scan(&count))
	require.Zero(t, count)
	req2 := supplierDispatchTestRequest()
	_, err = r.AcceptSupplierDispatch(ctx, req2)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE supplier_dispatches SET updated_at=CURRENT_TIMESTAMP-INTERVAL '49 hours' WHERE attempt_id=$1`, req2.AttemptID)
	require.NoError(t, err)
	got, err = r.RecoverSupplierBilling(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, got.Review)
}

func TestSupplierDispatchPostgresAuthorizationAndCAS(t *testing.T) {
	db := supplierDispatchTestDB(t)
	r := &usageBillingRepository{db: db}
	ctx := supplierDispatchTestContext()
	req := supplierDispatchTestRequest()
	req.UserID = 1
	_, err := r.AcceptSupplierDispatch(ctx, req)
	require.Error(t, err)
	_, err = db.Exec(`INSERT INTO user_platform_quotas(user_id,platform,daily_limit_usd) VALUES(2,'openai',100)`)
	require.NoError(t, err)
	_, err = r.AcceptSupplierDispatch(ctx, supplierDispatchTestRequest())
	require.Error(t, err)
	_, err = db.Exec(`DELETE FROM user_platform_quotas`)
	require.NoError(t, err)
	req = supplierDispatchTestRequest()
	a, err := r.AcceptSupplierDispatch(ctx, req)
	require.NoError(t, err)
	// Concurrent pre-send calls yield exactly one send permission.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- r.MarkSupplierDispatched(ctx, req.AttemptID, a.Snapshot.Digest) }()
	}
	wg.Wait()
	close(results)
	wins := 0
	for e := range results {
		if e == nil {
			wins++
		}
	}
	require.Equal(t, 1, wins)
	req2 := supplierDispatchTestRequest()
	a2, err := r.AcceptSupplierDispatch(ctx, req2)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE supplier_resources SET supply_enabled=FALSE`)
	require.NoError(t, err)
	require.Error(t, r.MarkSupplierDispatched(ctx, req2.AttemptID, a2.Snapshot.Digest))
}

func TestSupplierDispatchPostgresConcurrentRevocationFailsClosed(t *testing.T) {
	db := supplierDispatchTestDB(t)
	r := &usageBillingRepository{db: db}
	ctx := supplierDispatchTestContext()
	req := supplierDispatchTestRequest()
	a, err := r.AcceptSupplierDispatch(ctx, req)
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE supplier_resources SET authorization_revoked_at=CURRENT_TIMESTAMP,supply_enabled=FALSE`)
	require.NoError(t, err)
	// Revocation is uncommitted and invisible to an ordinary RR SELECT. The send
	// lock must nevertheless block/fail, never return a send permit from old data.
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	require.Error(t, r.MarkSupplierDispatched(call, req.AttemptID, a.Snapshot.Digest))
	require.NoError(t, tx.Commit())
	require.Error(t, r.MarkSupplierDispatched(ctx, req.AttemptID, a.Snapshot.Digest))
	var dispatched sql.NullTime
	require.NoError(t, db.QueryRow(`SELECT dispatched_at FROM supplier_dispatches WHERE attempt_id=$1`, req.AttemptID).Scan(&dispatched))
	require.False(t, dispatched.Valid)
}

func TestSupplierDispatchPostgresConcurrentRecovery(t *testing.T) {
	db := supplierDispatchTestDB(t)
	r := &usageBillingRepository{db: db}
	ctx := supplierDispatchTestContext()
	req := supplierDispatchTestRequest()
	a, err := r.AcceptSupplierDispatch(ctx, req)
	require.NoError(t, err)
	require.NoError(t, r.MarkSupplierDispatched(ctx, req.AttemptID, a.Snapshot.Digest))
	require.NoError(t, r.ObserveSupplierUsage(ctx, req.AttemptID, a.Snapshot.Digest, supplierDispatchTestObservation()))
	cmd := supplierDispatchTestBilling(a.Snapshot)
	require.NoError(t, r.StageSupplierBilling(ctx, cmd))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := (&usageBillingRepository{db: db}).RecoverSupplierBilling(ctx, 1)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM supplier_usage`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM usage_billing_dedup`).Scan(&count))
	require.Equal(t, 1, count)
}
