package prestosql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestPublicMissingPackageReturnsNoSessionHandle(t *testing.T) {
	p, _ := connections.Defaults(connections.PrestoSQL)
	p.ID = 67
	p.Name = "missing package"
	p.PrestoSQLOptions.DriverVersion = "jdbc-350-plugin-0.1.57"
	p.PrestoSQLOptions.DriverSHA256 = strings.Repeat("a", 64)
	p.PrestoSQLOptions.DriverDirectory = t.TempDir()
	session, err := (Provider{}).Open(context.Background(), p, "")
	if session != nil || !errors.Is(err, ErrManagedDriver) {
		t.Fatal("failed public open exposed a typed nil handle", err)
	}
}
