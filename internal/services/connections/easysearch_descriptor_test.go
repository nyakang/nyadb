package connections_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestEasysearchPublicDescriptorKeepsNativeProductCapabilities(t *testing.T) {
	d, registered := connections.Describe(connections.Easysearch)
	if !registered || d.Kind != connections.Easysearch || d.Label != "Easysearch" || d.ClientFamily != "easysearch" || d.Dialect != "easysearch" || d.DefaultPort != 9200 || d.FormKind != "server" {
		t.Fatal("independent public product descriptor missing")
	}
	c := d.Capabilities
	if !c.Documents || !c.Query || !c.Metadata || !c.EditableTables || !c.Count || !c.FieldChunks || !c.Keyset || !c.Export || !c.JSONL || c.Transactions || c.Import || c.Explain || c.Analysis || c.DesignTables || c.Accounts || c.Backup || c.Restore || c.Maintenance {
		t.Fatal("public descriptor advertises unsupported relational or administration features", c)
	}
	d.Label, d.Capabilities.Query = "changed", false
	current, _ := connections.Describe(connections.Easysearch)
	if current.Label != "Easysearch" || !current.Capabilities.Query || !connections.Easysearch.IsRESTSearch() || connections.Easysearch.IsMySQLFamily() || connections.Easysearch.IsPostgreSQLFamily() {
		t.Fatal("descriptor mutation or unrelated client family changed the product")
	}
}
