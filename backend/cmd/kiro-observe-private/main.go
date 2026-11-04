// Command kiro-observe-private explicitly queries one saved test account.
// It never imports a fixture or retries an operation. Only explicit kind=refresh
// may refresh credentials; models/quota never refresh implicitly.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"os"
	"time"
)

func main() {
	if run() != nil {
		fmt.Println("observation stopped; no retry performed")
		os.Exit(1)
	}
}
func run() error {
	for _, k := range []string{"KIRO_OBSERVE_DSN", "KIRO_OBSERVE_KEY_FILE", "KIRO_OBSERVE_KEY_ID", "KIRO_OBSERVE_MARKER", "KIRO_OBSERVE_KIND"} {
		if os.Getenv(k) == "" {
			return errors.New("configuration missing")
		}
	}
	kind := os.Getenv("KIRO_OBSERVE_KIND")
	if kind != "models" && kind != "quota" && kind != "refresh" && kind != "inference" {
		return errors.New("invalid kind")
	}
	f, e := os.OpenFile(os.Getenv("KIRO_OBSERVE_MARKER"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = fmt.Fprintf(f, "single existing-account operation reserved; kind=%s; no implicit refresh or replay\n", kind); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	db, e := sql.Open("postgres", os.Getenv("KIRO_OBSERVE_DSN"))
	if e != nil {
		return errors.New("database unavailable")
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	cfg := &config.Config{}
	cfg.KiroVault.VaultKeyFile = os.Getenv("KIRO_OBSERVE_KEY_FILE")
	cfg.KiroVault.VaultKeyID = os.Getenv("KIRO_OBSERVE_KEY_ID")
	native := repository.ProvideKiroNativeService(db, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if kind == "inference" {
		request, err := kiro.ConvertMessagesText([]byte(`{"model":"claude-haiku-4.5","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"Reply with OK only."}]}`), "bounded-platform-probe")
		if err != nil {
			return err
		}
		h := sha256.Sum256([]byte(os.Getenv("KIRO_OBSERVE_MARKER")))
		textBytes := 0
		result, err := native.ExecutePrivate(ctx, 2, hex.EncodeToString(h[:]), request, func(event kiro.Event) error {
			if a, ok := event.Assistant(); ok {
				textBytes += len(a.Content)
				if textBytes >= 256 {
					cancel()
					return context.Canceled
				}
			}
			return nil
		})
		fmt.Printf("kind=inference http_status=%d events=%d text_bytes=%d completion_verified=%t transport_eof=%t error_code=%s error_field=%s\n", result.StatusCode, result.Events, textBytes, result.CompletionVerified, result.TransportEOF, result.ErrorCode, result.ErrorField)
		if err != nil && !errors.Is(err, kiro.ErrIncomplete) {
			return errors.New("inference failed; do not replay")
		}
		return nil
	}
	if kind == "refresh" {
		record, _, err := native.Inference(ctx, 2)
		if err != nil {
			fmt.Println("kind=refresh result=blocked_or_ambiguous")
			return errors.New("refresh stopped")
		}
		fmt.Printf("kind=refresh version=%d state=%s\n", record.Binding.Version, record.RefreshState)
		_, err = fmt.Fprintf(f, "kind=refresh version=%d state=%s\n", record.Binding.Version, record.RefreshState)
		if err != nil {
			return err
		}
		return f.Sync()
	}
	status, e := native.Observe(ctx, 2, kind)
	if e != nil {
		code := "unavailable"
		if errors.Is(e, service.ErrKiroCredentialExpired) {
			code = "credential_expired"
		}
		if errors.Is(e, service.ErrKiroCredentialInvalid) {
			code = "validation_failed"
		}
		if errors.Is(e, service.ErrKiroRefreshBarrier) {
			code = "refresh_blocked"
		}
		var observation *service.KiroObservationError
		if errors.As(e, &observation) {
			fmt.Printf("kind=%s result=%s http_status=%d error_code=%s reason=%s\n", kind, observation.Kind, observation.StatusCode, observation.Code, observation.Reason)
		} else {
			fmt.Printf("kind=%s result=%s\n", kind, code)
		}
		return errors.New("observation failed")
	}
	raw := status.Models
	if kind == "quota" {
		raw = status.Quota
	}
	var snapshot struct {
		State  string          `json:"state"`
		Source string          `json:"source"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return errors.New("snapshot invalid")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(snapshot.Data, &data) != nil {
		return errors.New("data invalid")
	}
	count := 0
	name := "models"
	if kind == "quota" {
		name = "usageBreakdownList"
	}
	var items []json.RawMessage
	_ = json.Unmarshal(data[name], &items)
	count = len(items)
	if kind == "models" {
		for _, item := range items {
			var model struct {
				ID   string `json:"modelId"`
				Name string `json:"modelName"`
			}
			if json.Unmarshal(item, &model) != nil {
				return errors.New("invalid model")
			}
			fmt.Printf("model_id=%q model_name=%q\n", model.ID, model.Name)
		}
	}
	fmt.Printf("kind=%s state=%s entries=%d version=%d paused=%t\n", kind, snapshot.State, count, status.CredentialVersion, status.Paused)
	_, e = fmt.Fprintf(f, "kind=%s state=%s entries=%d version=%d\n", kind, snapshot.State, count, status.CredentialVersion)
	if e != nil {
		return e
	}
	return f.Sync()
}
