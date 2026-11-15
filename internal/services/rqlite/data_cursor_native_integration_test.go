package rqlite

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func cloneNativeCursor(t *testing.T, c *contract.DataCursor) *contract.DataCursor {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var copy contract.DataCursor
	if err := json.Unmarshal(b, &copy); err != nil {
		t.Fatal(err)
	}
	return &copy
}

func TestNativeRQLiteTypedCursorMatchesOriginalMixedStorageNullAndCollationOrder(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE native_sort(id INTEGER PRIMARY KEY,v COLLATE NOCASE);
INSERT INTO native_sort VALUES(1,NULL),(2,-2),(3,1),(4,CAST(1 AS REAL)),(5,2.5),(6,'A'),(7,'a'),(8,CAST(X'610062' AS TEXT)),(9,'2'),(10,X'00FF'),(11,X'80FF'),(12,9223372036854775807),(13,-9223372036854775808),(14,CAST(-0.0 AS REAL));`)
	_, r := nativeDataDocument(t, s, "native_sort")
	for _, descending := range []bool{false, true} {
		direction := " ASC"
		if descending {
			direction = " DESC"
		}
		original := nativeExecute(t, l, "SELECT id FROM native_sort ORDER BY v"+direction+",id COLLATE BINARY ASC")
		var expected, actual []string
		for _, row := range original.Sets[0].Rows {
			expected = append(expected, row[0].Text)
		}
		o := contract.DataOptions{Limit: 2, UseKeyset: true, Sorts: []contract.DataSortRule{{Column: "v", Descending: descending}}}
		for {
			page, err := r.AdvancedLoader(context.Background(), p.ID, "main", "native_sort", o)
			if err != nil {
				t.Fatal("original native cursor page", err)
			}
			if !page.KeysetAvailable || !page.LiveRead || !page.StableOrder {
				t.Fatal("native cursor falsely published a fixed or unstable snapshot")
			}
			if page.Offset != int64(len(actual)) || o.After != nil && (page.CurrentCursor == nil || page.CurrentCursor.Offset != o.After.Offset) {
				t.Fatal("original native cursor lost its current boundary or page position")
			}
			for _, row := range page.Rows {
				actual = append(actual, row[0].Text)
			}
			if !page.HasMore {
				break
			}
			if page.NextCursor == nil {
				t.Fatal("original typed boundary missing")
			}
			if len(actual) > len(expected) {
				t.Fatal("original cursor repeated native rows")
			}
			o.After = page.NextCursor
		}
		if !reflect.DeepEqual(expected, actual) {
			t.Fatalf("native cursor changed original order descending=%v: %v != %v", descending, actual, expected)
		}
	}
	// Duplicate sort/identity columns retain the native declared sort and its
	// independent binary unique-index tie breaker; compound keys stay ordered.
	nativeExecute(t, l, `CREATE TABLE collation_cursor(k TEXT COLLATE NOCASE NOT NULL,n INTEGER NOT NULL,note TEXT);CREATE UNIQUE INDEX binary_cursor_key ON collation_cursor(k COLLATE BINARY,n);INSERT INTO collation_cursor VALUES('A',2,'upper2'),('a',1,'lower1'),('A',1,'upper1'),('a',2,'lower2');`)
	_, r = nativeDataDocument(t, s, "collation_cursor")
	o := contract.DataOptions{UseKeyset: true, Limit: 1, SortColumn: "k", SortDescending: true, Filters: []contract.DataFilterRule{{Column: "n", Mode: "greater", Value: "0"}}}
	var actual []string
	for {
		page, err := r.AdvancedLoader(context.Background(), p.ID, "main", "collation_cursor", o)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Rows {
			actual = append(actual, row[2].Text)
		}
		if !page.HasMore {
			break
		}
		o.After = page.NextCursor
	}
	if !reflect.DeepEqual(actual, []string{"upper1", "upper2", "lower1", "lower2"}) {
		t.Fatal("original compound binary identity escaped declared collation", actual)
	}
}

func TestNativeRQLiteCursorOwnsCompleteBoundaryAndRejectsOtherDocumentsRulesAndSchema(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE original_cursor(id INTEGER PRIMARY KEY,note TEXT);INSERT INTO original_cursor VALUES(1,replace(hex(zeroblob(2048)),'0','猫')||'a'),(2,replace(hex(zeroblob(2048)),'0','猫')||'b'),(3,'z');`)
	_, r := nativeDataDocument(t, s, "original_cursor")
	o := contract.DataOptions{Limit: 1, UseKeyset: true, SortColumn: "note", SortDescending: true}
	first, err := r.AdvancedLoader(context.Background(), p.ID, "main", "original_cursor", o)
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == nil || !first.Rows[0][1].Truncated {
		t.Fatal("large sort boundary was taken from a complete display instead of original value")
	}
	var boundary contract.Cell
	if err := json.Unmarshal(first.NextCursor.Values[0], &boundary); err != nil {
		t.Fatal(err)
	}
	if len(boundary.Text) != 12289 || boundary.Truncated {
		t.Fatal("original cursor used the bounded display")
	}
	o.After = first.NextCursor
	next, err := r.AdvancedLoader(context.Background(), p.ID, "main", "original_cursor", o)
	if err != nil || next.Rows[0][0].Text != "1" || strings.Contains(next.QuerySQL, "猫") || !strings.Contains(next.QuerySQL, "?") {
		t.Fatal("complete native seek boundary changed or leaked", err)
	}
	_, sibling := nativeDataDocument(t, s, "original_cursor")
	if _, err := sibling.AdvancedLoader(context.Background(), p.ID, "main", "original_cursor", o); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("sibling document accepted original cursor", err)
	}
	for _, change := range []func(*contract.DataCursor){
		func(c *contract.DataCursor) { c.Values[0][0] = 'x' },
		func(c *contract.DataCursor) { c.Columns[0] = "id" },
		func(c *contract.DataCursor) { c.Descending[0] = !c.Descending[0] },
		func(c *contract.DataCursor) { c.Offset++ },
		func(c *contract.DataCursor) { c.Table = "another" },
	} {
		altered := o
		altered.After = cloneNativeCursor(t, first.NextCursor)
		change(altered.After)
		if _, err := r.AdvancedLoader(context.Background(), p.ID, "main", "original_cursor", altered); err == nil {
			t.Fatal("altered original cursor was accepted")
		}
	}
	changed := o
	changed.FilterColumn, changed.FilterMode, changed.FilterValue = "id", "greater", "0"
	if _, err := r.AdvancedLoader(context.Background(), p.ID, "main", "original_cursor", changed); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("new filter accepted old cursor", err)
	}
	reopened := openNative(t, p, "original-fixture-password")
	_, fresh := nativeDataDocument(t, reopened, "original_cursor")
	if _, err := fresh.AdvancedLoader(context.Background(), p.ID, "main", "original_cursor", o); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("replacement session accepted old cursor", err)
	}
	nativeExecute(t, l, "ALTER TABLE original_cursor ADD COLUMN later TEXT")
	if _, err := r.AdvancedLoader(context.Background(), p.ID, "main", "original_cursor", o); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("changed original schema accepted stale boundary", err)
	}
}

func TestNativeRQLiteCursorRejectsOversizedOriginalWithoutTransferringCompleteValue(t *testing.T) {
	p := nativeFixture(t, "")
	p.RQLiteOptions.DatabaseTimeoutMilliseconds, p.RQLiteOptions.RequestTimeoutSeconds = 120000, 180
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, "CREATE TABLE bounded_cursor(id INTEGER PRIMARY KEY,v BLOB);INSERT INTO bounded_cursor VALUES(1,zeroblob(1048577)),(2,X'FF')")
	_, r := nativeDataDocument(t, s, "bounded_cursor")
	if _, err := r.AdvancedLoader(context.Background(), p.ID, "main", "bounded_cursor", contract.DataOptions{UseKeyset: true, Limit: 1, SortColumn: "v"}); !errors.Is(err, contract.ErrDataEditLimit) {
		t.Fatal("oversized original boundary silently used preview bytes", err)
	}
}
