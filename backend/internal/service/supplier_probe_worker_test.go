package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSupplierProbeWorkerBackoffAndMissingKey(t *testing.T) {
	require.Equal(t, time.Minute, probeBackoff(0))
	require.Equal(t, 64*time.Minute, probeBackoff(99))
	w := NewSupplierProbeWorker(nil, &SupplierResourceService{}, &config.Config{SupplierProbe: config.SupplierProbeConfig{IntervalSeconds: 600}})
	require.ErrorIs(t, w.Start(context.Background()), ErrSupplierVault)
	require.NoError(t, w.Stop(context.Background()))
}

type supplierProbeCancelRepo struct {
	SupplierProbeRepository
	entered chan struct{}
}

func (r supplierProbeCancelRepo) Reconcile(ctx context.Context) error {
	close(r.entered)
	<-ctx.Done()
	return ctx.Err()
}
func TestSupplierProbeWorkerStopsAndJoins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring")
	require.NoError(t, os.WriteFile(path, []byte(`{"keys":{"v1":"`+strings.Repeat("11", 32)+`"}}`), 0600))
	cfg := &config.Config{SupplierPortal: config.SupplierPortalConfig{VaultKeyFile: path, VaultKeyID: "v1"}, SupplierProbe: config.SupplierProbeConfig{IntervalSeconds: 600}}
	resources := NewSupplierResourceService(nil, nil, nil, cfg)
	repo := supplierProbeCancelRepo{entered: make(chan struct{})}
	worker := NewSupplierProbeWorker(repo, resources, cfg)
	require.NoError(t, worker.Start(context.Background()))
	select {
	case <-repo.entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	stop, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, worker.Stop(stop))
}
