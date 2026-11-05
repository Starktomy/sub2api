package repository

import (
	"context"
	"database/sql"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"fmt"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCompositeCandidateEntityRoundtrip(t *testing.T) {
	candidates := []service.CompositeRouteCandidate{{TargetPlatform: service.PlatformOpenAI, UpstreamModel: "oauth-alias", Enabled: true}, {TargetPlatform: service.PlatformOpenCodeGo, UpstreamModel: "code-alias", Enabled: false}}
	require.Equal(t, candidates, routeCandidatesFromEntity(routeCandidatesToEntity(candidates)))
	require.Nil(t, routeCandidatesFromEntity(nil))
	require.Nil(t, routeCandidatesToEntity(nil))
}

func TestCompositeCandidatePostgresCRUD(t *testing.T) {
	socket := os.Getenv("SUPPLIER_TEST_PG_SOCKET")
	if socket == "" {
		t.Skip("disposable PG required")
	}
	require.True(t, strings.HasPrefix(socket, "/tmp/supplier-pg-"))
	db, err := sql.Open("postgres", "host="+socket+" port=55439 user=postgres dbname=postgres sslmode=disable")
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := fmt.Sprintf("composite_candidates_%d", time.Now().UnixNano())
	_, err = db.ExecContext(ctx, `CREATE SCHEMA `+schema+`; SET search_path TO `+schema+`; CREATE TABLE groups(id BIGINT PRIMARY KEY); INSERT INTO groups VALUES(10)`)
	require.NoError(t, err)
	defer db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
	migration, err := os.ReadFile("../../migrations/172_composite_model_routes.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO composite_model_routes(group_id,public_model,target_platform,upstream_model) VALUES(10,'legacy','openai','legacy-upstream')`)
	require.NoError(t, err)
	migration, err = os.ReadFile("../../migrations/262_composite_route_candidates.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = db.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, `ALTER TABLE composite_model_routes DROP CONSTRAINT composite_model_routes_target_platform_check`)
	require.NoError(t, err) // mirrors applied migration242
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	repo := NewCompositeModelRouteRepository(client)
	rows, err := repo.ListByGroup(ctx, 10, true)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].Candidates)
	require.Equal(t, "legacy-upstream", rows[0].UpstreamModel)
	route := &service.CompositeModelRoute{GroupID: 10, PublicModel: "gpt6luna", MatchType: "exact", TargetPlatform: "openai", UpstreamModel: "oauth-alias", Endpoint: "chat_completions", Enabled: true, Priority: 100, Candidates: []service.CompositeRouteCandidate{{TargetPlatform: service.PlatformOpenAI, UpstreamModel: "oauth-alias", Enabled: true}, {TargetPlatform: service.PlatformOpenCodeGo, UpstreamModel: "code-alias", Enabled: true}}}
	require.NoError(t, repo.Create(ctx, route))
	require.Len(t, route.Candidates, 2)
	rows, err = repo.ListByGroup(ctx, 10, true)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	route.Candidates[0], route.Candidates[1] = route.Candidates[1], route.Candidates[0]
	route.Candidates[1].Enabled = false
	route.UpstreamModel = "code-alias"
	route.TargetPlatform = service.PlatformOpenCodeGo
	require.NoError(t, repo.Update(ctx, route))
	require.Equal(t, service.PlatformOpenCodeGo, route.Candidates[0].TargetPlatform)
	require.False(t, route.Candidates[1].Enabled)
	rows, err = repo.ListByGroup(ctx, 10, true)
	require.NoError(t, err)
	require.Equal(t, route.Candidates, rows[1].Candidates)
	require.NoError(t, repo.Delete(ctx, route.ID))
	rows, err = repo.ListByGroup(ctx, 10, true)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}
