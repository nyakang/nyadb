package graphresult

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nodeValue(properties ...contract.GraphProperty) contract.GraphValue {
	return contract.GraphValue{Kind: "node", Node: &contract.GraphNode{ElementID: "original-element", LegacyID: 42, Labels: []string{"Original"}, Properties: properties}}
}

func TestNodePropertyProjectionPreservesAliasesExactKindsSourceOrdinalsAndOwnership(t *testing.T) {
	blob := []byte{0, 255, 1}
	temporal := contract.GraphValue{Kind: "datetime", Text: "2026-07-03T01:02:03.123456789+02:00", Properties: []contract.GraphProperty{{Name: "zone", Value: contract.GraphValue{Kind: "string", Text: "Europe/Stockholm"}}}}
	properties := []contract.GraphProperty{
		{Name: "x", Value: contract.GraphValue{Kind: "integer", Text: "1"}},
		{Name: "blob", Value: contract.GraphValue{Kind: "bytes", Text: base64.StdEncoding.EncodeToString(blob)}},
		{Name: "exact", Value: contract.GraphValue{Kind: "integer", Text: "9223372036854775807"}},
		{Name: "empty", Value: contract.GraphValue{Kind: "string"}},
		{Name: "datetime", Value: temporal},
		{Name: "list", Value: contract.GraphValue{Kind: "list", Values: []contract.GraphValue{{Kind: "integer", Text: "-9223372036854775808"}}}},
		{Name: "z_猫`\\u0060", Value: contract.GraphValue{Kind: "string", Text: "literal key"}},
	}
	graph := contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"n", "n.x", "n", "empty", "mixed"}, Rows: [][]contract.GraphValue{
		{nodeValue(properties...), {Kind: "string", Text: "original scalar alias"}, nodeValue(contract.GraphProperty{Name: "x", Value: contract.GraphValue{Kind: "integer", Text: "2"}}), nodeValue(), nodeValue(contract.GraphProperty{Name: "x", Value: contract.GraphValue{Kind: "boolean", Text: "true"}})},
		{nodeValue(contract.GraphProperty{Name: "x", Value: contract.GraphValue{Kind: "string", Text: "different native kind"}}, contract.GraphProperty{Name: "late", Value: contract.GraphValue{Kind: "string", Text: "later row"}}), {Kind: "null"}, {Kind: "null"}, nodeValue(), {Kind: "string", Text: "scalar source"}},
	}}
	graph.Summary = contract.GraphSummary{Database: "neo4j", Counters: map[string]int64{}}
	before, _ := json.Marshal(graph)
	r := Project("RETURN original source", graph)
	set := r.Sets[0]
	cell := func(row int, name string) contract.Cell {
		index := slices.IndexFunc(set.Columns, func(c contract.Column) bool { return c.Name == name })
		if index < 0 {
			t.Fatal("missing property", name)
		}
		return set.Rows[row][index]
	}
	if !reflect.DeepEqual(set.DefaultHiddenColumns, []int{0, 2}) || set.Rows[0][1].Text != "original scalar alias" || set.Rows[1][4].Text != "scalar source" || set.Columns[5].Name != "n.blob" {
		t.Fatal("pure/empty/mixed nodes, source ordinals or canonical property order lost")
	}
	if cell(0, "n.x (2)").Kind != contract.Integer || cell(1, "n.x (2)").Kind != contract.Text || cell(0, "n.x (3)").Text != "2" || cell(0, "mixed.x").Kind != contract.Boolean || cell(1, "mixed.x").Kind != contract.Null {
		t.Fatal("alias collision or mixed native kinds lost")
	}
	if cell(0, "n.exact").Text != "9223372036854775807" || cell(0, "n.exact").Kind != contract.Integer || cell(0, "n.empty").Kind != contract.Text || cell(0, "n.empty").Text != "" || cell(1, "n.empty").Kind != contract.Null || cell(0, "n.late").Kind != contract.Null || cell(1, "n.late").Text != "later row" || !bytes.Equal(cell(0, "n.blob").Bytes, blob) || cell(0, "n.z_猫`\\u0060").Text != "literal key" {
		t.Fatal("exact integer, empty/missing, bytes or literal property key changed")
	}
	rawTemporal, _ := json.Marshal(temporal)
	if cell(0, "n.datetime").Kind != contract.JSON || cell(0, "n.datetime").Text != string(rawTemporal) || !strings.Contains(cell(0, "n.list").Text, "-9223372036854775808") {
		t.Fatal("temporal zone or list element kind narrowed")
	}
	retained, _ := json.Marshal(r.Graph)
	if !bytes.Equal(before, retained) || r.Steps[0].SQL != "RETURN original source" || r.Steps[0].State != "completed" {
		t.Fatal("projection changed native receipt, identities, columns or source")
	}
	graph.Rows[0][0].Node.Properties[0].Value.Text = "changed input"
	set.Rows[0][5].Bytes[0] = 17
	retained, _ = json.Marshal(r.Graph)
	if !bytes.Equal(before, retained) {
		t.Fatal("projection retained mutable caller/property cell aliases")
	}
}

func TestNodePropertyLayoutIdentityIgnoresObservedTypeAndRowOrder(t *testing.T) {
	graph := contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"n"}, Rows: [][]contract.GraphValue{
		{nodeValue(contract.GraphProperty{Name: "z", Value: contract.GraphValue{Kind: "integer", Text: "1"}})},
		{nodeValue(contract.GraphProperty{Name: "a", Value: contract.GraphValue{Kind: "string", Text: "two"}})},
	}}
	first := Project("RETURN n", graph).Sets[0]
	slices.Reverse(graph.Rows)
	graph.Rows[1][0].Node.Properties[0].Value.Kind = "string"
	second := Project("RETURN n", graph).Sets[0]
	if first.LayoutKey == "" || first.LayoutKey != second.LayoutKey || first.Columns[1].Name != "n.a" || !reflect.DeepEqual(first.DefaultHiddenColumns, second.DefaultHiddenColumns) || first.Columns[2].DatabaseType == second.Columns[2].DatabaseType {
		t.Fatal("type observations or row order replaced stable property layout identity")
	}
	graph.Rows = append(graph.Rows, []contract.GraphValue{{Kind: "integer", Text: "3"}})
	mixed := Project("RETURN n", graph).Sets[0]
	if len(mixed.DefaultHiddenColumns) != 0 || mixed.LayoutKey == first.LayoutKey {
		t.Fatal("mixed scalar/node result inherited a hidden source")
	}
}

func TestNodePropertyBoundsRetainCompleteNativeResultAndConfirmedCompletion(t *testing.T) {
	for _, kind := range []string{"columns", "cells", "bytes", "layout"} {
		t.Run(kind, func(t *testing.T) {
			graph := contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"n"}}
			switch kind {
			case "columns":
				properties := make([]contract.GraphProperty, 1024)
				for i := range properties {
					properties[i] = contract.GraphProperty{Name: fmt.Sprintf("p%d", i), Value: contract.GraphValue{Kind: "integer", Text: "1"}}
				}
				graph.Rows = [][]contract.GraphValue{{nodeValue(properties...)}}
			case "cells":
				for i := 0; i < 2000; i++ {
					graph.Rows = append(graph.Rows, []contract.GraphValue{nodeValue(contract.GraphProperty{Name: fmt.Sprintf("p%d", i%600), Value: contract.GraphValue{Kind: "integer", Text: "1"}})})
				}
			case "bytes":
				for range 60 {
					graph.Rows = append(graph.Rows, []contract.GraphValue{nodeValue(contract.GraphProperty{Name: "memo", Value: contract.GraphValue{Kind: "string", Text: strings.Repeat("a", 150000)}})})
				}
			case "layout":
				properties := make([]contract.GraphProperty, 32)
				for i := range properties {
					properties[i] = contract.GraphProperty{Name: fmt.Sprintf("%d", i) + strings.Repeat("x", 4000), Value: contract.GraphValue{Kind: "integer", Text: "1"}}
				}
				graph.Rows = [][]contract.GraphValue{{nodeValue(properties...)}}
			}
			r := Project("RETURN n", graph)
			if !r.Graph.Completed || r.Graph.Truncated || len(r.Graph.Rows) != len(graph.Rows) || r.Steps[0].State != "completed" || r.Steps[0].Error != "" || len(r.Sets[0].Columns) != 1 || len(r.Sets[0].Rows) != len(graph.Rows) || len(r.Sets[0].DefaultHiddenColumns) != 0 || !slices.Contains(r.Sets[0].Truncation, PropertyProjectionLimit) {
				t.Fatal("bounded property display replaced native completion or silently dropped original results")
			}
		})
	}
}

func TestNodePropertyProjectionLeavesEmptyNodesAndScalarQueriesUnchanged(t *testing.T) {
	for _, value := range []contract.GraphValue{nodeValue(), {Kind: "integer", Text: "9223372036854775807"}, {Kind: "null"}} {
		r := Project("RETURN value", contract.GraphResult{Attempted: true, Completed: true, Columns: []string{"value"}, Rows: [][]contract.GraphValue{{value}}})
		if len(r.Sets[0].Columns) != 1 || len(r.Sets[0].DefaultHiddenColumns) != 0 || r.Sets[0].LayoutKey != "" || len(r.Sets[0].Truncation) != 0 {
			t.Fatal("empty node or scalar query acquired property projection")
		}
	}
}
