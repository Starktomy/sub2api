package service

import "context"

// MaintenanceFence is an immutable identity allocated by PostgreSQL while its
// dedicated session holds the maintenance advisory lock. It is not a Redis
// lease and does not make cross-store mutations atomic.
type MaintenanceFence struct {
	Generation int64
	Owner      string
}

func (f MaintenanceFence) Valid() bool { return f.Generation > 0 && f.Owner != "" }

// FencedMaintenanceSession is optional until all consumers are wired. Legacy
// sessions do not acquire generations, and may never authorize fenced tasks.
type FencedMaintenanceSession interface {
	MaintenanceSession
	Fence() MaintenanceFence
}

type maintenanceFenceContextKey struct{}

func ContextWithMaintenanceFence(ctx context.Context, fence MaintenanceFence) context.Context {
	return context.WithValue(ctx, maintenanceFenceContextKey{}, fence)
}

func MaintenanceFenceFromContext(ctx context.Context) (MaintenanceFence, bool) {
	fence, ok := ctx.Value(maintenanceFenceContextKey{}).(MaintenanceFence)
	return fence, ok && fence.Valid()
}
