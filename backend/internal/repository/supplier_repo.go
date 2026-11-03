package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type supplierRepository struct{ db *sql.DB }

func NewSupplierRepository(db *sql.DB) service.SupplierRepository { return &supplierRepository{db: db} }

const supplierColumns = `id,user_id,display_name,status,verified_email,email_verified_at,version,created_at,updated_at`

type supplierScanner interface{ Scan(...any) error }

func scanSupplier(row supplierScanner) (*service.SupplierProfile, error) {
	p := new(service.SupplierProfile)
	err := row.Scan(&p.ID, &p.UserID, &p.DisplayName, &p.Status, &p.VerifiedEmail, &p.EmailVerifiedAt, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}
func (r *supplierRepository) GetProfile(ctx context.Context, id int64) (*service.SupplierProfile, error) {
	p, err := scanSupplier(r.db.QueryRowContext(ctx, `SELECT `+supplierColumns+` FROM supplier_profiles WHERE user_id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupplierNotEnrolled
	}
	if err != nil {
		return nil, err
	}
	var valid bool
	err = r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND email=$2 AND status='active' AND deleted_at IS NULL)`, id, p.VerifiedEmail).Scan(&valid)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, service.ErrSupplierEmailChanged
	}
	return p, nil
}
func (r *supplierRepository) Enroll(ctx context.Context, id int64, email, name string) (*service.SupplierProfile, error) {
	// Lock the shared user row so a concurrent email change cannot certify the wrong address.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT email FROM users WHERE id=$1 AND status='active' AND deleted_at IS NULL FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) || err == nil && current != email {
		return nil, service.ErrSupplierEmailChanged
	}
	if err != nil {
		return nil, err
	}
	p, err := scanSupplier(tx.QueryRowContext(ctx, `INSERT INTO supplier_profiles(user_id,display_name,verified_email) VALUES($1,$2,$3)
 ON CONFLICT(user_id) DO UPDATE SET
 display_name=CASE WHEN supplier_profiles.verified_email<>EXCLUDED.verified_email THEN EXCLUDED.display_name ELSE supplier_profiles.display_name END,
 verified_email=EXCLUDED.verified_email,
 email_verified_at=CASE WHEN supplier_profiles.verified_email<>EXCLUDED.verified_email THEN CURRENT_TIMESTAMP ELSE supplier_profiles.email_verified_at END,
 version=supplier_profiles.version+CASE WHEN supplier_profiles.verified_email<>EXCLUDED.verified_email THEN 1 ELSE 0 END,
 updated_at=CASE WHEN supplier_profiles.verified_email<>EXCLUDED.verified_email THEN CURRENT_TIMESTAMP ELSE supplier_profiles.updated_at END
 RETURNING `+supplierColumns, id, name, email))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}
func (r *supplierRepository) UpdateProfile(ctx context.Context, id int64, name string, version int64) (*service.SupplierProfile, error) {
	p, err := scanSupplier(r.db.QueryRowContext(ctx, `UPDATE supplier_profiles p SET display_name=$2,version=p.version+1,updated_at=CURRENT_TIMESTAMP
 WHERE p.user_id=$1 AND p.version=$3 AND EXISTS(SELECT 1 FROM users u WHERE u.id=p.user_id AND u.email=p.verified_email AND u.status='active' AND u.deleted_at IS NULL)
 RETURNING `+supplierColumns, id, name, version))
	if errors.Is(err, sql.ErrNoRows) {
		if _, readErr := r.GetProfile(ctx, id); readErr != nil {
			return nil, readErr
		}
		return nil, service.ErrSupplierVersion
	}
	return p, err
}
