package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestKiroVaultExplicitIndependentConfiguration(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.ValidateSupplierPortal())
	c.KiroVault.VaultKeyFile = "/tmp/fake-keyring"
	require.Error(t, c.ValidateSupplierPortal())
	c.KiroVault.VaultKeyID = "fake-key"
	require.NoError(t, c.ValidateSupplierPortal())
	c.KiroVault.UseSupplierVault = true
	require.Error(t, c.ValidateSupplierPortal())
	c.KiroVault = KiroVaultConfig{UseSupplierVault: true}
	require.Error(t, c.ValidateSupplierPortal())
	c.SupplierPortal.VaultKeyFile = "/tmp/explicit-alias-keyring"
	c.SupplierPortal.VaultKeyID = "fake-alias"
	require.NoError(t, c.ValidateSupplierPortal())
}
