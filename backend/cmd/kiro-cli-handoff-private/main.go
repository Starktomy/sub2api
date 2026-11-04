// Private operator-only export of the current test access token. No refresh material
// is handed to a second refresh owner. Output must be a new private file.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/credentialcrypto"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	_ "github.com/lib/pq"
)

func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "private handoff failed")
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, e := sql.Open("postgres", os.Getenv("KIRO_OBSERVE_DSN"))
	if e != nil {
		return e
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	r, e := repository.NewKiroNativeRepository(db).Get(ctx, 2)
	if e != nil {
		return e
	}
	if r.RefreshState != "idle" {
		return fmt.Errorf("refresh barrier")
	}
	w, e := credentialcrypto.LoadLocalKeyWrapper(os.Getenv("KIRO_OBSERVE_KEY_FILE"))
	if e != nil {
		return e
	}
	c, e := credentialcrypto.New(w, os.Getenv("KIRO_OBSERVE_KEY_ID"))
	if e != nil {
		return e
	}
	raw, _ := json.Marshal([]string{"sub2api/kiro-inference-credentials", "1", r.Binding.ResourceUUID, strconv.FormatInt(r.Binding.AccountID, 10), strconv.FormatInt(r.Binding.Version, 10), string(r.Binding.Mode)})
	sum := sha256.Sum256(raw)
	doc, e := c.Decrypt(ctx, "kiro:v1:"+hex.EncodeToString(sum[:]), r.Envelope)
	if e != nil {
		return e
	}
	defer clear(doc)
	text, ok := doc["kiro_secret"].(string)
	if !ok {
		return fmt.Errorf("missing secret")
	}
	var s struct {
		InferenceToken string `json:"inference_token"`
		InferenceType  string `json:"inference_token_type"`
		Expires        int64  `json:"inference_expires_at"`
		SSO            string `json:"sso_access_token"`
		SSOExpires     int64  `json:"sso_expires_at"`
	}
	if json.Unmarshal([]byte(text), &s) != nil {
		return fmt.Errorf("invalid secret")
	}
	token, expiry := s.SSO, s.SSOExpires
	if token == "" {
		token, expiry = s.InferenceToken, s.Expires
	}
	if token == "" || expiry <= time.Now().Add(2*time.Minute).Unix() {
		return fmt.Errorf("expired")
	}
	out := map[string]any{"access_token": token, "expires_at": time.Unix(expiry, 0).UTC().Format(time.RFC3339), "refresh_token": nil, "region": r.Region, "start_url": nil, "oauth_flow": "DeviceCode", "scopes": nil}
	f, e := os.OpenFile(os.Getenv("KIRO_HANDOFF_OUTPUT"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = json.NewEncoder(f).Encode(out); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	fmt.Printf("handoff_version=%d access_only=true refresh_exported=false\n", r.Binding.Version)
	return nil
}
