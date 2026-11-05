package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type supplierInventoryFake struct{ SupplierResourceRepository }

func (supplierInventoryFake) ListAdminResources(context.Context) ([]AdminSupplierResource, error) {
	return []AdminSupplierResource{{SupplierName: "supplier", SupplierUserID: 7, Resource: &SupplierResource{Envelope: []byte("secret-envelope"), AccountHint: "secret-account", TokenFingerprint: "secret-fingerprint", Email: "supplier-account@example.test", MaskedEmail: "s***@example.test", Status: "disconnected"}}}, nil
}
func TestSupplierAdminInventoryRedactsCredentials(t *testing.T) {
	s := &SupplierResourceService{repo: supplierInventoryFake{}}
	v, err := s.ListAdminResources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"email":"supplier-account@example.test"`) {
		t.Fatal("full email missing from credential-free admin inventory")
	}
	if strings.Contains(string(b), "secret-") {
		t.Fatal("credential metadata leaked")
	}
	if v[0].Resource.Schedulable {
		t.Fatal("inventory silently admitted resource")
	}
}
