package contract

import "testing"

func TestGraphResultClonePreservesNestedIdentityAndReceipt(t *testing.T) {
	r := GraphResult{Columns: []string{"path"}, Rows: [][]GraphValue{{{Kind: "path", Path: &GraphPath{Nodes: []GraphNode{{ElementID: "original", Labels: []string{"Person"}, Properties: []GraphProperty{{Name: "payload", Value: GraphValue{Kind: "list", Values: []GraphValue{{Kind: "integer", Text: "9223372036854775807"}}}}}}}, Relationships: []GraphRelationship{{ElementID: "edge", StartElementID: "original", EndElementID: "destination"}}}}}}, Summary: GraphSummary{Counters: map[string]int64{"nodes_created": 1}, Bookmarks: []string{"ack"}}, Completed: true}
	c := r.Clone()
	c.Columns[0] = "bad"
	c.Rows[0][0].Path.Nodes[0].ElementID = "bad"
	c.Rows[0][0].Path.Nodes[0].Labels[0] = "bad"
	c.Rows[0][0].Path.Nodes[0].Properties[0].Value.Values[0].Text = "bad"
	c.Rows[0][0].Path.Relationships[0].EndElementID = "bad"
	c.Summary.Counters["nodes_created"] = 99
	c.Summary.Bookmarks[0] = "bad"
	n := r.Rows[0][0].Path.Nodes[0]
	if r.Columns[0] != "path" || n.ElementID != "original" || n.Labels[0] != "Person" || n.Properties[0].Value.Values[0].Text != "9223372036854775807" || r.Rows[0][0].Path.Relationships[0].EndElementID != "destination" || r.Summary.Counters["nodes_created"] != 1 || r.Summary.Bookmarks[0] != "ack" {
		t.Fatal("graph copy aliases original")
	}
}
