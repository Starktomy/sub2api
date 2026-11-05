package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// AdmissionState is a metadata projection, not authorization for later execution.
// GetExecutionResource repeats the authoritative checks for each actual request.
func (r *supplierResourceRepository) AdmissionState(ctx context.Context, owner int64, id string) (int64, bool, error) {
	var accountID int64
	err := r.db.QueryRowContext(ctx, `SELECT account_id FROM supplier_account_bindings WHERE supplier_id=$1 AND resource_id=$2`, owner, id).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	_, err = r.GetExecutionResource(ctx, accountID, id)
	if errors.Is(err, service.ErrSupplierResource) {
		return accountID, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return accountID, true, nil
}
