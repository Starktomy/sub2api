package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func supplierRows() *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows([]string{"id", "user_id", "display_name", "status", "verified_email", "email_verified_at", "version", "created_at", "updated_at"}).AddRow(1, 7, "name", "active", "s@example.com", now, 1, now, now)
}
func TestSupplierRepositoryEnrollmentLocksCurrentEmail(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	r := &supplierRepository{db}
	m.ExpectBegin()
	m.ExpectQuery("SELECT email FROM users.*FOR UPDATE").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("changed@example.com"))
	m.ExpectRollback()
	_, err = r.Enroll(context.Background(), 7, "s@example.com", "name")
	require.ErrorIs(t, err, service.ErrSupplierEmailChanged)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestSupplierRepositoryEnrollmentIdempotentSQL(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	r := &supplierRepository{db}
	for i := 0; i < 2; i++ {
		m.ExpectBegin()
		m.ExpectQuery("SELECT email FROM users.*FOR UPDATE").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("s@example.com"))
		m.ExpectQuery("INSERT INTO supplier_profiles.*ON CONFLICT").WithArgs(int64(7), "name", "s@example.com").WillReturnRows(supplierRows())
		m.ExpectCommit()
		p, err := r.Enroll(context.Background(), 7, "s@example.com", "name")
		require.NoError(t, err)
		require.Equal(t, int64(1), p.Version)
	}
	require.NoError(t, m.ExpectationsWereMet())
}
func TestSupplierRepositoryCASConflict(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	r := &supplierRepository{db}
	m.ExpectQuery("UPDATE supplier_profiles.*p.version=\\$3").WithArgs(int64(7), "name", int64(1)).WillReturnError(sql.ErrNoRows)
	m.ExpectQuery("SELECT .* FROM supplier_profiles WHERE user_id").WithArgs(int64(7)).WillReturnRows(supplierRows())
	m.ExpectQuery("SELECT EXISTS").WithArgs(int64(7), "s@example.com").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	_, err = r.UpdateProfile(context.Background(), 7, "name", 1)
	require.ErrorIs(t, err, service.ErrSupplierVersion)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestSupplierRepositoryRejectsStaleEmail(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	r := &supplierRepository{db}
	m.ExpectQuery("SELECT .* FROM supplier_profiles WHERE user_id").WithArgs(int64(7)).WillReturnRows(supplierRows())
	m.ExpectQuery("SELECT EXISTS").WithArgs(int64(7), "s@example.com").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	p, err := r.GetProfile(context.Background(), 7)
	require.Nil(t, p)
	require.ErrorIs(t, err, service.ErrSupplierEmailChanged)
	require.NoError(t, m.ExpectationsWereMet())
}
