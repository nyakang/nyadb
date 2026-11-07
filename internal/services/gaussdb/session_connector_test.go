package gaussdb

import (
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestSessionProductValidationRejectsCompatibleForeignServers(t *testing.T) {
	for _, version := range []string{
		"PostgreSQL 15.1", "openGauss 6.0.0", "gaussdb (openGauss 6.0.0)",
		"gaussdb (GaussDB Kernel 502.9.9 build abc)", "gaussdb (GaussDB Kernel 503.0.9 build abc)",
		"gaussdb (GaussDB Kernel 503.1.0 build abc) DWS", "gaussdb (GaussDB Kernel 503.1.0 build abc) openGauss",
		"gaussdb (GaussDB Kernel 503.1.0)",
	} {
		if !errors.Is(validateVersion(version), contract.ErrUnsupported) {
			t.Fatal("foreign or unverified native server accepted", version)
		}
	}
	if err := validateVersion("gaussdb (GaussDB Kernel 503.1.0 build f53b4ed8) compiled at 2023-03-27"); err != nil {
		t.Fatal(err)
	}
}
