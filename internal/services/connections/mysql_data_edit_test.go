package connections

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Supply server-style hidden identity/fingerprint columns to existing drivers.
func dataEditFixtureProjections(query string, rows *structureRows) *structureRows {
	if !strings.Contains(query, "__nyadb_identity_") {
		return rows
	}
	n := len(rows.cols)
	rows.cols = append(rows.cols, "identity")
	for i := 0; i < n; i++ {
		rows.cols = append(rows.cols, "hash")
	}
	for i, line := range rows.values {
		next := append(append([]driver.Value{}, line...), line[0])
		for _, v := range line {
			var hash driver.Value
			if v != nil {
				sum := sha256.Sum256(mysqlEditRawBytes(v))
				hash = hex.EncodeToString(sum[:])
			}
			next = append(next, hash)
		}
		rows.values[i] = next
	}
	return rows
}
func TestMySQLDataIdentityExistsForOffsetAndNullableSort(t *testing.T) {
	m := connectedQueryOptionsFixture(t, make(chan structureCall, 10))
	for _, o := range []MySQLDataOptions{{Limit: 2}, {Limit: 2, UseKeyset: true, SortColumn: "note"}} {
		page, e := m.TableDataPageWithOptions(context.Background(), 19, "db", "records", o)
		if e != nil {
			t.Fatal(e)
		}
		if page.KeysetAvailable {
			t.Fatal("unexpected seek")
		}
		if len(page.RowRefs) != 2 || page.RowRefs[0] == nil || string(page.RowRefs[1].Values[0]) != "2" || len(page.RowSnapshots) != 2 {
			t.Fatalf("identity unavailable: %+v", page)
		}
		snapshot := page.RowSnapshots[1]
		if snapshot.Fingerprints[1] != nil || snapshot.Fingerprints[2] == nil {
			t.Fatal("NULL and empty fingerprint conflated")
		}
	}
}
func TestMySQLDataEditParametersPreserveNullDefaultAndExactNumbers(t *testing.T) {
	cols := []MySQLStructureColumn{{Name: "n`x", Nullable: true}, {Name: "price"}, {Name: "other", Nullable: true}}
	names, expr, args, e := mysqlEditAssignments(cols, map[string]MySQLDataValue{"n`x": {Null: true}, "price": {Text: "9007199254740993.123456789"}, "other": {Default: true}}, true)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(names, []string{"`n``x`", "`other`", "`price`"}) || !reflect.DeepEqual(expr, []string{"?", "DEFAULT", "?"}) || len(args) != 2 || args[0] != nil || args[1] != "9007199254740993.123456789" {
		t.Fatalf("lost semantics %v %v %v", names, expr, args)
	}
	_, _, args, e = mysqlEditAssignments(cols, map[string]MySQLDataValue{"n`x": {Text: ""}}, false)
	if e != nil || len(args) != 1 || args[0] != "" {
		t.Fatal("empty string became NULL/default")
	}
}
func TestMySQLDataEditRejectsInvalidValuesAndGeneratedColumns(t *testing.T) {
	cols := []MySQLStructureColumn{{Name: "normal", Extra: "DEFAULT_GENERATED"}, {Name: "virtual", Extra: "VIRTUAL GENERATED"}, {Name: "stored", Extra: "STORED GENERATED"}}
	if MySQLDataColumnGenerated(cols[0]) || !MySQLDataColumnGenerated(cols[1]) || !MySQLDataColumnGenerated(cols[2]) {
		t.Fatal("generated column classification")
	}
	for _, v := range []map[string]MySQLDataValue{{"virtual": {Text: "x"}}, {"stored": {Default: true}}, {"missing": {Text: "x"}}, {"normal": {Null: true}}, {"normal": {Null: true, Default: true}}, {"normal": {Text: "x", Default: true}}, {"normal": {Text: strings.Repeat("x", MySQLDataEditMaxBytes+1)}}} {
		if _, _, _, e := mysqlEditAssignments(cols, v, false); e == nil {
			t.Fatalf("accepted invalid %v", v)
		}
	}
}
func TestMySQLDataSnapshotDetectsChangeIncludingHiddenField(t *testing.T) {
	cols := []MySQLStructureColumn{{Name: "id"}, {Name: "long"}}
	a := mysqlSnapshotFromRaw(MySQLRowRef{}, cols, []any{"id-hash", "full-long-hash"})
	b := mysqlSnapshotFromRaw(MySQLRowRef{}, cols, []any{"id-hash", "changed-long-hash"})
	if mysqlSnapshotsEqual(a, b) {
		t.Fatal("hidden concurrent change ignored")
	}
	b = mysqlSnapshotFromRaw(MySQLRowRef{}, cols, []any{"id-hash", "full-long-hash"})
	if !mysqlSnapshotsEqual(a, b) {
		t.Fatal("same snapshot differs")
	}
	cols[1].Extra = "STORED GENERATED"
	b = mysqlSnapshotFromRaw(MySQLRowRef{}, cols, []any{"id-hash", "full-long-hash"})
	if mysqlSnapshotsEqual(a, b) {
		t.Fatal("schema change ignored")
	}
}
func TestMySQLDataEditRejectsCrossTableRefAndDisconnect(t *testing.T) {
	ref := MySQLRowRef{Database: "a", Table: "t", Columns: []string{"id"}, Values: [][]byte{[]byte("1")}}
	if _, _, e := mysqlEditWhere(ref, "b", "t"); e == nil {
		t.Fatal("cross-schema accepted")
	}
	m := NewMySQLSessionManager()
	if _, e := m.ApplyMySQLDataEdits(context.Background(), 1, "a", "t", []MySQLDataEdit{{Kind: "insert"}}); !errors.Is(e, ErrMySQLNotConnected) {
		t.Fatal(e)
	}
}
