package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMultiReplicaWorkerGate(t *testing.T) {
	for _, role := range []string{RuntimeRoleAll, RuntimeRoleGateway, RuntimeRoleControl} {
		cfg := RuntimeConfig{Role: role, EnableSplitRoles: true, MultiReplicaWorkers: true}
		require.Error(t, cfg.Validate())
	}
	cfg := RuntimeConfig{Role: RuntimeRoleWorker, EnableSplitRoles: true, MultiReplicaWorkers: true}
	require.NoError(t, cfg.Validate())
	cfg.EnableSplitRoles = false
	require.Error(t, cfg.Validate())
}
