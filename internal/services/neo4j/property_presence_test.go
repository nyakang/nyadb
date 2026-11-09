package neo4j

import (
	"math"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestGraphPropertyPresenceParametersAndAbsentReceipts(t *testing.T) {
	null := contract.GraphValue{Kind: "null"}
	r := propertyRequestTest()
	r.Before, r.After = null, contract.GraphValue{Kind: "integer", Text: "-9223372036854775808"}
	q, err := propertyWriteQuery(r)
	if err != nil || q.Parameters["type"] != "NULL" || q.Parameters["before"] != nil || q.Parameters["after"] != int64(math.MinInt64) {
		t.Fatal("absent typed addition lost native NULL or exact integer", err)
	}
	r.Before, r.After = contract.GraphValue{Kind: "string", Text: ""}, null
	q, err = propertyWriteQuery(r)
	if err != nil || q.Parameters["type"] != "STRING NOT NULL" || q.Parameters["before"] != "" || q.Parameters["after"] != nil {
		t.Fatal("removal confused a present empty string with absence", err)
	}
	for _, pair := range [][2]contract.GraphValue{{null, null}, {{Kind: "null", Text: "NULL"}, {Kind: "string"}}, {{Kind: "integer", Text: "1"}, {Kind: "float", Text: "1"}}, {{Kind: "list", Text: "untyped"}, null}} {
		r.Before, r.After = pair[0], pair[1]
		if _, err := propertyWriteQuery(r); err == nil {
			t.Fatal("unqualified presence/type mutation admitted", pair)
		}
	}
	node := contract.GraphValue{Kind: "node", Node: &contract.GraphNode{ElementID: r.Target.ElementID, Labels: r.Target.Labels, Properties: []contract.GraphProperty{{Name: "other", Value: contract.GraphValue{Kind: "boolean", Text: "false"}}}}}
	if !propertyReceiptEntity(node, r.Target, r.Property, null) {
		t.Fatal("complete absent-property receipt rejected")
	}
	node.Node.Properties = append(node.Node.Properties, contract.GraphProperty{Name: r.Property, Value: null})
	if propertyReceiptEntity(node, r.Target, r.Property, null) {
		t.Fatal("a stored NULL was treated as the confirmed removal of a property")
	}
}
