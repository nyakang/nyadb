package connections_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSolrPublicDescriptorPreservesNativeCapabilities(t *testing.T) {
	d, ok := connections.Describe(connections.Solr)
	if !ok || d.Label != "Apache Solr" || d.ClientFamily != "solr" || d.Dialect != "solr" || d.FormKind != "server" || d.DefaultPort != 8983 {
		t.Fatal("native Solr product identity", d)
	}
	c := d.Capabilities
	if !c.Objects || !c.Documents || !c.Query || !c.Metadata || !c.EditableTables || !c.Keyset || !c.Count || !c.FieldChunks || !c.Export || !c.JSONL || c.Transactions || c.Import || c.DesignTables || c.DesignViews || c.Maintenance || c.Accounts || c.Backup || c.Restore || c.Explain {
		t.Fatal("unverified Solr product capabilities", c)
	}
	d.Capabilities.Query = false
	current, _ := connections.Describe(connections.Solr)
	if !current.Capabilities.Query || !connections.Solr.IsRESTSearch() || connections.Solr.IsMySQLFamily() || connections.Solr.IsPostgreSQLFamily() {
		t.Fatal("Solr descriptor leaked mutation or inherited a wire family")
	}
}
