package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompositeRouteTargetsMigration(t *testing.T) {
	content, err := FS.ReadFile("247_composite_route_targets.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS targets JSONB NOT NULL DEFAULT '[]'::jsonb")
	require.NotContains(t, sql, "CREATE INDEX", "targets are read as a whole and do not need a GIN index")
}
