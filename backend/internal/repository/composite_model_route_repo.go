package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/compositemodelroute"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type compositeModelRouteRepository struct {
	client *dbent.Client
}

func NewCompositeModelRouteRepository(client *dbent.Client) service.CompositeModelRouteRepository {
	return &compositeModelRouteRepository{client: client}
}

func (r *compositeModelRouteRepository) ListByGroup(ctx context.Context, groupID int64, includeDisabled bool) ([]service.CompositeModelRoute, error) {
	q := clientFromContext(ctx, r.client).CompositeModelRoute.Query().
		Where(compositemodelroute.GroupIDEQ(groupID)).
		Order(
			dbent.Asc(compositemodelroute.FieldPriority),
			dbent.Asc(compositemodelroute.FieldID),
		)
	if !includeDisabled {
		q = q.Where(compositemodelroute.EnabledEQ(true))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.CompositeModelRoute, 0, len(rows))
	for _, row := range rows {
		out = append(out, *compositeModelRouteEntityToService(row))
	}
	return out, nil
}

func (r *compositeModelRouteRepository) Create(ctx context.Context, route *service.CompositeModelRoute) error {
	if route == nil {
		return service.ErrCompositeRouteNotFound
	}
	created, err := clientFromContext(ctx, r.client).CompositeModelRoute.Create().
		SetCandidates(routeCandidatesToEntity(route.Candidates)).
		SetGroupID(route.GroupID).
		SetPublicModel(route.PublicModel).
		SetMatchType(route.MatchType).
		SetTargetPlatform(route.TargetPlatform).
		SetUpstreamModel(route.UpstreamModel).
		SetEndpoint(route.Endpoint).
		SetPriority(route.Priority).
		SetEnabled(route.Enabled).
		SetNotes(route.Notes).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, nil, service.ErrCompositeRouteExists)
	}
	*route = *compositeModelRouteEntityToService(created)
	return nil
}

func (r *compositeModelRouteRepository) Update(ctx context.Context, route *service.CompositeModelRoute) error {
	if route == nil {
		return service.ErrCompositeRouteNotFound
	}
	updated, err := clientFromContext(ctx, r.client).CompositeModelRoute.UpdateOneID(route.ID).
		SetCandidates(routeCandidatesToEntity(route.Candidates)).
		SetPublicModel(route.PublicModel).
		SetMatchType(route.MatchType).
		SetTargetPlatform(route.TargetPlatform).
		SetUpstreamModel(route.UpstreamModel).
		SetEndpoint(route.Endpoint).
		SetPriority(route.Priority).
		SetEnabled(route.Enabled).
		SetNotes(route.Notes).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrCompositeRouteNotFound, service.ErrCompositeRouteExists)
	}
	*route = *compositeModelRouteEntityToService(updated)
	return nil
}

func (r *compositeModelRouteRepository) Delete(ctx context.Context, id int64) error {
	err := clientFromContext(ctx, r.client).CompositeModelRoute.DeleteOneID(id).Exec(ctx)
	return translatePersistenceError(err, service.ErrCompositeRouteNotFound, nil)
}

func (r *compositeModelRouteRepository) DeleteByGroup(ctx context.Context, groupID int64) error {
	_, err := clientFromContext(ctx, r.client).CompositeModelRoute.Delete().
		Where(compositemodelroute.GroupIDEQ(groupID)).
		Exec(ctx)
	return err
}

func compositeModelRouteEntityToService(row *dbent.CompositeModelRoute) *service.CompositeModelRoute {
	if row == nil {
		return nil
	}
	return &service.CompositeModelRoute{
		Candidates:     routeCandidatesFromEntity(row.Candidates),
		ID:             row.ID,
		GroupID:        row.GroupID,
		PublicModel:    row.PublicModel,
		MatchType:      row.MatchType,
		TargetPlatform: row.TargetPlatform,
		UpstreamModel:  row.UpstreamModel,
		Endpoint:       row.Endpoint,
		Priority:       row.Priority,
		Enabled:        row.Enabled,
		Notes:          derefString(row.Notes),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func routeCandidatesToEntity(c []service.CompositeRouteCandidate) []map[string]any {
	if c == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(c))
	for _, source := range c {
		out = append(out, map[string]any{"target_platform": source.TargetPlatform, "upstream_model": source.UpstreamModel, "enabled": source.Enabled})
	}
	return out
}
func routeCandidatesFromEntity(c []map[string]any) []service.CompositeRouteCandidate {
	if c == nil {
		return nil
	}
	out := make([]service.CompositeRouteCandidate, 0, len(c))
	for _, source := range c {
		platform, _ := source["target_platform"].(string)
		model, _ := source["upstream_model"].(string)
		enabled, _ := source["enabled"].(bool)
		out = append(out, service.CompositeRouteCandidate{TargetPlatform: platform, UpstreamModel: model, Enabled: enabled})
	}
	return out
}
