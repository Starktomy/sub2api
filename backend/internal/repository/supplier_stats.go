package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *supplierResourceRepository) ResourceStats(ctx context.Context, owner int64, id string) (*service.SupplierResourceStats, error) {
	v := &service.SupplierResourceStats{Currency: "USD"}
	err := r.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(input_tokens),0),coalesce(sum(output_tokens),0),coalesce(sum(cache_read_tokens),0),coalesce(sum(cache_creation_tokens),0),coalesce(sum(fee_amount),0)::text FROM supplier_usage WHERE supplier_id=$1 AND resource_id=$2`, owner, id).Scan(&v.Calls, &v.InputTokens, &v.OutputTokens, &v.CacheReadTokens, &v.CacheCreationTokens, &v.FeeAmount)
	return v, err
}
