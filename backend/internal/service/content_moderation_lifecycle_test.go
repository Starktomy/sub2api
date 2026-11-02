package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type moderationLifecycleRepo struct {
	ContentModerationRepository
	entered chan struct{}
	release <-chan struct{}
	mu      sync.Mutex
	logs    []string
}

func (r *moderationLifecycleRepo) CreateLog(_ context.Context, entry *ContentModerationLog) error {
	if entry.Endpoint == "first" {
		close(r.entered)
		<-r.release
	}
	r.mu.Lock()
	r.logs = append(r.logs, entry.Endpoint)
	r.mu.Unlock()
	return nil
}

func TestManagedModerationDefersAndDrainsAcceptedAuditTasks(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	repo := &moderationLifecycleRepo{entered: make(chan struct{}), release: release}
	settings := &contentModerationTestSettingRepo{values: map[string]string{}}
	svc := NewManagedContentModerationService(settings, repo, nil, nil, nil, nil, nil, nil)
	svc.workerCount = 1
	t.Cleanup(func() { unblock(); svc.Stop() })
	require.False(t, svc.started)
	require.False(t, svc.enqueueTask(contentModerationTask{}, nil), "unstarted managed service must not accept async work")
	svc.Start()
	ctx := svc.lifecycleCtx
	svc.Start()
	require.Equal(t, ctx, svc.lifecycleCtx, "Start must not replace running consumers")
	cfg := defaultContentModerationConfig()
	for _, endpoint := range []string{"first", "second"} {
		require.True(t, svc.enqueueTask(contentModerationTask{
			log: &ContentModerationLog{Endpoint: endpoint}, config: cfg,
			enqueuedAt: time.Now(),
		}, cfg))
	}
	select {
	case <-repo.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("accepted audit task did not reach repository")
	}
	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	<-ctx.Done()
	require.False(t, svc.enqueueTask(contentModerationTask{}, cfg), "Stop closes queue admission")
	select {
	case <-stopped:
		t.Fatal("Stop returned while an accepted audit still used its repository")
	default:
	}
	unblock()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not join all accepted audit work")
	}
	repo.mu.Lock()
	require.Equal(t, []string{"first", "second"}, repo.logs)
	repo.mu.Unlock()
	svc.Stop()
	svc.Start()
	require.True(t, svc.stopped)
}

func TestManagedModerationStopBeforeStartIsTerminal(t *testing.T) {
	svc := NewManagedContentModerationService(nil, nil, nil, nil, nil, nil, nil, nil)
	svc.Stop()
	svc.Stop()
	svc.Start()
	require.False(t, svc.started)
	require.True(t, svc.stopped)
	require.False(t, svc.enqueueTask(contentModerationTask{}, nil))
}
