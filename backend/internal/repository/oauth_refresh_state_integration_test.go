//go:build integration

package repository

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Durable operations own their short transactions; these tests deliberately
// use the disposable container's non-transactional client, unlike legacy suite.
func (s *AccountRepoSuite) useDurableRefreshClient() {
	s.client = testEntClient(s.T())
	s.repo = newAccountRepositoryWithSQL(s.client, integrationDB, nil)
}

func (s *AccountRepoSuite) TestOAuthRefreshDurableFencingAndRecovery() {
	s.useDurableRefreshClient()
	a := mustCreateAccount(s.T(), s.client, &service.Account{Name: "durable-fence", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh"}})
	expected, err := s.repo.GetByID(s.ctx, a.ID)
	s.Require().NoError(err)
	attempt, err := s.repo.BeginOAuthRefresh(s.ctx, expected, time.Minute)
	s.Require().NoError(err)
	_, err = s.repo.BeginOAuthRefresh(s.ctx, expected, time.Minute)
	s.Require().ErrorIs(err, service.ErrOAuthRefreshPending)
	// Spoofed owner cannot commit. No remote OAuth is involved.
	forged := *attempt
	forged.Owner = "other-owner"
	applied, err := s.repo.CompleteOAuthRefresh(s.ctx, &forged, expected, map[string]any{"access_token": "forged"})
	s.Require().NoError(err)
	s.Require().False(applied)
	next := map[string]any{"access_token": "rotated", "refresh_token": "rotated-refresh"}
	applied, err = s.repo.CompleteOAuthRefresh(s.ctx, attempt, expected, next)
	s.Require().NoError(err)
	s.Require().True(applied)
	var before, after int
	s.Require().NoError(integrationDB.QueryRowContext(s.ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", a.ID).Scan(&before))
	// Simulate a lost COMMIT reply: retry only the identical local completion.
	applied, err = s.repo.CompleteOAuthRefresh(s.ctx, attempt, expected, next)
	s.Require().NoError(err)
	s.Require().True(applied)
	s.Require().NoError(integrationDB.QueryRowContext(s.ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", a.ID).Scan(&after))
	s.Require().Equal(before, after)
	current, err := s.repo.GetByID(s.ctx, a.ID)
	s.Require().NoError(err)
	nextAttempt, err := s.repo.BeginOAuthRefresh(s.ctx, current, time.Minute)
	s.Require().NoError(err)
	s.Require().Greater(nextAttempt.Generation, attempt.Generation)
	applied, err = s.repo.CompleteOAuthRefresh(s.ctx, attempt, expected, next)
	s.Require().NoError(err)
	s.Require().False(applied)
	s.Require().NoError(s.repo.MarkOAuthRefreshAmbiguous(s.ctx, nextAttempt))
	_, err = s.repo.BeginOAuthRefresh(s.ctx, current, time.Minute)
	s.Require().ErrorIs(err, service.ErrOAuthRefreshAmbiguous)
}

func (s *AccountRepoSuite) TestOAuthRefreshDurableRejectsPlatformTypeAndExpiredReplay() {
	s.useDurableRefreshClient()
	for _, change := range []string{"platform", "type", "expired"} {
		s.Run(change, func() {
			a := mustCreateAccount(s.T(), s.client, &service.Account{Name: "durable-" + change, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh"}})
			expected, err := s.repo.GetByID(s.ctx, a.ID)
			s.Require().NoError(err)
			attempt, err := s.repo.BeginOAuthRefresh(s.ctx, expected, time.Minute)
			s.Require().NoError(err)
			switch change {
			case "platform":
				_, err = integrationDB.ExecContext(s.ctx, "UPDATE accounts SET platform='gemini' WHERE id=$1", a.ID)
			case "type":
				_, err = integrationDB.ExecContext(s.ctx, "UPDATE accounts SET type='apikey' WHERE id=$1", a.ID)
			case "expired":
				_, err = integrationDB.ExecContext(s.ctx, "UPDATE oauth_refresh_state SET lease_until=NOW()-INTERVAL '1 second' WHERE account_id=$1", a.ID)
			}
			s.Require().NoError(err)
			if change == "expired" {
				_, err = s.repo.BeginOAuthRefresh(s.ctx, expected, time.Minute)
				s.Require().ErrorIs(err, service.ErrOAuthRefreshAmbiguous)
				// Original owner can salvage its known response, never acquire a new call.
				applied, e := s.repo.CompleteOAuthRefresh(s.ctx, attempt, expected, map[string]any{"access_token": "late", "refresh_token": "late-refresh"})
				s.Require().NoError(e)
				s.Require().True(applied)
			} else {
				applied, e := s.repo.CompleteOAuthRefresh(s.ctx, attempt, expected, map[string]any{"access_token": "must-not-write"})
				s.Require().NoError(e)
				s.Require().False(applied)
			}
		})
	}
}

func (s *AccountRepoSuite) TestOAuthRefreshCommitRechecksScopedAuthorization() {
	s.useDurableRefreshClient()
	for _, change := range []string{"success", "revoked", "rotated", "policy", "membership", "creator-disabled"} {
		s.Run(change, func() {
			ctx := s.ctx
			admin, err := s.client.User.Create().SetEmail(fmt.Sprintf("refresh-auth-%s-%d@test.invalid", change, time.Now().UnixNano())).SetPasswordHash("not-a-real-password").SetRole("admin").SetStatus("active").Save(ctx)
			s.Require().NoError(err)
			group, err := s.client.Group.Create().SetName(fmt.Sprintf("refresh-auth-%s-%d", change, time.Now().UnixNano())).Save(ctx)
			s.Require().NoError(err)
			a := mustCreateAccount(s.T(), s.client, &service.Account{Name: "auth-" + change, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"refresh_token": "synthetic-old"}})
			_, err = integrationDB.ExecContext(ctx, "INSERT INTO account_groups(account_id,group_id) VALUES ($1,$2)", a.ID, group.ID)
			s.Require().NoError(err)
			policy := adminaccess.Policy{Rules: []adminaccess.Rule{{Permissions: []string{"accounts.refresh"}, GroupIDs: []int64{group.ID}, AccountTypes: []string{"oauth"}, Platforms: []string{"openai"}}}}
			raw, err := json.Marshal(policy)
			s.Require().NoError(err)
			keyID := fmt.Sprintf("rf-%d", a.ID)
			_, err = integrationDB.ExecContext(ctx, "INSERT INTO scoped_admin_api_keys(id,name,key_prefix,key_hash,policy,created_by) VALUES ($1,'test','synthetic-prefix','synthetic-hash',$2::jsonb,$3)", keyID, string(raw), admin.ID)
			s.Require().NoError(err)
			ctx = adminaccess.WithPrincipal(ctx, &adminaccess.Principal{KeyID: keyID, CreatorID: admin.ID, Policy: policy})
			authorized, err := s.repo.PrepareOAuthRefreshAuthorization(ctx, a.ID)
			s.Require().NoError(err)
			expected, err := s.repo.GetByID(s.ctx, a.ID)
			s.Require().NoError(err)
			attempt, err := s.repo.BeginOAuthRefresh(authorized, expected, time.Minute)
			s.Require().NoError(err)
			// Mutations occur after the remote call would begin, proving no authorization
			// transaction remains open across that call and commit revalidation is live.
			switch change {
			case "revoked":
				_, err = integrationDB.ExecContext(s.ctx, "UPDATE scoped_admin_api_keys SET revoked_at=NOW() WHERE id=$1", keyID)
			case "rotated":
				_, err = integrationDB.ExecContext(s.ctx, "UPDATE scoped_admin_api_keys SET key_hash='replacement' WHERE id=$1", keyID)
			case "policy":
				_, err = integrationDB.ExecContext(s.ctx, "UPDATE scoped_admin_api_keys SET policy='{}'::jsonb WHERE id=$1", keyID)
			case "membership":
				_, err = integrationDB.ExecContext(s.ctx, "DELETE FROM account_groups WHERE account_id=$1", a.ID)
			case "creator-disabled":
				_, err = integrationDB.ExecContext(s.ctx, "UPDATE users SET status='disabled' WHERE id=$1", admin.ID)
			}
			s.Require().NoError(err)
			applied, err := s.repo.CompleteOAuthRefresh(authorized, attempt, expected, map[string]any{"refresh_token": "synthetic-new"})
			if change == "success" {
				s.Require().NoError(err)
				s.Require().True(applied)
			} else {
				s.Require().Error(err)
				s.Require().False(applied)
				var stored string
				s.Require().NoError(integrationDB.QueryRowContext(s.ctx, "SELECT credentials->>'refresh_token' FROM accounts WHERE id=$1", a.ID).Scan(&stored))
				s.Require().Equal("synthetic-old", stored)
			}
		})
	}
}

func (s *AccountRepoSuite) TestOAuthRefreshExplicitRecoveryAdvancesFence() {
	s.useDurableRefreshClient()
	a := mustCreateAccount(s.T(), s.client, &service.Account{Name: "explicit-recovery", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"refresh_token": "original"}})
	expected, err := s.repo.GetByID(s.ctx, a.ID)
	s.Require().NoError(err)
	attempt, err := s.repo.BeginOAuthRefresh(s.ctx, expected, time.Minute)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.MarkOAuthRefreshAmbiguous(s.ctx, attempt))
	_, err = integrationDB.ExecContext(s.ctx, "UPDATE accounts SET status='inactive',schedulable=false WHERE id=$1", a.ID)
	s.Require().NoError(err)
	paused, err := s.repo.GetByID(s.ctx, a.ID)
	s.Require().NoError(err)
	recovered, err := s.repo.ResolveOAuthRefreshAmbiguous(s.ctx, paused, attempt.Generation)
	s.Require().NoError(err)
	s.Require().False(recovered)
	// Merely editing metadata cannot clear uncertain consumption of the old token.
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, a.ID, map[string]any{"refresh_token": "original", "note": "changed"}))
	paused, err = s.repo.GetByID(s.ctx, a.ID)
	s.Require().NoError(err)
	recovered, err = s.repo.ResolveOAuthRefreshAmbiguous(s.ctx, paused, attempt.Generation)
	s.Require().NoError(err)
	s.Require().False(recovered)
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, a.ID, map[string]any{"refresh_token": "fresh-reauthorized"}))
	paused, err = s.repo.GetByID(s.ctx, a.ID)
	s.Require().NoError(err)
	recovered, err = s.repo.ResolveOAuthRefreshAmbiguous(s.ctx, paused, attempt.Generation+1)
	s.Require().NoError(err)
	s.Require().False(recovered)
	recovered, err = s.repo.ResolveOAuthRefreshAmbiguous(s.ctx, paused, attempt.Generation)
	s.Require().NoError(err)
	s.Require().True(recovered)
	applied, err := s.repo.CompleteOAuthRefresh(s.ctx, attempt, expected, map[string]any{"refresh_token": "late-old-response"})
	s.Require().NoError(err)
	s.Require().False(applied)
	newAttempt, err := s.repo.BeginOAuthRefresh(s.ctx, paused, time.Minute)
	s.Require().NoError(err)
	s.Require().Greater(newAttempt.Generation, attempt.Generation)
}
