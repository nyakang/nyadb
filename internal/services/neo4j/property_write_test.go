package neo4j

import (
	"math"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func propertyRequestTest() contract.GraphPropertyWriteRequest {
	return contract.GraphPropertyWriteRequest{Namespace: contract.NamespaceRef{ProfileID: 42, Catalog: "neo4j"}, Target: contract.GraphPropertyTarget{Kind: "node", ElementID: "4:original", Labels: []string{"Person"}}, Property: "name` $malicious", Before: contract.GraphValue{Kind: "string", Text: "old"}, After: contract.GraphValue{Kind: "string", Text: "new ' SET e.bad=1 //"}}
}
func TestGraphPropertyWriteParametersTypesAndQualifiedTargets(t *testing.T) {
	r := propertyRequestTest()
	q, e := propertyWriteQuery(r)
	if e != nil || q.Query != nodePropertyWriteQuery || q.Mode != "write" || q.Parameters["key"] != r.Property || q.Parameters["after"] != r.After.Text || q.MaxRows != 2 || q.MaxBytes > 2<<20 {
		t.Fatal("unsafe native property request", q, e)
	}
	r.Target = contract.GraphPropertyTarget{Kind: "relationship", ElementID: "5:original", SourceID: "4:a", TargetID: "4:a", RelationshipType: "SELF"}
	if q, e = propertyWriteQuery(r); e != nil || q.Query != relationshipPropertyWriteQuery || q.Parameters["source"] != "4:a" || q.Parameters["target"] != "4:a" {
		t.Fatal("lost directed/self relationship identity", e)
	}
	for _, v := range []contract.GraphValue{{Kind: "integer", Text: "9223372036854775807"}, {Kind: "integer", Text: "-9223372036854775808"}, {Kind: "float", Text: "-0"}, {Kind: "float", Text: "1e-12"}, {Kind: "boolean", Text: "false"}, {Kind: "string", Text: ""}} {
		if _, _, e := propertyScalar(v); e != nil {
			t.Fatal("supported exact scalar rejected", v, e)
		}
	}
	for _, v := range []contract.GraphValue{{Kind: "integer", Text: "9223372036854775808"}, {Kind: "integer", Text: "01"}, {Kind: "integer", Text: "+1"}, {Kind: "boolean", Text: "TRUE"}, {Kind: "float", Text: "NaN"}, {Kind: "float", Text: "+Inf"}, {Kind: "float", Text: "1e1000"}, {Kind: "bytes", Text: "AA=="}, {Kind: "string", Text: "\xff"}, {Kind: "string", Values: []contract.GraphValue{{Kind: "null"}}}} {
		if _, _, e := propertyScalar(v); e == nil {
			t.Fatal("unsupported or malformed scalar accepted", v.Kind)
		}
	}
	for _, mutate := range []func(*contract.GraphPropertyWriteRequest){func(r *contract.GraphPropertyWriteRequest) { r.Target.Kind = "unknown" }, func(r *contract.GraphPropertyWriteRequest) { r.Target.ElementID = "" }, func(r *contract.GraphPropertyWriteRequest) { r.Target.Labels = []string{"duplicate", "duplicate"} }, func(r *contract.GraphPropertyWriteRequest) { r.Target.SourceID = "foreign" }, func(r *contract.GraphPropertyWriteRequest) { r.Property = "" }, func(r *contract.GraphPropertyWriteRequest) { r.After = contract.GraphValue{Kind: "float", Text: "1"} }} {
		r := propertyRequestTest()
		mutate(&r)
		if _, e := propertyWriteQuery(r); e == nil {
			t.Fatal("unqualified/mismatched mutation accepted")
		}
	}
	if n, _, e := propertyScalar(contract.GraphValue{Kind: "integer", Text: "9223372036854775807"}); e != nil || n != int64(math.MaxInt64) {
		t.Fatal("integer lost native precision")
	}
}
