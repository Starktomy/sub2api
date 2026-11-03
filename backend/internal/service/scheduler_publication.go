package service

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// SchedulerPublication identifies one never-reused immutable Redis namespace.
// Digest pins its manifest in PostgreSQL, independently of Redis recovery.
type SchedulerPublication struct {
	Generation int64
	ID         string
	Digest     string
}

type SchedulerPublicationData struct {
	Accounts []Account
	Buckets  map[string][]int64
}

// SchedulerPublicationCache separates cloud readers from legacy mutable keys.
// BindPublisher creates a new session-bound capability; it never rebinds a singleton.
type SchedulerPublicationCache interface {
	SchedulerCache
	ReadOnlyPublicationCache()
	BindPublisher(MaintenanceSession) (SchedulerPublicationPublisher, error)
}

type SchedulerPublicationPublisher interface {
	SchedulerOutboxRepository
	Healthy(context.Context) (bool, error)
	Collect(context.Context) error
	Stage(context.Context, SchedulerPublicationData) (SchedulerPublication, error)
	Activate(context.Context, SchedulerPublication, []int64) error
}

// RunPublicationCycle validates every required payload with an empty outbox,
// rebuilding only on missing/corrupt data or a different leader generation.
// A cycle owns no detached work: staging completes before activation, cancellation
// or session release. On ambiguous activation the next cycle uses a fresh ID.
func (s *SchedulerSnapshotService) RunPublicationCycle(ctx context.Context, publisher SchedulerPublicationPublisher) error {
	events, err := publisher.ListPendingAndReleaseDedup(ctx, 200)
	if err != nil {
		return err
	}
	// Sweep abandoned staging even if this cycle later fails to load/stage.
	if err := publisher.Collect(ctx); err != nil {
		return err
	}
	if len(events) == 0 {
		healthy, err := publisher.Healthy(ctx)
		if err != nil {
			return err
		}
		if healthy {
			return nil
		}
	}
	loader, ok := s.accountRepo.(SchedulerSnapshotLoader)
	if !ok {
		return errors.New("publication requires consistent snapshot loader")
	}
	var data SchedulerPublicationData
	err = loader.WithSchedulerSnapshot(ctx, func(accounts AccountRepository, groups GroupRepository) error {
		snapshot := NewSchedulerSnapshotService(nil, nil, accounts, groups, s.cfg)
		defer snapshot.Stop()
		var loadErr error
		data, loadErr = snapshot.loadPublicationData(ctx)
		return loadErr
	})
	if err != nil {
		return err
	}
	publication, err := publisher.Stage(ctx, data)
	if err != nil {
		return err
	}
	ids := make([]int64, len(events))
	for i := range events {
		ids[i] = events[i].ID
	}
	if err := publisher.Activate(ctx, publication, ids); err != nil {
		return err
	}
	return nil
}

func (s *SchedulerSnapshotService) PublicationTask(session MaintenanceSession) (RuntimeTask, error) {
	cache, ok := s.cache.(SchedulerPublicationCache)
	if !ok {
		return RuntimeTask{}, errors.New("publication worker requires immutable cache")
	}
	publisher, err := cache.BindPublisher(session)
	if err != nil {
		return RuntimeTask{}, err
	}
	// Each acquisition constructs an independent worker and capability.
	worker := NewSchedulerSnapshotService(s.cache, nil, s.accountRepo, s.groupRepo, s.cfg)
	return RuntimeTask{
		Name: "SchedulerPublicationService", Scope: RuntimeScopeWorker, RequiresMaintenanceFence: true,
		Start: func(ctx context.Context) error {
			worker.lifecycleMu.Lock()
			defer worker.lifecycleMu.Unlock()
			worker.cancel()
			worker.runCtx, worker.cancel = context.WithCancel(ctx)
			worker.started = true
			worker.wg.Add(1)
			go func() {
				defer worker.wg.Done()
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()
				for {
					cycle, cancel := context.WithTimeout(worker.runCtx, 2*time.Minute)
					if err := worker.RunPublicationCycle(cycle, publisher); err != nil && worker.runCtx.Err() == nil {
						slog.Warn("scheduler publication cycle failed; retaining PG authority and retrying fresh namespace", "error", err)
					}
					cancel()
					select {
					case <-worker.runCtx.Done():
						return
					case <-ticker.C:
					}
				}
			}()
			go func() { worker.wg.Wait(); close(worker.joined) }()
			return nil
		}, Stop: worker.StopContext,
	}, nil
}

// SchedulerSnapshotLoader binds all existing repository queries to one read-only snapshot.
type SchedulerSnapshotLoader interface {
	WithSchedulerSnapshot(context.Context, func(AccountRepository, GroupRepository) error) error
}

func (s *SchedulerSnapshotService) loadPublicationData(ctx context.Context) (SchedulerPublicationData, error) {
	groups, err := s.listActiveSchedulerGroupIDs(ctx)
	if err != nil {
		return SchedulerPublicationData{}, err
	}
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, "", "", "", "", 0, "")
	if err != nil {
		return SchedulerPublicationData{}, err
	}
	data := SchedulerPublicationData{Accounts: accounts, Buckets: make(map[string][]int64)}
	known := make(map[int64]bool, len(accounts))
	for _, account := range accounts {
		known[account.ID] = true
	}
	for _, group := range append([]int64{0}, groups...) {
		for _, bucket := range schedulerCanonicalBuckets(group) {
			members, err := s.loadAccountsFromDB(ctx, bucket, bucket.Mode == SchedulerModeMixed)
			if err != nil {
				return SchedulerPublicationData{}, err
			}
			ids := make([]int64, 0, len(members))
			for _, account := range members {
				if !known[account.ID] {
					return SchedulerPublicationData{}, errors.New("account changed during publication load; retry fresh cycle")
				}
				ids = append(ids, account.ID)
			}
			data.Buckets[bucket.String()] = ids
		}
	}
	return data, nil
}
