package neo4j

import (
	"reflect"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func propertyListTest(t *testing.T, kind, text string) contract.GraphValue {
	t.Helper()
	v, err := contract.ParseGraphPropertyList(kind, text)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestGraphPropertyListParametersTypesEmptyTransitionsAndCapture(t *testing.T) {
	r := propertyRequestTest()
	r.Before, r.After = propertyListTest(t, "integer", "[9223372036854775807]"), propertyListTest(t, "integer", "[-9223372036854775808]")
	q, err := propertyWriteQuery(r)
	if err != nil || q.Parameters["type"] != "LIST<INTEGER NOT NULL> NOT NULL" || !reflect.DeepEqual(q.Parameters["after"], []any{int64(-9223372036854775808)}) {
		t.Fatal("list parameter lost exact int64 or native list type", err)
	}
	r.Before.Values[0].Text, r.After.Values[0].Text = "1", "2"
	if !reflect.DeepEqual(q.Parameters["before"], []any{int64(9223372036854775807)}) || !reflect.DeepEqual(q.Parameters["after"], []any{int64(-9223372036854775808)}) {
		t.Fatal("native list parameters shared mutable graph value storage")
	}
	r.Before, r.After = contract.GraphValue{Kind: "null"}, contract.GraphValue{Kind: "list"}
	if q, err = propertyWriteQuery(r); err != nil || q.Parameters["before"] != nil || !reflect.DeepEqual(q.Parameters["after"], []any{}) {
		t.Fatal("present empty list addition confused absence", err)
	}
	r.Before, r.After = r.After, propertyListTest(t, "boolean", "[false]")
	if q, err = propertyWriteQuery(r); err != nil || q.Parameters["type"] != "LIST<NOTHING> NOT NULL" {
		t.Fatal("empty list could not acquire an explicit element type", err)
	}
	r.Before, r.After = r.After, contract.GraphValue{Kind: "list"}
	if _, err = propertyWriteQuery(r); err != nil {
		t.Fatal("typed list could not be explicitly cleared", err)
	}
	r.Before, r.After = propertyListTest(t, "integer", "[1]"), propertyListTest(t, "float", "[1]")
	if _, err = propertyWriteQuery(r); err == nil {
		t.Fatal("nonempty list subtype change was silently admitted")
	}
	r.Before = contract.GraphValue{Kind: "string", Text: "1"}
	if _, err = propertyWriteQuery(r); err == nil {
		t.Fatal("scalar/list property change was silently admitted")
	}
	r.Before, r.After = propertyListTest(t, "float", "[1]"), contract.GraphValue{Kind: "null"}
	if q, err = propertyWriteQuery(r); err != nil || q.Parameters["type"] != "LIST<FLOAT NOT NULL> NOT NULL" || q.Parameters["after"] != nil {
		t.Fatal("list removal lost its native typed old comparison", err)
	}
}
