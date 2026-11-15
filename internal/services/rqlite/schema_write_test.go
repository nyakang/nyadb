package rqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func schemaWriteUnitReceipt(t *testing.T, statements []string, catalog schemaddl.SQLiteCatalog, edit func([]any)) string {
	t.Helper()
	var payload map[string][]any
	if err := json.Unmarshal([]byte(schemaUnitJSON(t, catalog)), &payload); err != nil {
		t.Fatal(err)
	}
	results := make([]any, len(statements))
	for i, sql := range statements {
		results[i] = map[string]any{"rows_affected": 0}
		switch {
		case strings.HasSuffix(sql, "RETURNING original_rows"):
			results[i] = map[string]any{"columns": []string{"original_rows"}, "types": []string{"integer"}, "values": [][]any{{2}}}
		case strings.HasSuffix(sql, "RETURNING schema_version"):
			results[i] = payload["results"][0]
		case strings.HasSuffix(sql, "RETURNING type_hex,name_hex,table_hex,rootpage,sql_hex,sql_null"):
			results[i] = payload["results"][1]
		case strings.HasPrefix(sql, "INSERT INTO temp.") && !strings.HasSuffix(sql, " WHERE 0"):
			results[i] = map[string]any{"rows_affected": 1}
		}
	}
	if edit != nil {
		edit(results)
	}
	raw, err := json.Marshal(map[string]any{"results": results, "raft_index": json.Number("9007199254740993")})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSchemaWriteIncompleteOrUnknownAcknowledgementRemainsOriginalAndBlocksReplay(t *testing.T) {
	for _, mode := range []string{"top_level_commit_error", "partial_success", "final_catalog_incomplete", "guard_count_incomplete", "identity_changed", "committed_schema_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			var writes atomic.Int32
			var native string
			var statements []string
			originalCatalog := schemaUnitOriginal()
			changed := originalCatalog.Clone()
			changed.Version++
			changed.Entries[1].SQL = `CREATE TABLE original(id INTEGER PRIMARY KEY,v TEXT DEFAULT 'X''61''' UNIQUE,added TEXT)`
			s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/db/query" {
					fmt.Fprint(w, schemaUnitJSON(t, originalCatalog))
					return
				}
				writes.Add(1)
				if err := json.NewDecoder(r.Body).Decode(&statements); err != nil {
					t.Error(err)
				}
				switch mode {
				case "top_level_commit_error":
					native = `{"results":[],"error":"FOREIGN KEY constraint failed","raft_index":9007199254740993}`
				case "partial_success":
					native = `{"results":[{"rows_affected":1}],"raft_index":9007199254740993}`
				default:
					final := changed.Clone()
					if mode == "committed_schema_mismatch" {
						final.Entries[1].SQL = `CREATE TABLE original(id INTEGER PRIMARY KEY,v TEXT DEFAULT 'X''61''' UNIQUE,added BLOB)`
					}
					native = schemaWriteUnitReceipt(t, statements, *final, func(results []any) {
						if mode == "guard_count_incomplete" {
							results[1] = map[string]any{"rows_affected": 0}
						}
						if mode == "final_catalog_incomplete" {
							for i, sql := range statements {
								if strings.HasSuffix(sql, "RETURNING type_hex,name_hex,table_hex,rootpage,sql_hex,sql_null") {
									results[i] = map[string]any{"columns": []string{"truncated"}, "types": []string{"text"}, "values": [][]any{{"not a full schema"}}}
								}
							}
						}
					})
					if mode == "identity_changed" {
						w.Header().Set("X-RQLITE-VERSION", "8.43.4")
					}
				}
				fmt.Fprint(w, native)
			}, nil)
			d := reserveSchemaUnit(t, s, "unknown-original", "original")
			original := nativeSchemaWriteSnapshot(t, d)
			draft := original.Schema.Table.Clone()
			draft.Clauses = append(draft.Clauses, schemaddl.Clause{ID: "new:added", Kind: "column", Name: "added", SQL: "TEXT"})
			out := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft})
			if out.Verified || out.Receipt == nil || out.Receipt.NativeJSON != native || !reflect.DeepEqual(out.Receipt.Statements, statements) || out.Receipt.DocumentID != "unknown-original" || out.Receipt.Original.Target != original.Target || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) {
				t.Fatal("incomplete original acknowledgement was lost or declared safe", out.State, out.Error)
			}
			if mode == "top_level_commit_error" || mode == "partial_success" || mode == "identity_changed" {
				if out.State != "uncertain" {
					t.Fatal("unknown original request was declared committed/rolled back", out.State)
				}
			} else if out.State != "committed" {
				t.Fatal("complete commit receipt was relabelled", out.State)
			}
			if mode == "committed_schema_mismatch" && (out.Actual == nil || out.Receipt.Actual == nil || out.Receipt.Actual.Schema.Table.Clauses[2].SQL != "BLOB") {
				t.Fatal("unverified complete original catalog was discarded")
			}
			retained := d.OriginalOutcome()
			out.Receipt.Original.Schema.SQLite.Entries[0].SQL = "caller mutation"
			out.Receipt.Statements[0] = "caller replay"
			out.Receipt.Desired.Clauses[0].SQL = "caller desired"
			if !reflect.DeepEqual(retained, d.OriginalOutcome()) {
				t.Fatal("caller overwrote original retained write evidence")
			}
			_, err := d.Operations().Snapshot(context.Background(), d.target)
			if err != nil || !reflect.DeepEqual(retained, d.OriginalOutcome()) {
				t.Fatal("later read changed original unknown receipt", err)
			}
			next := d.Operations().Apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: draft})
			if next.State != "not_executed" || next.Receipt != nil || !errors.Is(next.Error, contract.ErrSchemaCommitUncertain) || writes.Load() != 1 {
				t.Fatal("unknown or incomplete commit replayed", next.Error)
			}
		})
	}
}

func TestSchemaWriteReadOnlyAndInvalidOriginalCannotDispatch(t *testing.T) {
	s := mockSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/db/query" {
			t.Error("read-only write dispatched")
		}
		fmt.Fprint(w, schemaUnitJSON(t, schemaUnitOriginal()))
	}, func(p *connections.Profile) { p.RQLiteOptions.ReadOnly = true })
	d := reserveSchemaUnit(t, s, "reader", "original")
	original := nativeSchemaWriteSnapshot(t, d)
	ops := d.Operations()
	if ops.Apply != nil || ops.Drop != nil || ops.Rename != nil {
		t.Fatal("read-only structure document exposed native write methods")
	}
	out := d.apply(context.Background(), contract.SchemaDesignRequest{Original: original, Desired: original.Schema.Table.Clone()})
	if out.State != "not_executed" || out.Receipt != nil || !errors.Is(out.Error, contract.ErrUnsupported) {
		t.Fatal("private read-only write bypassed permission", out.Error)
	}
}
