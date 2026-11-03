package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaintenanceFenceContextRequiresValidIdentity(t *testing.T) {
	parent := context.Background()
	_, ok := MaintenanceFenceFromContext(parent)
	require.False(t, ok)
	for _, f := range []MaintenanceFence{{}, {Generation: 1}, {Owner: "owner"}} {
		_, ok = MaintenanceFenceFromContext(ContextWithMaintenanceFence(parent, f))
		require.False(t, ok)
	}
	first := MaintenanceFence{Generation: 9007199254740993, Owner: "old-owner"}
	old := ContextWithMaintenanceFence(parent, first)
	next := ContextWithMaintenanceFence(parent, MaintenanceFence{Generation: first.Generation + 1, Owner: "new-owner"})
	got, ok := MaintenanceFenceFromContext(old)
	require.True(t, ok)
	require.Equal(t, first, got)
	later, ok := MaintenanceFenceFromContext(next)
	require.True(t, ok)
	require.NotEqual(t, got, later)
	_, ok = MaintenanceFenceFromContext(parent)
	require.False(t, ok)
}
