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

// Opt-in disposable local PostgreSQL only, matching supplier admission tests.
func TestSupplierUsageBillingPostgres(t *testing.T) {
	socket := os.Getenv("SUPPLIER_TEST_PG_SOCKET")
	if socket == "" {
		t.Skip("disposable PG required")
	}
	require.Contains(t, socket, "/tmp/supplier-pg-")
	db, err := sql.Open("postgres", "host="+socket+" port=55439 user=postgres dbname=postgres sslmode=disable")
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("supplier_billing_%d", time.Now().UnixNano())
	_, err = db.Exec(`CREATE SCHEMA ` + schema + `; SET search_path TO ` + schema)
	require.NoError(t, err)
	defer db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	for _, file := range []string{"001_init.sql", "253_supplier_profiles.sql", "254_supplier_resources.sql", "255_supplier_probe_jobs.sql", "256_supplier_admission.sql", "071_add_usage_billing_dedup.sql", "073_add_usage_billing_dedup_archive.sql", "257_supplier_usage.sql", "257_supplier_usage.sql"} {
		migration, e := os.ReadFile("../../migrations/" + file)
		require.NoError(t, e)
		_, e = db.Exec(string(migration))
		require.NoError(t, e, file)
	}
	_, err = db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 INSERT INTO users(id,email,password_hash,balance) VALUES(1,'billing@example.invalid','not-a-password',10);
 INSERT INTO supplier_profiles(id,user_id,display_name,verified_email) VALUES(1,1,'supplier','billing@example.invalid');
 INSERT INTO accounts(id,name,platform,type) VALUES(1,'supplier-account','openai','oauth');
 INSERT INTO supplier_resources(id,supplier_id,display_name,identity_fingerprint,account_hint,token_fingerprint,key_id,envelope,masked_email,token_expires_at,status)
 VALUES('00000000-0000-4000-8000-000000000001',1,'resource','identity','account','token','key','encrypted','masked',CURRENT_TIMESTAMP+INTERVAL '1 hour','paused');
 INSERT INTO supplier_account_bindings(resource_id,account_id,supplier_id,credential_version) VALUES('00000000-0000-4000-8000-000000000001',1,1,1)`)
	require.NoError(t, err)
	repo := &usageBillingRepository{db: db}
	cmd := &service.UsageBillingCommand{RequestID: "pg-supplier-request", APIKeyID: 1, UserID: 1, AccountID: 1,
		Model: "test-model", SupplierCostProvided: true, StandardCost: 0.000078125, CustomerCharge: 0.2, BalanceCost: 0.2, InputTokens: 100, OutputTokens: 20}
	checkState := func(balance string, claims, facts int) {
		t.Helper()
		var got string
		var count int
		require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=1`).Scan(&got))
		require.Equal(t, balance, got)
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM usage_billing_dedup`).Scan(&count))
		require.Equal(t, claims, count)
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM supplier_usage`).Scan(&count))
		require.Equal(t, facts, count)
	}
	// Missing policy must undo the debit and the dedup claim together.
	result, err := repo.Apply(context.Background(), cmd)
	require.Error(t, err)
	require.Nil(t, result)
	checkState("10.00000000", 0, 0)
	_, err = db.Exec(`INSERT INTO settings VALUES('supplier_operating_policy_v1','{"fee_mode":"standard_cost","fee_ratio_bps":7000,"version":9}')`)
	require.NoError(t, err)
	result, err = repo.Apply(context.Background(), cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	checkState("9.80000000", 1, 1)
	var base, fee string
	var version int64
	require.NoError(t, db.QueryRow(`SELECT fee_base_amount::text,fee_amount::text,policy_version FROM supplier_usage`).Scan(&base, &fee, &version))
	require.Equal(t, "0.00007813", base)
	require.Equal(t, "0.00005469", fee)
	require.Equal(t, int64(9), version)
	// Policy deletion cannot cause repricing or prevent a committed retry.
	_, err = db.Exec(`DELETE FROM settings`)
	require.NoError(t, err)
	result, err = repo.Apply(context.Background(), cmd)
	require.NoError(t, err)
	require.False(t, result.Applied)
	checkState("9.80000000", 1, 1)
	// UPDATE and DELETE cannot rewrite history.
	_, err = db.Exec(`UPDATE supplier_usage SET fee_amount=0`)
	require.ErrorContains(t, err, "immutable")
	_, err = db.Exec(`DELETE FROM supplier_usage`)
	require.ErrorContains(t, err, "immutable")
	// Check constraints independently reject inconsistent new facts.
	insert := `INSERT INTO supplier_usage(request_id,api_key_id,request_fingerprint,user_id,account_id,resource_id,supplier_id,credential_version,model,billing_type,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,image_count,standard_cost,customer_charge,fee_mode,fee_ratio_bps,policy_version,fee_base_amount,fee_amount)
 SELECT $1,api_key_id,request_fingerprint,user_id,account_id,resource_id,supplier_id,credential_version,model,billing_type,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,image_count,standard_cost,customer_charge,fee_mode,fee_ratio_bps,policy_version,$2,$3 FROM supplier_usage LIMIT 1`
	_, err = db.Exec(insert, "bad-base", 0.2, 0.14)
	require.ErrorContains(t, err, "supplier_usage_fee_base_matches_mode")
	_, err = db.Exec(insert, "bad-fee", 0.00007813, 0)
	require.ErrorContains(t, err, "supplier_usage_fee_matches_snapshot")
	checkState("9.80000000", 1, 1)
}
