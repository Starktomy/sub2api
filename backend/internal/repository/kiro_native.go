package repository

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type KiroNativeRepository struct{ db *sql.DB }

func NewKiroNativeRepository(db *sql.DB) *KiroNativeRepository { return &KiroNativeRepository{db: db} }
func ProvideKiroNativeService(db *sql.DB, cfg *config.Config) *service.KiroNativeService {
	// Reuse strict oauth refresh transport constraints: HTTP/1 only, fresh
	// connections, no environmental proxy, redirect, alternate host or retry.
	tr := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: -1}).DialContext, DisableKeepAlives: true, ForceAttemptHTTP2: false, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 60 * time.Second}
	client := &http.Client{Transport: tr, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return service.NewKiroNativeService(NewKiroNativeRepository(db), service.ProvideKiroVault(cfg), client, NewUsageAttemptRepository(db, 10000))
}
func (r *KiroNativeRepository) Create(ctx context.Context, in service.KiroNativeImport, ref string, encrypt func(service.KiroCredentialBinding) ([]byte, error)) (*service.KiroNativeRecord, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	creds, _ := json.Marshal(map[string]any{"kiro_credential_ref": ref})
	var id int64
	// Deliberately paused: cannot join a paid pool until a settlement policy exists.
	err = tx.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,credentials,extra,concurrency,priority,status,schedulable) VALUES($1,'kiro','oauth',$2,'{}',1,50,'disabled',false) RETURNING id`, in.Name, string(creds)).Scan(&id)
	if err != nil {
		return nil, err
	}
	b := service.KiroCredentialBinding{ResourceUUID: ref, AccountID: id, Version: 1, Mode: in.AuthMode}
	envelope, err := encrypt(b)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO kiro_credentials(id,account_id,version,mode,region,envelope,user_agent,amz_user_agent,machine_id) VALUES($1,$2,1,$3,$4,$5,$6,$7,$8)`, ref, id, string(in.AuthMode), in.Region, envelope, in.UserAgent, in.AmzUserAgent, in.MachineID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, service.ErrKiroRefreshBarrier
	}
	return &service.KiroNativeRecord{Binding: b, Region: in.Region, Envelope: envelope, RefreshState: "idle", UserAgent: in.UserAgent, AmzUserAgent: in.AmzUserAgent, MachineID: in.MachineID}, nil
}
func (r *KiroNativeRepository) Get(ctx context.Context, id int64) (*service.KiroNativeRecord, error) {
	v := &service.KiroNativeRecord{}
	err := r.db.QueryRowContext(ctx, `SELECT k.id,k.account_id,k.version,k.mode,k.region,k.envelope,k.user_agent,k.amz_user_agent,k.machine_id,k.refresh_state,COALESCE(k.refresh_owner::text,''),k.models_snapshot,k.quota_snapshot FROM kiro_credentials k JOIN accounts a ON a.id=k.account_id WHERE k.account_id=$1 AND a.deleted_at IS NULL AND a.platform='kiro' AND a.type='oauth' AND a.credentials->>'kiro_credential_ref'=k.id::text`, id).Scan(&v.Binding.ResourceUUID, &v.Binding.AccountID, &v.Binding.Version, &v.Binding.Mode, &v.Region, &v.Envelope, &v.UserAgent, &v.AmzUserAgent, &v.MachineID, &v.RefreshState, &v.RefreshOwner, &v.ModelsSnapshot, &v.QuotaSnapshot)
	if err != nil {
		return nil, err
	}
	return v, nil
}
func kiroAffected(result sql.Result, err error) error {
	if err != nil {
		return service.ErrKiroRefreshBarrier
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return service.ErrKiroRefreshBarrier
	}
	return nil
}
func (r *KiroNativeRepository) BeginRefresh(ctx context.Context, v *service.KiroNativeRecord, owner string) error {
	return kiroAffected(r.db.ExecContext(ctx, `UPDATE kiro_credentials SET refresh_state='pending',refresh_owner=$4,refresh_started_at=NOW(),updated_at=NOW() WHERE id=$1 AND account_id=$2 AND version=$3 AND refresh_state='idle'`, v.Binding.ResourceUUID, v.Binding.AccountID, v.Binding.Version, owner))
}
func (r *KiroNativeRepository) CompleteRefresh(ctx context.Context, v *service.KiroNativeRecord, owner string, envelope []byte) error {
	return kiroAffected(r.db.ExecContext(ctx, `UPDATE kiro_credentials SET envelope=$5,version=version+1,refresh_state='idle',refresh_owner=NULL,updated_at=NOW() WHERE id=$1 AND account_id=$2 AND version=$3 AND refresh_owner=$4 AND refresh_state='pending'`, v.Binding.ResourceUUID, v.Binding.AccountID, v.Binding.Version, owner, envelope))
}
func (r *KiroNativeRepository) AmbiguousRefresh(ctx context.Context, v *service.KiroNativeRecord, owner string) error {
	return kiroAffected(r.db.ExecContext(ctx, `UPDATE kiro_credentials SET refresh_state='ambiguous',updated_at=NOW() WHERE id=$1 AND account_id=$2 AND version=$3 AND refresh_owner=$4 AND refresh_state='pending'`, v.Binding.ResourceUUID, v.Binding.AccountID, v.Binding.Version, owner))
}

func (r *KiroNativeRepository) PatchRuntime(ctx context.Context, v *service.KiroNativeRecord, p service.KiroRuntimePatch, envelope []byte) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Lock/verify the account reference and keep the shared account permanently paused.
	var id int64
	err = tx.QueryRowContext(ctx, `UPDATE accounts SET status='disabled',schedulable=false WHERE id=$1 AND platform='kiro' AND type='oauth' AND deleted_at IS NULL AND credentials->>'kiro_credential_ref'=$2 RETURNING id`, v.Binding.AccountID, v.Binding.ResourceUUID).Scan(&id)
	if err != nil {
		return service.ErrKiroRefreshBarrier
	}
	err = kiroAffected(tx.ExecContext(ctx, `UPDATE kiro_credentials SET version=version+1,envelope=$4,user_agent=$5,amz_user_agent=$6,machine_id=$7,refresh_state=CASE WHEN $8='reimport' THEN 'idle' ELSE refresh_state END,refresh_owner=CASE WHEN $8='reimport' THEN NULL ELSE refresh_owner END,refresh_started_at=CASE WHEN $8='reimport' THEN NULL ELSE refresh_started_at END,models_snapshot='{"state":"unknown","source":"not_observed"}',quota_snapshot='{"state":"unknown","source":"not_observed"}',updated_at=NOW() WHERE id=$1 AND account_id=$2 AND version=$3 AND refresh_state=$9 AND COALESCE(refresh_owner::text,'')=$10`, v.Binding.ResourceUUID, v.Binding.AccountID, p.ExpectedVersion, envelope, v.UserAgent, v.AmzUserAgent, v.MachineID, p.Operation, v.RefreshState, v.RefreshOwner))
	if err != nil {
		return err
	}
	if tx.Commit() != nil {
		return service.ErrKiroRefreshBarrier
	}
	return nil
}
func (r *KiroNativeRepository) SaveSnapshot(ctx context.Context, v *service.KiroNativeRecord, kind string, snapshot json.RawMessage) error {
	column := "quota_snapshot"
	if kind == "models" {
		column = "models_snapshot"
	} else if kind != "quota" {
		return service.ErrKiroCredentialInvalid
	}
	return kiroAffected(r.db.ExecContext(ctx, `UPDATE kiro_credentials SET `+column+`=$4,updated_at=NOW() WHERE id=$1 AND account_id=$2 AND version=$3 AND refresh_state='idle'`, v.Binding.ResourceUUID, v.Binding.AccountID, v.Binding.Version, string(snapshot)))
}
