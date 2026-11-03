package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAuthorizedOAuthRefreshRejectsRevokedPrincipalAtCommit(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "scoped", true: "full_key"}[full], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			repo := &accountRepository{client: client}
			ctx := adminaccess.WithPrincipal(context.Background(), &adminaccess.Principal{KeyID: "key", CreatorID: 9, Policy: adminaccess.Policy{FullAccess: full}})
			ctx = service.WithOAuthRefreshAuthorization(ctx, service.OAuthRefreshAuthorization{KeyHash: "fixture-hash", UpdatedAt: time.Now()})
			// A revoked/disabled/expired key no longer matches. No account update or
			// scheduler outbox may execute after this authorization failure.
			mock.ExpectQuery("SELECT key_hash, updated_at, policy FROM scoped_admin_api_keys.*FOR SHARE").WithArgs("key", int64(9)).WillReturnRows(sqlmock.NewRows([]string{"key_hash", "updated_at", "policy"}))
			require.ErrorIs(t, repo.authorizeOAuthRefresh(ctx, client, 42, true), service.ErrScopedAdminKeyInvalid)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAuthorizedOAuthRefreshRejectsRotatedKeyGenerationAtCommit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	repo := &accountRepository{client: client}
	policy := adminaccess.Policy{FullAccess: true}
	raw, err := json.Marshal(policy)
	require.NoError(t, err)
	now := time.Now()
	ctx := adminaccess.WithPrincipal(context.Background(), &adminaccess.Principal{KeyID: "key", CreatorID: 9, Policy: policy})
	ctx = service.WithOAuthRefreshAuthorization(ctx, service.OAuthRefreshAuthorization{KeyHash: "original-fixture-hash", UpdatedAt: now})
	mock.ExpectQuery("SELECT key_hash, updated_at, policy FROM scoped_admin_api_keys.*FOR SHARE").WithArgs("key", int64(9)).WillReturnRows(sqlmock.NewRows([]string{"key_hash", "updated_at", "policy"}).AddRow("rotated-fixture-hash", now.Add(time.Second), raw))
	require.ErrorIs(t, repo.authorizeOAuthRefresh(ctx, client, 42, true), service.ErrScopedAdminKeyInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}
