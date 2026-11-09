package contract

import (
	"reflect"
	"strings"
	"testing"
)

func TestGraphPropertyListsKeepExactTokensKindsAndIndependentValues(t *testing.T) {
	for _, sample := range []struct{ kind, text string }{{"integer", "[-9223372036854775808,9223372036854775807]"}, {"float", "[-0,1e-12]"}, {"boolean", "[false,true]"}, {"string", `["","猫 ' ` + "`" + ` \\","\ud83d\udc31"]`}} {
		value, err := ParseGraphPropertyList(sample.kind, sample.text)
		if err != nil || value.Kind != "list" || value.Text != "" || len(value.Values) == 0 {
			t.Fatal("typed list parsing failed", sample.kind, err)
		}
		kind, err := GraphPropertyListElementKind(value)
		if err != nil || kind != sample.kind {
			t.Fatal("selected list element kind was lost", err)
		}
		text, err := GraphPropertyListJSON(value)
		if err != nil {
			t.Fatal(err)
		}
		copy, err := ParseGraphPropertyList(sample.kind, text)
		if err != nil || !reflect.DeepEqual(value, copy) {
			t.Fatal("list editing text changed an exact value or kind", sample.kind, text)
		}
		clone := value.Clone()
		clone.Values[0].Text = "different"
		if reflect.DeepEqual(value, clone) {
			t.Fatal("typed list clone shared element storage")
		}
	}
	for _, kind := range []string{"string", "boolean", "integer", "float"} {
		v, err := ParseGraphPropertyList(kind, "[]")
		element, _ := GraphPropertyListElementKind(v)
		if err != nil || v.Kind != "list" || element != "" || len(v.Values) != 0 {
			t.Fatal("empty list confused absence or invented an element subtype", kind, err)
		}
	}
	float, err := ParseGraphPropertyList("float", "[1]")
	if err != nil || float.Values[0].Kind != "float" || float.Values[0].Text != "1" {
		t.Fatal("explicit float selection was inferred as an integer")
	}
}

func TestGraphPropertyListsRejectUnsupportedRepresentationsAndBounds(t *testing.T) {
	for _, sample := range []struct{ kind, text string }{{"string", "null"}, {"string", "[null]"}, {"string", "[1]"}, {"boolean", "[null]"}, {"boolean", "[false,1]"}, {"integer", "[1.0]"}, {"integer", "[1e0]"}, {"integer", "[-0]"}, {"integer", "[9223372036854775808]"}, {"float", "[1e1000]"}, {"float", "[\"1\"]"}, {"integer", "[[1]]"}, {"string", "[{\"x\":1}]"}, {"integer", "[1] [2]"}, {"map", "[]"}, {"string", "[\"\xff\"]"}} {
		if _, err := ParseGraphPropertyList(sample.kind, sample.text); err == nil {
			t.Fatal("unsupported or ambiguous list admitted", sample)
		}
	}
	for _, value := range []GraphValue{{Kind: "list", Text: "flattened"}, {Kind: "list", Properties: []GraphProperty{{Name: "extra"}}}, {Kind: "list", Values: []GraphValue{{Kind: "integer", Text: "1"}, {Kind: "float", Text: "1"}}}, {Kind: "list", Values: []GraphValue{{Kind: "list"}}}, {Kind: "list", Values: []GraphValue{{Kind: "null"}}}, {Kind: "list", Values: []GraphValue{{Kind: "float", Text: "NaN"}}}, {Kind: "list", Values: make([]GraphValue, GraphPropertyListMaxItems+1)}} {
		if _, err := GraphPropertyListElementKind(value); err == nil {
			t.Fatal("malformed typed list admitted")
		}
	}
	if _, err := ParseGraphPropertyList("string", "[\""+strings.Repeat("x", 1<<20)+"\"]"); err == nil {
		t.Fatal("list text exceeded its bounded editing input")
	}
	oversized := GraphValue{Kind: "list", Values: []GraphValue{{Kind: "string", Text: strings.Repeat("\x00", (1<<20)/2)}}}
	if _, err := GraphPropertyListJSON(oversized); err == nil {
		t.Fatal("escaped editing text exceeded its bound")
	}
}
