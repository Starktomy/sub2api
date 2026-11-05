package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type supplierRecoveryRepoFake struct {
	SupplierDispatchRepository
	SupplierBillingRepairRepository
	mu           sync.Mutex
	repairs      []SupplierBillingRepair
	acknowledged []string
	recovered    int
	failRecovery bool
	failAck      bool
	block        chan struct{}
	entered      chan struct{}
	once         sync.Once
}

func (r *supplierRecoveryRepoFake) RecoverSupplierBilling(ctx context.Context, limit int) (SupplierRecoveryResult, error) {
	if limit != 20 {
		panic("unbounded recovery")
	}
	if r.block != nil {
		r.once.Do(func() { close(r.entered) })
		<-ctx.Done()
		return SupplierRecoveryResult{}, ctx.Err()
	}
	r.mu.Lock()
	r.recovered++
	r.mu.Unlock()
	if r.failRecovery {
		return SupplierRecoveryResult{}, errors.New("private database details")
	}
	return SupplierRecoveryResult{Replayed: 1}, nil
}
func (r *supplierRecoveryRepoFake) ListSupplierBillingRepairs(_ context.Context, limit int) ([]SupplierBillingRepair, error) {
	if limit != 20 {
		panic("unbounded repairs")
	}
	return r.repairs, nil
}
func (r *supplierRecoveryRepoFake) CompleteSupplierBillingRepair(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failAck {
		return errors.New("ack commit unknown")
	}
	r.acknowledged = append(r.acknowledged, id)
	return nil
}

type supplierRecoveryCacheFake struct {
	calls []string
	fail  bool
}

func (c *supplierRecoveryCacheFake) InvalidateUserBalance(context.Context, int64) error {
	c.calls = append(c.calls, "balance")
	if c.fail {
		return errors.New("redis unavailable")
	}
	return nil
}
func (c *supplierRecoveryCacheFake) InvalidateSubscription(context.Context, int64, int64) error {
	c.calls = append(c.calls, "subscription")
	return nil
}
func (c *supplierRecoveryCacheFake) InvalidateAPIKeyRateLimit(context.Context, int64) error {
	c.calls = append(c.calls, "key")
	return nil
}
func TestSupplierRecoveryRepairsOnlyAfterAllDeletes(t *testing.T) {
	r := &supplierRecoveryRepoFake{repairs: []SupplierBillingRepair{{AttemptID: "attempt", UserID: 1, APIKeyID: 2, AccountID: 3, GroupID: 4}}}
	c := &supplierRecoveryCacheFake{fail: true}
	w := &SupplierBillingRecoveryWorker{repo: r, repairs: r, cache: c}
	w.tick(context.Background())
	require.Empty(t, r.acknowledged)
	require.Equal(t, []string{"balance", "subscription", "key"}, c.calls)
	c.fail = false
	w.tick(context.Background())
	require.Equal(t, []string{"attempt"}, r.acknowledged)
	require.Equal(t, 2, r.recovered)
	// A repeat only deletes stale projections: no incremental cost operation exists.
	require.NoError(t, w.repair(context.Background(), r.repairs[0]))
	require.Len(t, c.calls, 9)
}
func TestSupplierRecoveryProcessesRepairsAfterReplayFailure(t *testing.T) {
	r := &supplierRecoveryRepoFake{failRecovery: true, repairs: []SupplierBillingRepair{{AttemptID: "attempt", UserID: 1, APIKeyID: 2, AccountID: 3, GroupID: 4}}}
	c := &supplierRecoveryCacheFake{}
	w := &SupplierBillingRecoveryWorker{repo: r, repairs: r, cache: c}
	w.tick(context.Background())
	require.Len(t, r.acknowledged, 1)
}
func TestSupplierRecoveryStopCancelsAndJoins(t *testing.T) {
	r := &supplierRecoveryRepoFake{block: make(chan struct{}), entered: make(chan struct{})}
	w := &SupplierBillingRecoveryWorker{repo: r, repairs: r, cache: &supplierRecoveryCacheFake{}, interval: time.Hour}
	require.NoError(t, w.Start(context.Background()))
	require.NoError(t, w.Start(context.Background()))
	select {
	case <-r.entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not enter recovery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, w.Stop(ctx))
	require.NoError(t, w.Stop(ctx))
}
func TestSupplierRecoveryMissingDependenciesFailClosed(t *testing.T) {
	w := NewSupplierBillingRecoveryWorker(nil, nil, nil)
	require.ErrorIs(t, w.Start(context.Background()), ErrSupplierDispatchUnavailable)
	require.NoError(t, w.Stop(context.Background()))
	require.Empty(t, MaintenanceTaskPolicy("SupplierBillingRecoveryWorker"))
}

func TestSupplierRecoveryAmbiguousRepairAckIsRetried(t *testing.T) {
	r := &supplierRecoveryRepoFake{failAck: true, repairs: []SupplierBillingRepair{{AttemptID: "attempt", UserID: 1, APIKeyID: 2, AccountID: 3, GroupID: 4}}}
	c := &supplierRecoveryCacheFake{}
	w := &SupplierBillingRecoveryWorker{repo: r, repairs: r, cache: c}
	w.tick(context.Background())
	require.Empty(t, r.acknowledged)
	r.failAck = false
	w.tick(context.Background())
	require.Equal(t, []string{"attempt"}, r.acknowledged)
	require.Len(t, c.calls, 6)
}

func TestSupplierRecoveryProviderRegistersOnlyOptedInWorker(t *testing.T) {
	for _, role := range []string{config.RuntimeRoleAll, config.RuntimeRoleGateway, config.RuntimeRoleControl, config.RuntimeRoleWorker} {
		for _, enabled := range []bool{false, true} {
			cfg := &config.Config{
				Runtime:       config.RuntimeConfig{Role: role, EnableSplitRoles: true},
				Database:      config.DatabaseConfig{MigrationMode: config.DatabaseMigrationModeCheck},
				SupplierProbe: config.SupplierProbeConfig{Enabled: enabled, IntervalSeconds: 600},
			}
			lifecycle := NewRuntimeLifecycle(cfg)
			ProvideSupplierProbeWorker(nil, nil, cfg, lifecycle, nil, nil, nil)
			var recovery *RuntimeTask
			for i := range lifecycle.tasks {
				if lifecycle.tasks[i].Name == "SupplierBillingRecoveryWorker" {
					recovery = &lifecycle.tasks[i]
				}
			}
			if role != config.RuntimeRoleWorker {
				require.Nil(t, recovery)
				continue
			}
			require.NotNil(t, recovery, "recovery must remain active with probes disabled")
			require.Equal(t, RuntimeScopeWorker, recovery.Scope)
			require.Equal(t, RuntimeStopConsumers, recovery.StopPhase)
			require.ErrorIs(t, recovery.Start(context.Background()), ErrSupplierDispatchUnavailable)
		}
	}
	for _, cfg := range []*config.Config{
		{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleWorker}, Database: config.DatabaseConfig{MigrationMode: config.DatabaseMigrationModeCheck}},
		{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleWorker, EnableSplitRoles: true}},
	} {
		lifecycle := NewRuntimeLifecycle(cfg)
		ProvideSupplierProbeWorker(nil, nil, cfg, lifecycle, nil, nil, nil)
		require.Empty(t, lifecycle.tasks)
	}
}

func TestSupplierRecoveryLogsNoRawFailure(t *testing.T) {
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(old)
	r := &supplierRecoveryRepoFake{failRecovery: true}
	w := &SupplierBillingRecoveryWorker{repo: r, repairs: r, cache: &supplierRecoveryCacheFake{}}
	w.tick(context.Background())
	require.Contains(t, output.String(), "supplier billing recovery failed")
	require.NotContains(t, output.String(), "private database details")
}
