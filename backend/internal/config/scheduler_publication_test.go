package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSchedulerPublicationOptInConfig(t *testing.T) {
	for _, role := range []string{RuntimeRoleGateway, RuntimeRoleControl, RuntimeRoleWorker} {
		cfg := RuntimeConfig{Role: role, EnableSplitRoles: true, SchedulerPublications: true, MultiReplicaWorkers: role == RuntimeRoleWorker}
		require.NoError(t, cfg.Validate())
	}
	for _, cfg := range []RuntimeConfig{
		{Role: RuntimeRoleAll, SchedulerPublications: true},
		{Role: RuntimeRoleGateway, SchedulerPublications: true},
		{Role: RuntimeRoleWorker, EnableSplitRoles: true, SchedulerPublications: true},
	} {
		require.Error(t, cfg.Validate())
	}
	var legacy RuntimeConfig
	require.NoError(t, legacy.Validate())
	require.False(t, legacy.SchedulerPublications)
}
