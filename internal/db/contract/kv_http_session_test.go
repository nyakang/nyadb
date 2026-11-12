package contract

import "testing"

func TestHTTPSessionListCloneOwnsOriginalNestedSlices(t *testing.T) {
	original := KVHTTPResult{Sessions: &KVHTTPSessionList{Rows: []KVHTTPSession{{ID: "original", PresentFields: []string{"ID", "Name"}, NullFields: []string{"Name"}, Checks: []string{"legacy"}, NodeChecks: []string{"node"}, ServiceChecks: []KVHTTPSessionServiceCheck{{ID: "service", PresentFields: []string{"ID", "Namespace"}, NullFields: []string{"Namespace"}}}}}}}
	copy := original.Clone()
	row := &copy.Sessions.Rows[0]
	row.ID = "other"
	row.PresentFields[0] = "other"
	row.NullFields[0] = "other"
	row.Checks[0] = "other"
	row.NodeChecks[0] = "other"
	row.ServiceChecks[0].ID = "other"
	row.ServiceChecks[0].PresentFields[0] = "other"
	row.ServiceChecks[0].NullFields[0] = "other"
	row = &original.Sessions.Rows[0]
	if row.ID != "original" || row.PresentFields[0] != "ID" || row.NullFields[0] != "Name" || row.Checks[0] != "legacy" || row.NodeChecks[0] != "node" || row.ServiceChecks[0].ID != "service" || row.ServiceChecks[0].PresentFields[0] != "ID" || row.ServiceChecks[0].NullFields[0] != "Namespace" {
		t.Fatal("HTTP Session clone aliases original receipt projection")
	}
}
