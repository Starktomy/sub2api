package repository

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *supplierResourceRepository) ListAdminResources(ctx context.Context) ([]service.AdminSupplierResource, error) {
	// Read only redacted metadata. Never select envelope, token fingerprints or
	// account identity hints into the admin listing.
	rows, err := r.db.QueryContext(ctx, `SELECT r.id,r.supplier_id,r.display_name,r.status,r.version,r.masked_email,r.plan_type,r.token_expires_at,r.last_usage_at,r.created_at,r.updated_at,r.auto_sync_enabled,r.probe_state,r.last_probe_at,r.next_probe_at,r.probe_error_code,r.usage,r.supply_enabled,r.supply_enable_pending,r.admin_disabled,r.consent_version,r.authorization_version,r.authorization_expires_at,r.authorization_revoked_at,r.last_verified_at,r.verified_credential_version,r.credential_version,p.display_name,p.user_id FROM supplier_resources r JOIN supplier_profiles p ON p.id=r.supplier_id ORDER BY r.created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []service.AdminSupplierResource{}
	for rows.Next() {
		v := service.AdminSupplierResource{Resource: new(service.SupplierResource)}
		x := v.Resource
		var usage []byte
		if err = rows.Scan(&x.ID, &x.SupplierID, &x.DisplayName, &x.Status, &x.Version, &x.MaskedEmail, &x.PlanType, &x.TokenExpiresAt, &x.LastUsageAt, &x.CreatedAt, &x.UpdatedAt, &x.AutoSyncEnabled, &x.ProbeState, &x.LastProbeAt, &x.NextProbeAt, &x.ProbeErrorCode, &usage, &x.SupplyEnabled, &x.SupplyEnablePending, &x.AdminDisabled, &x.ConsentVersion, &x.AuthorizationVersion, &x.AuthorizationExpiresAt, &x.AuthorizationRevokedAt, &x.LastVerifiedAt, &x.VerifiedCredentialVersion, &x.CredentialVersion, &v.SupplierName, &v.SupplierUserID); err != nil {
			return nil, err
		}
		if len(usage) > 0 {
			if err := json.Unmarshal(usage, &x.Usage); err != nil {
				return nil, err
			}
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
