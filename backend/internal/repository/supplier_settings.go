package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *settingRepository) CompareAndSetSupplierPolicy(ctx context.Context, old, new string) error {
	const key = "supplier_operating_policy_v1"
	if old == "" {
		_, err := r.client.Setting.Create().SetKey(key).SetValue(new).Save(ctx)
		if err != nil {
			if ent.IsConstraintError(err) {
				return service.ErrSupplierPolicyVersion
			}
			return err
		}
		return nil
	}
	n, err := r.client.Setting.Update().Where(setting.KeyEQ(key), setting.ValueEQ(old)).SetValue(new).SetUpdatedAt(time.Now()).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrSupplierPolicyVersion
	}
	return nil
}
