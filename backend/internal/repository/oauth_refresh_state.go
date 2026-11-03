package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.OAuthRefreshStateRepository = (*accountRepository)(nil)

func oauthRefreshFingerprint(account *service.Account, credentials map[string]any) (string, error) {
	document, err := json.Marshal(struct {
		Platform    string         `json:"platform"`
		Type        string         `json:"type"`
		ProxyID     *int64         `json:"proxy_id"`
		Credentials map[string]any `json:"credentials"`
	}{account.Platform, account.Type, account.ProxyID, normalizeJSONMap(credentials)})
	if err != nil {
		return "", errors.New("OAuth refresh snapshot encoding failed")
	}
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:]), nil
}

// Begin commits a durable no-replay barrier before returning permission to call
// the provider. An expired pending holder is quarantined, never replaced.
func (r *accountRepository) BeginOAuthRefresh(ctx context.Context, expected *service.Account, ttl time.Duration) (*service.OAuthRefreshAttempt, error) {
	if r == nil || r.client == nil || expected == nil || ttl <= 0 {
		return nil, errors.New("OAuth refresh durable repository is not configured")
	}
	fingerprint, err := oauthRefreshFingerprint(expected, expected.Credentials)
	if err != nil {
		return nil, err
	}
	document, err := json.Marshal(normalizeJSONMap(expected.Credentials))
	if err != nil {
		return nil, err
	}
	ownerBytes := make([]byte, 24)
	if _, err = rand.Read(ownerBytes); err != nil {
		return nil, err
	}
	owner := hex.EncodeToString(ownerBytes)
	credentialsDigest := sha256.Sum256(document)
	credentialsFingerprint := hex.EncodeToString(credentialsDigest[:])
	refreshDigest := sha256.Sum256([]byte(expected.GetCredential("refresh_token")))
	refreshFingerprint := hex.EncodeToString(refreshDigest[:])
	if adminaccess.FromContext(ctx) != nil {
		if _, ok := service.OAuthRefreshAuthorizationFromContext(ctx); !ok {
			return nil, service.ErrScopedAdminKeyInvalid
		}
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if err = r.authorizeOAuthRefresh(ctx, client, expected.ID, true); err != nil {
		return nil, err
	}
	rows, err := client.QueryContext(ctx, `WITH eligible AS (
 SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL
 AND platform=$2 AND type=$3 AND credentials=$4::jsonb
 AND proxy_id IS NOT DISTINCT FROM $5 FOR UPDATE
 ) INSERT INTO oauth_refresh_state AS s
 (account_id,generation,owner,state,input_fingerprint,input_credentials_fingerprint,input_refresh_token_fingerprint,lease_until)
 SELECT id,1,$6,'pending',$7,$9,$10,NOW()+($8 * INTERVAL '1 millisecond') FROM eligible
 ON CONFLICT (account_id) DO UPDATE SET generation=s.generation+1,
 owner=EXCLUDED.owner,state='pending',input_fingerprint=EXCLUDED.input_fingerprint,
 input_credentials_fingerprint=EXCLUDED.input_credentials_fingerprint,
 input_refresh_token_fingerprint=EXCLUDED.input_refresh_token_fingerprint,
 output_fingerprint=NULL,lease_until=EXCLUDED.lease_until,updated_at=NOW()
 WHERE s.state='completed' AND s.input_fingerprint<>EXCLUDED.input_fingerprint
 AND s.input_credentials_fingerprint<>EXCLUDED.input_credentials_fingerprint
 RETURNING generation`, expected.ID, expected.Platform, expected.Type, string(document), expected.ProxyID, owner, fingerprint, ttl.Milliseconds(), credentialsFingerprint, refreshFingerprint)
	if err != nil {
		return nil, err
	}
	var generation int64
	acquired := rows.Next()
	if acquired {
		err = rows.Scan(&generation)
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	if acquired {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return &service.OAuthRefreshAttempt{AccountID: expected.ID, Generation: generation, Owner: owner}, nil
	}
	// Expiry only changes classification. It does NOT authorize token reuse.
	if _, err = client.ExecContext(ctx, `UPDATE oauth_refresh_state SET state='ambiguous',updated_at=NOW()
 WHERE account_id=$1 AND state='pending' AND lease_until<=NOW()`, expected.ID); err != nil {
		return nil, err
	}
	rows, err = client.QueryContext(ctx, `SELECT state FROM oauth_refresh_state WHERE account_id=$1`, expected.ID)
	if err != nil {
		return nil, err
	}
	var state string
	if rows.Next() {
		err = rows.Scan(&state)
	}
	rowErr = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if state == "pending" {
		return nil, service.ErrOAuthRefreshPending
	}
	return nil, service.ErrOAuthRefreshAmbiguous
}

// Complete may be retried ONLY with the same provider response and identity.
// Account credentials, durable completed state and scheduler event commit together.
// The lease is not a provider deadline: its expired original holder may salvage a
// late response, but only while no recovery operator has fenced its generation.
func (r *accountRepository) CompleteOAuthRefresh(ctx context.Context, attempt *service.OAuthRefreshAttempt, expected *service.Account, credentials map[string]any) (bool, error) {
	if r == nil || r.client == nil || attempt == nil || expected == nil || attempt.AccountID != expected.ID || credentials == nil {
		return false, errors.New("invalid OAuth refresh completion")
	}
	input, err := oauthRefreshFingerprint(expected, expected.Credentials)
	if err != nil {
		return false, err
	}
	output, err := oauthRefreshFingerprint(expected, credentials)
	if err != nil {
		return false, err
	}
	oldJSON, err := json.Marshal(normalizeJSONMap(expected.Credentials))
	if err != nil {
		return false, err
	}
	newJSON, err := json.Marshal(normalizeJSONMap(credentials))
	if err != nil {
		return false, err
	}
	if adminaccess.FromContext(ctx) != nil {
		if _, ok := service.OAuthRefreshAuthorizationFromContext(ctx); !ok {
			return false, service.ErrScopedAdminKeyInvalid
		}
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if err = r.authorizeOAuthRefresh(ctx, client, expected.ID, true); err != nil {
		return false, err
	}
	// Lock account first, consistently with Begin and authorization row locking.
	rows, err := client.QueryContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, expected.ID)
	if err != nil {
		return false, err
	}
	for rows.Next() {
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return false, err
	}
	rows, err = client.QueryContext(ctx, `SELECT state,output_fingerprint FROM oauth_refresh_state
 WHERE account_id=$1 AND generation=$2 AND owner=$3 AND input_fingerprint=$4 FOR UPDATE`, attempt.AccountID, attempt.Generation, attempt.Owner, input)
	if err != nil {
		return false, err
	}
	var state string
	var durableOutput *string
	found := rows.Next()
	if found {
		err = rows.Scan(&state, &durableOutput)
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return false, err
	}
	if rowErr != nil {
		return false, rowErr
	}
	if !found {
		return false, nil
	}
	if state == "completed" {
		// Do not reapply or publish an event twice after a lost commit response.
		if durableOutput == nil || *durableOutput != output {
			return false, nil
		}
		return true, tx.Commit()
	}
	if state != "pending" && state != "ambiguous" {
		return false, nil
	}
	result, err := client.ExecContext(ctx, `WITH updated AS (
 UPDATE accounts SET credentials=$1::jsonb,updated_at=NOW()
 WHERE id=$2 AND deleted_at IS NULL AND platform=$3 AND type=$4
 AND credentials=$5::jsonb AND proxy_id IS NOT DISTINCT FROM $6 RETURNING id
 ), completed AS (
 UPDATE oauth_refresh_state SET state='completed',output_fingerprint=$7,updated_at=NOW()
 WHERE account_id IN (SELECT id FROM updated) AND generation=$8 AND owner=$9 RETURNING account_id
 ) INSERT INTO scheduler_outbox (event_type,account_id,group_id,payload)
 SELECT $10,account_id,NULL,NULL FROM completed`, string(newJSON), expected.ID, expected.Platform, expected.Type, string(oldJSON), expected.ProxyID, output, attempt.Generation, attempt.Owner, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		if _, err = client.ExecContext(ctx, `UPDATE oauth_refresh_state SET state='ambiguous',updated_at=NOW() WHERE account_id=$1 AND generation=$2 AND owner=$3 AND state<>'completed'`, attempt.AccountID, attempt.Generation, attempt.Owner); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return affected > 0, nil
}

// ResolveOAuthRefreshAmbiguous is an explicit operator recovery, never called by
// normal refresh paths. Stop every old holder and reauthorize first. The account
// must remain inactive and unschedulable during recovery. Changed route/type alone
// is not proof of new provider authorization: callers must supply genuinely fresh
// provider credentials, not reuse the possibly consumed token. Generation advances
// permanently so a late response cannot overwrite the recovered credentials.
func (r *accountRepository) ResolveOAuthRefreshAmbiguous(ctx context.Context, expected *service.Account, generation int64) (bool, error) {
	if r == nil || r.client == nil || expected == nil || generation <= 0 {
		return false, errors.New("invalid OAuth refresh recovery")
	}
	if expected.IsActive() || expected.Schedulable {
		return false, errors.New("OAuth refresh recovery requires an inactive unschedulable account")
	}
	if adminaccess.FromContext(ctx) != nil {
		if _, ok := service.OAuthRefreshAuthorizationFromContext(ctx); !ok {
			return false, service.ErrScopedAdminKeyInvalid
		}
	}
	document, err := json.Marshal(normalizeJSONMap(expected.Credentials))
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(document)
	fingerprint := hex.EncodeToString(digest[:])
	refreshToken := expected.GetCredential("refresh_token")
	if refreshToken == "" {
		return false, errors.New("OAuth refresh recovery requires a new refresh token")
	}
	refreshDigest := sha256.Sum256([]byte(refreshToken))
	refreshFingerprint := hex.EncodeToString(refreshDigest[:])
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if err = r.authorizeOAuthRefresh(ctx, client, expected.ID, true); err != nil {
		return false, err
	}
	result, err := client.ExecContext(ctx, `WITH eligible AS (
 SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL AND status<>'active'
 AND schedulable=false AND platform=$2 AND type=$3 AND credentials=$4::jsonb
 AND proxy_id IS NOT DISTINCT FROM $5 FOR UPDATE
 ) UPDATE oauth_refresh_state SET generation=generation+1,owner='',state='completed',
 output_fingerprint=NULL,updated_at=NOW()
 WHERE account_id IN (SELECT id FROM eligible) AND generation=$6
 AND (state='ambiguous' OR (state='pending' AND lease_until<=NOW()))
 AND input_credentials_fingerprint<>$7 AND input_refresh_token_fingerprint<>$8`, expected.ID, expected.Platform, expected.Type, string(document), expected.ProxyID, generation, fingerprint, refreshFingerprint)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (r *accountRepository) MarkOAuthRefreshAmbiguous(ctx context.Context, attempt *service.OAuthRefreshAttempt) error {
	if r == nil || r.sql == nil || attempt == nil {
		return errors.New("invalid OAuth refresh quarantine")
	}
	_, err := r.sql.ExecContext(ctx, `UPDATE oauth_refresh_state SET state='ambiguous',updated_at=NOW()
 WHERE account_id=$1 AND generation=$2 AND owner=$3 AND state='pending'`, attempt.AccountID, attempt.Generation, attempt.Owner)
	return err
}
