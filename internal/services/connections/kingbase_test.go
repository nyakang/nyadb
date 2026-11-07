package connections_test

import (
	"encoding/json"
	"fmt"
	"github.com/nyakang/nyadb/internal/services/connections"
	"strings"
	"testing"
)

func TestKingbaseNativeProfileURIBoundaries(t *testing.T) {
	for _, uri := range []string{"kingbase://reader:private%40secret@host/test?sslmode=require", "KINGBASE://reader:private%40secret@[::1]:54322/test"} {
		imported, err := connections.ParseURI(uri)
		if err != nil || imported.Profile.Kind != connections.Kingbase || imported.Password != "private@secret" || imported.Profile.Database != "test" {
			t.Fatal("native URI identity", err)
		}
		raw, _ := json.Marshal(imported.Profile)
		if strings.Contains(string(raw), "private") || strings.Contains(fmt.Sprintf("%+v", imported), "private") {
			t.Fatal("secret escaped URI draft")
		}
	}
	p, _ := connections.Defaults(connections.Kingbase)
	if p.Port != 54321 || p.Database != "test" || p.Username != "system" || p.TLSMode != "verify-full" || !p.Kind.IsPostgreSQLFamily() || p.Kind.IsGaussFamily() {
		t.Fatal("native defaults or family")
	}
	d, _ := connections.Describe(p.Kind)
	if !d.Capabilities.Query || !d.Capabilities.Import || !d.Capabilities.Explain || d.Capabilities.Objects || d.Capabilities.DesignTables || d.Capabilities.DesignViews || d.Capabilities.Accounts || d.Capabilities.Backup || d.Capabilities.Restore || d.Capabilities.Maintenance {
		t.Fatal("unverified capability advertised")
	}
	for _, uri := range []string{"kingbase://reader@host", "kingbase://host/test", "kingbase://reader:private@host:0/test", "kingbase://reader@host/test?sslmode=prefer", "kingbase://reader@host/test?sslmode=require&sslmode=disable", "kingbase://reader@host/test?unknown=private", "kingbase://reader@host/test/other", "kingbase://reader@host/test#private"} {
		_, err := connections.ParseURI(uri)
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid URI accepted or secret leaked", uri)
		}
	}
}
