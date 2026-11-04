// Command kiro-smoke is an opt-in private integration probe, never a server.
// It reads only an explicitly supplied file when an operator invokes it. It
// performs at most one refresh and one journaled generation, without retries.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Kiro smoke stopped; inspect safe local state, do not replay automatically")
		os.Exit(1)
	}
}
func run() error {
	// Marker is created before any credential read or upstream action. Keep both
	// this marker and the encrypted PostgreSQL data/key after a rotation.
	required := []string{"KIRO_SMOKE_DSN", "KIRO_SMOKE_FIXTURE", "KIRO_SMOKE_KEY_FILE", "KIRO_SMOKE_KEY_ID", "KIRO_SMOKE_MARKER", "KIRO_SMOKE_REGION", "KIRO_SMOKE_MODEL"}
	for _, k := range required {
		if os.Getenv(k) == "" {
			return fmt.Errorf("configuration missing")
		}
	}
	region := os.Getenv("KIRO_SMOKE_REGION")
	if region != "us-east-1" && region != "us-west-2" {
		return fmt.Errorf("region invalid")
	}
	marker, err := os.OpenFile(os.Getenv("KIRO_SMOKE_MARKER"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("marker exists or unavailable")
	}
	defer marker.Close()
	if _, err = marker.WriteString("single attempt reserved; do not delete or replay\n"); err != nil {
		return err
	}
	if err = marker.Sync(); err != nil {
		return err
	}
	info, err := os.Stat(os.Getenv("KIRO_SMOKE_FIXTURE"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return fmt.Errorf("fixture permissions invalid")
	}
	input, err := os.ReadFile(os.Getenv("KIRO_SMOKE_FIXTURE"))
	if err != nil {
		return fmt.Errorf("fixture unavailable")
	}
	defer clear(input)
	if code := safeFixtureShape(input); code != "" {
		fmt.Fprintln(os.Stderr, "kiro_smoke_phase=local_fixture error_code="+code)
		return fmt.Errorf("fixture shape rejected")
	}
	db, err := sql.Open("postgres", os.Getenv("KIRO_SMOKE_DSN"))
	if err != nil {
		return fmt.Errorf("database unavailable")
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	cfg := &config.Config{}
	cfg.KiroVault.VaultKeyFile = os.Getenv("KIRO_SMOKE_KEY_FILE")
	cfg.KiroVault.VaultKeyID = os.Getenv("KIRO_SMOKE_KEY_ID")
	native := repository.ProvideKiroNativeService(db, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	imported, err := native.Import(ctx, service.KiroNativeImport{Name: "private-bounded-kiro-smoke", Credential: input, Region: region, AuthMode: service.KiroCredentialIdC, UserAgent: os.Getenv("KIRO_SMOKE_USER_AGENT"), AmzUserAgent: os.Getenv("KIRO_SMOKE_AMZ_USER_AGENT"), MachineID: os.Getenv("KIRO_SMOKE_MACHINE_ID")})
	if err != nil {
		fmt.Fprintln(os.Stderr, "kiro_smoke_phase=local_import error_code="+safeImportCode(err))
		return fmt.Errorf("import rejected")
	}
	if _, err = fmt.Fprintf(marker, "account_id=%d credential_ref=%s\n", imported.AccountID, imported.CredentialRef); err != nil {
		return err
	}
	if err = marker.Sync(); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"model": os.Getenv("KIRO_SMOKE_MODEL"), "max_tokens": 32, "stream": true, "messages": []any{map[string]any{"role": "user", "content": "Reply with OK only."}}})
	request, err := kiro.ConvertMessagesText(body, "bounded-smoke")
	if err != nil {
		return err
	}
	operation := sha256.Sum256([]byte(os.Getenv("KIRO_SMOKE_MARKER")))
	operationID := hex.EncodeToString(operation[:])
	// Bound output observation independently of maxTokens, which is an extension
	// and not a verified upstream enforcement contract. Never print generated text.
	textBytes := 0
	result, executeErr := native.ExecutePrivate(ctx, imported.AccountID, operationID, request, func(e kiro.Event) error {
		if a, ok := e.Assistant(); ok {
			textBytes += len(a.Content)
			if textBytes >= 256 {
				cancel()
				return context.Canceled
			}
		}
		return nil
	})
	outcome := "unverified"
	if executeErr != nil {
		outcome = "error_or_unverified"
	}
	// No bearer, raw error, event payload, fixture fields or output is printed.
	fmt.Printf("account_id=%d phase=%s http_status=%d events=%d observed_text_bytes=%d completion_verified=%t outcome=%s\n", imported.AccountID, result.Phase, result.StatusCode, result.Events, textBytes, result.CompletionVerified, outcome)
	_, _ = fmt.Fprintf(marker, "operation_id=%s phase=%s http_status=%d observed_text_bytes=%d\n", operationID, result.Phase, result.StatusCode, textBytes)
	_ = marker.Sync()
	if executeErr != nil && !strings.Contains(executeErr.Error(), "completion unestablished") {
		return fmt.Errorf("attempt did not establish completion")
	}
	return nil
}

// Never print err.Error(), pq.Detail, input field names, paths or secret values.
func safeImportCode(err error) string {
	switch {
	case errors.Is(err, service.ErrKiroUnavailable):
		return "vault_unavailable"
	case errors.Is(err, service.ErrKiroCredentialConfiguration):
		return "vault_configuration"
	case errors.Is(err, service.ErrKiroCredentialCrypto):
		return "credential_encryption"
	case errors.Is(err, service.ErrKiroCredentialInvalid):
		return "credential_validation"
	case errors.Is(err, service.ErrKiroCredentialUnsupported):
		return "unsupported_credential"
	case errors.Is(err, service.ErrKiroCredentialExpired):
		return "expired_credential"
	case errors.Is(err, service.ErrKiroRefreshBarrier):
		return "local_commit_ambiguous"
	}
	var pg *pq.Error
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23502":
			return "database_not_null_constraint"
		case "23503":
			return "database_foreign_key_constraint"
		case "23505":
			return "database_unique_constraint"
		case "23514":
			return "database_check_constraint"
		case "42P01":
			return "database_missing_relation"
		case "42703":
			return "database_missing_column"
		case "42501":
			return "database_permission"
		default:
			return "database_error"
		}
	}
	return "local_import_unknown"
}

func safeFixtureShape(input []byte) string {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return "credential_array_not_supported"
	}
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return "credential_object_required"
	}
	return ""
}
