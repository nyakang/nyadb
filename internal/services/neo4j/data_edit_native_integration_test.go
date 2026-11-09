package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func graphDataValueTest(t *testing.T, value Value) contract.DataValue {
	t.Helper()
	cell, err := nativePropertyCell(value)
	if err != nil {
		t.Fatal(err)
	}
	return contract.DataValue{Kind: cell.Kind, Text: cell.Text, Bytes: cell.Bytes, Null: cell.Kind == contract.Null, Binary: cell.Kind == contract.Binary}
}

func TestNeo4jNativeDataWritePropertiesTypedAtomicReceiptsAndOldRows(t *testing.T) {
	f := fixtureTest(t)
	admin := nativeOpenTest(t, f)
	s := nativeSessionTest(t, f)
	label := "NyaGridWrite_" + f.Nonce + "_猫`"
	relationship := "NyaGridWriteRel_" + f.Nonce + "_猫`"
	t.Cleanup(func() {
		nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": label}})
	})
	initial := map[string]any{"guard": "original", "long": strings.Repeat("长值 ", 900)}
	after := map[string]contract.DataValue{
		"property.guard": {Null: true}, "property.long": {Kind: contract.Text, Text: strings.Repeat("完整修改 ", 700)},
		"property.string": {Kind: contract.Text, Text: ""}, "property.boolean": {Kind: contract.Boolean, Text: "false"},
		"property.integer": {Kind: contract.Integer, Text: "-9223372036854775808"}, "property.float": {Kind: contract.Floating, Text: "-0"},
		"property.bytes": {Kind: contract.Binary, Binary: true, Bytes: []byte{255, 0, 1}},
	}
	for key, value := range map[string]contract.DataValue{
		"string": {Kind: contract.Text, Text: "original"}, "boolean": {Kind: contract.Boolean, Text: "true"},
		"integer": {Kind: contract.Integer, Text: "9223372036854775807"}, "float": {Kind: contract.Floating, Text: "1.5"},
		"bytes": {Kind: contract.Binary, Binary: true, Bytes: []byte{0, 1, 255}},
	} {
		_, parameter, err := dataPropertyValue(value)
		if err != nil {
			t.Fatal(err)
		}
		initial[key] = parameter
	}
	for _, sample := range []struct{ kind, before, after string }{
		{"date", "10000-02-29", "-0001-02-28"}, {"local_time", "01:02:03.000000001", "23:59:59.999999999"},
		{"local_datetime", "10000-02-29T01:02:03.000000001", "-0001-02-28T23:59:59.999999999"},
		{"time", `{"value":"01:02:03.000000001+00:20","offset_seconds":1234}`, `{"value":"23:59:59.999999999-00:20","offset_seconds":-1234}`},
		{"datetime", `{"value":"2026-07-03T01:02:03.123456789+02:00","offset_seconds":7200,"zone":"Europe/Stockholm"}`, `{"value":"1890-01-01T00:00:00+00:09","offset_seconds":561,"zone":"Europe/Paris"}`},
		{"duration", `{"months":0,"days":1,"seconds":0,"nanoseconds":0}`, `{"months":-2,"days":3,"seconds":0,"nanoseconds":-1}`},
		{"point2d", `{"srid":7203,"x":1,"y":2}`, `{"srid":4326,"x":181,"y":-90}`},
		{"point3d", `{"srid":9157,"x":1,"y":2,"z":3}`, `{"srid":4979,"x":113.5,"y":22.3,"z":-0}`},
	} {
		before := extendedPropertyTest(t, sample.kind, sample.before)
		value := extendedPropertyTest(t, sample.kind, sample.after)
		parameter, err := dataPropertyParameter(before)
		if err != nil {
			t.Fatal(err)
		}
		initial[sample.kind] = parameter
		after[graphPropertyPrefix+sample.kind] = graphDataValueTest(t, value)
	}
	for kind, samples := range map[string][2]string{"string": {`["原始"]`, `["","猫"]`}, "boolean": {`[true]`, `[false,true]`}, "integer": {`[1]`, `[-9223372036854775808,9223372036854775807]`}, "float": {`[1.5]`, `[-0,2.5]`}} {
		before, err := contract.ParseGraphPropertyList(kind, samples[0])
		if err != nil {
			t.Fatal(err)
		}
		value, err := contract.ParseGraphPropertyList(kind, samples[1])
		if err != nil {
			t.Fatal(err)
		}
		parameter, err := dataPropertyParameter(before)
		if err != nil {
			t.Fatal(err)
		}
		initial["list_"+kind] = parameter
		after["property.list_"+kind] = graphDataValueTest(t, value)
	}
	nativeRunTest(t, admin, Request{Query: "CREATE (a:$($label)),(b:$($label)) SET a +=$initial,b +=$initial SET b.optional='existing' CREATE (a)-[r:$($type)]->(b) SET r +=$initial RETURN a,b,r", Parameters: map[string]any{"label": label, "type": relationship, "initial": initial}})
	ctx := context.Background()
	for _, target := range []struct {
		kind, name string
		rows       int
	}{{"label", label, 2}, {"relationship", relationship, 1}} {
		t.Run(target.kind, func(t *testing.T) {
			l := dataLeaseTest(t, s, target.kind, target.name)
			page, err := l.page(ctx, s.ProfileID(), "neo4j", target.name, contract.DataOptions{Limit: 10})
			if err != nil || len(page.Rows) != target.rows {
				t.Fatal("original graph write page unavailable", err)
			}
			edits := make([]contract.DataEdit, target.rows)
			for index := range edits {
				values := make(map[string]contract.DataValue, len(after)+1)
				for field, value := range after {
					values[field] = value
				}
				if target.kind == "label" {
					values["property.optional"] = contract.DataValue{Kind: contract.Text, Text: "added/updated 猫"}
				}
				edits[index] = contract.DataEdit{Kind: "update", Original: page.RowSnapshots[index], Values: values}
			}
			original, _ := json.Marshal(page)
			result, err := l.save(ctx, s.ProfileID(), "neo4j", target.name, edits)
			if err != nil || result == nil || !result.CommitConfirmed || result.CommitUncertain || result.Applied != target.rows || len(result.Outcomes) != target.rows {
				t.Fatal("native atomic grid property save failed", err)
			}
			var receipt dataWriteReceipt
			if json.Unmarshal([]byte(result.NativeJSON), &receipt) != nil || !receipt.BeginConfirmed || !receipt.CommitAttempted || !receipt.CommitConfirmed || receipt.OutcomeUnknown || receipt.RollbackAttempted || len(receipt.Bookmarks) == 0 || len(receipt.Statements) != 2*target.rows {
				t.Fatal("native commit receipt lost transaction boundary")
			}
			for _, statement := range receipt.Statements {
				if !statement.Result.Completed || statement.Result.Uncertain || statement.Result.Truncated || len(statement.Result.Rows) != 1 {
					t.Fatal("committed batch lacks complete provisional native receipts")
				}
			}
			for index, outcome := range result.Outcomes {
				row, err := l.editRow(ctx, s.ProfileID(), "neo4j", target.name, outcome.Saved.Ref)
				if err != nil || !reflect.DeepEqual(row.Snapshot, outcome.Saved) {
					t.Fatal("complete editing read lost its page fingerprint snapshot", err)
				}
				for field, expected := range edits[index].Values {
					want, _, valueErr := dataPropertyValue(expected)
					actual, _, actualErr := dataPropertyValue(row.Values[field])
					if valueErr != nil || actualErr != nil || !reflect.DeepEqual(want, actual) {
						t.Fatal("native grid commit lost precise typed field", field, valueErr, actualErr)
					}
				}
			}
			unchanged, _ := json.Marshal(page)
			if string(original) != string(unchanged) {
				t.Fatal("saving mutated captured original page")
			}
			if target.rows == 2 {
				// The greater ID is locked second. Its conflict must roll back the
				// lesser ID's already-returned provisional update.
				stale := result.Outcomes[1].Saved
				nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE elementId(n)=$id SET n.guard='distinctive concurrent'", Parameters: map[string]any{"id": stale.Ref.TypedValues[0].Text}})
				batch := []contract.DataEdit{
					{Kind: "update", Original: result.Outcomes[0].Saved, Values: map[string]contract.DataValue{"property.integer": {Kind: contract.Integer, Text: "12"}}},
					{Kind: "update", Original: stale, Values: map[string]contract.DataValue{"property.integer": {Kind: contract.Integer, Text: "34"}}},
				}
				rolledBack, err := l.save(ctx, s.ProfileID(), "neo4j", target.name, batch)
				if !errors.Is(err, contract.ErrDataConflict) || rolledBack == nil || rolledBack.CommitConfirmed || rolledBack.CommitUncertain || rolledBack.Applied != 0 {
					t.Fatal("later-row conflict did not return a confirmed atomic rollback", err)
				}
				if json.Unmarshal([]byte(rolledBack.NativeJSON), &receipt) != nil || !receipt.RollbackAttempted || !receipt.RollbackConfirmed || receipt.CommitAttempted || len(receipt.Statements) != 3 {
					t.Fatal("rollback receipt did not retain the earlier provisional write")
				}
				read := nativeRunTest(t, admin, Request{Query: "MATCH (n) WHERE $label IN labels(n) RETURN n.integer,n.guard ORDER BY elementId(n)", Mode: "read", Parameters: map[string]any{"label": label}})
				if read.Rows[0][0].Text != "-9223372036854775808" || read.Rows[0][1].Kind != "null" || read.Rows[1][0].Text != "-9223372036854775808" || read.Rows[1][1].Text != "distinctive concurrent" {
					t.Fatal("atomic rollback changed the earlier or concurrent native value")
				}
				// A full values validation payload remains valid independently of
				// fingerprint previews and is never narrowed to displayed text.
				full, err := l.row(ctx, s.ProfileID(), "neo4j", target.name, result.Outcomes[0].Saved.Ref)
				if err != nil {
					t.Fatal(err)
				}
				confirmed, err := l.save(ctx, s.ProfileID(), "neo4j", target.name, []contract.DataEdit{{Kind: "update", Original: full.Snapshot, Values: map[string]contract.DataValue{"property.integer": {Kind: contract.Integer, Text: "56"}}}})
				if err != nil || !confirmed.CommitConfirmed {
					t.Fatal("complete values validation cannot save a graph property", err)
				}
			} else {
				full, err := l.row(ctx, s.ProfileID(), "neo4j", target.name, result.Outcomes[0].Saved.Ref)
				if err != nil {
					t.Fatal(err)
				}
				removeAll := make(map[string]contract.DataValue)
				for _, column := range full.Columns {
					if strings.HasPrefix(column.Name, graphPropertyPrefix) {
						removeAll[column.Name] = contract.DataValue{Null: true}
					}
				}
				empty, err := l.save(ctx, s.ProfileID(), "neo4j", target.name, []contract.DataEdit{{Kind: "update", Original: full.Snapshot, Values: removeAll}})
				if err != nil || !empty.CommitConfirmed {
					t.Fatal("removing all graph properties lost the complete empty-entity receipt", err)
				}
				read := nativeRunTest(t, admin, Request{Query: "MATCH ()-[r]->() WHERE elementId(r)=$id RETURN size(keys(r)),type(r)", Mode: "read", Parameters: map[string]any{"id": empty.Outcomes[0].Saved.Ref.TypedValues[0].Text}})
				if read.Rows[0][0].Text != "0" || read.Rows[0][1].Text != target.name {
					t.Fatal("removing properties changed relationship identity or type")
				}
			}
			t.Log("Native scalar, bytes, homogeneous lists, eight temporal/spatial kinds, absence, complete values/fingerprints, exact receipts and atomic rollback confirmed")
		})
	}
}
