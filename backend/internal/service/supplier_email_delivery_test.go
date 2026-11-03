package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type supplierSMTPSettings struct {
	SettingRepository
	values map[string]string
}

func (s supplierSMTPSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return s.values, nil
}
func TestSupplierEmailDeliveryConfiguredIsNotHealthProbe(t *testing.T) {
	values := map[string]string{}
	s := &EmailService{settingRepo: supplierSMTPSettings{values: values}}
	require.False(t, s.EmailDeliveryConfigured(context.Background()))
	require.ErrorIs(t, s.SendSupplierSignupCode(context.Background(), "u@example.test", "supplier"), ErrEmailNotConfigured)
	values[SettingKeySMTPHost] = "smtp.example.test"
	values[SettingKeySMTPFrom] = "noreply@example.test"
	require.True(t, s.EmailDeliveryConfigured(context.Background()))
	values[SettingKeySMTPUsername] = "operator"
	require.False(t, s.EmailDeliveryConfigured(context.Background()))
	values[SettingKeySMTPPassword] = "synthetic-not-used"
	require.True(t, s.EmailDeliveryConfigured(context.Background()))
}
