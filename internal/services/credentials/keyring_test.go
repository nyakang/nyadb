package credentials

import (
	"testing"

	keyring "github.com/zalando/go-keyring"
)

func TestKeyringRoundTripAndSeparateConfigurations(t *testing.T) {
	keyring.MockInit()
	v := NewOSVault()
	if _, found, err := v.Get(9001, ""); err != nil || found {
		t.Fatalf("missing primary secret: found=%t err=%v", found, err)
	}
	if err := v.Set(9001, "", "primary-only"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set(9001, "测试/备份", "variant-only"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		profile       int64
		variant, want string
	}{
		{9001, "", "primary-only"},
		{9001, "测试/备份", "variant-only"},
	} {
		secret, found, err := v.Get(tc.profile, tc.variant)
		if err != nil || !found || secret != tc.want {
			t.Fatalf("keyring round trip failed: found=%t err=%v", found, err)
		}
	}
	if _, found, err := v.Get(9002, ""); err != nil || found {
		t.Fatal("credential leaked across profiles")
	}
	if err := v.Delete(9001, ""); err != nil {
		t.Fatal(err)
	}
	if _, found, err := v.Get(9001, ""); err != nil || found {
		t.Fatal("primary secret not deleted")
	}
	secret, found, err := v.Get(9001, "测试/备份")
	if err != nil || !found || secret != "variant-only" {
		t.Fatal("deleting primary removed a variant's secret")
	}
}

func TestKeyringRefusesInvalidIdentifiersAndEmptySecret(t *testing.T) {
	keyring.MockInit()
	v := NewOSVault()
	if err := v.Set(1, "", ""); err == nil {
		t.Fatal("empty secret was silently accepted")
	}
	if err := v.Set(0, "", "x"); err == nil {
		t.Fatal("invalid profile identity was accepted")
	}
}
