//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestSupplierStatsAggregateOwnerFilter(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(`SELECT count\(\*\).*FROM supplier_usage WHERE supplier_id=\$1 AND resource_id=\$2`).WithArgs(int64(7), "resource").WillReturnRows(sqlmock.NewRows([]string{"calls", "input", "output", "read", "creation", "fee"}).AddRow(3, 100, 20, 40, 5, "0.00005469"))
	got, err := (&supplierResourceRepository{db: db}).ResourceStats(context.Background(), 7, "resource")
	require.NoError(t, err)
	require.Equal(t, int64(3), got.Calls)
	require.Equal(t, int64(40), got.CacheReadTokens)
	require.Equal(t, "0.00005469", got.FeeAmount)
	require.Equal(t, "USD", got.Currency)
	require.NoError(t, mock.ExpectationsWereMet())
}
