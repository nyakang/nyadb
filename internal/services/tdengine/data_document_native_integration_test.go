package tdengine

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeDataMetadataEvidence(t *testing.T, s *Session, o contract.ObjectRef) {
	t.Helper()
	evidence := map[string]any{"object": o}
	err := s.withConnection(context.Background(), func(ctx context.Context, w *wire) error {
		structure, e := s.tableMetadataOnWire(ctx, w, o)
		evidence["structure"] = structure
		if e != nil {
			evidence["structure_error"] = e.Error()
		}
		databases, e := metadataRead(ctx, w, "SHOW DATABASES")
		evidence["databases"] = databases
		if e != nil {
			evidence["databases_error"] = e.Error()
		}
		return nil
	})
	if err != nil {
		evidence["connection_error"] = err.Error()
	}
	nativeEvidence(t, "data-document-metadata-"+o.Catalog, evidence)
}

func nativeDataDocument(t *testing.T, s *Session, o contract.ObjectRef) *dataLease {
	t.Helper()
	l, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.(*dataLease)
}

func TestNativeTDengineDataDocumentCompleteRowsAndOriginalIdentity(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	sql := nativeLease(t, s, 1)
	ctx := context.Background()
	for _, precision := range []string{"ms", "us", "ns"} {
		t.Run(precision, func(t *testing.T) {
			db := nativeDatabase(t, sql, precision)
			nativeSQL(t, sql, db, "CREATE TABLE `owned`(ts TIMESTAMP,i BIGINT,u BIGINT UNSIGNED,dec DECIMAL(38,10),txt VARCHAR(12000),uni NCHAR(64),bin VARBINARY(12000),n INT)")
			timestamp := map[string]string{"ms": "1704067200123", "us": "1704067200123456", "ns": "1704067200123456789"}[precision]
			text := strings.Repeat("x", 6000)
			nativeSQL(t, sql, db, "INSERT INTO `owned` VALUES("+timestamp+",9007199254740993,18446744073709551614,'-12345678901234567890.1234567890',"+literal(text)+",'原始 Ω','\\x"+strings.Repeat("ff", 6000)+"',NULL)")
			o := s.Namespace(db).Object("table", "owned")
			doc := nativeDataDocument(t, s, o)
			page, err := doc.page(ctx, p.ID, db, o.Name, contract.DataOptions{Object: o, Limit: 2, SortColumn: "ts"})
			if err != nil || len(page.Rows) != 1 || len(page.RowRefs) != 1 || !page.Rows[0][4].Truncated || !page.Rows[0][6].Truncated || *page.Editable {
				nativeDataMetadataEvidence(t, s, o)
				t.Fatal("original owned page unavailable", err, page)
			}
			if !slices.Equal(page.ColumnEditKinds["ts"], []string{"insert"}) || !slices.Equal(page.ColumnEditKinds["txt"], []string{"insert", "update"}) || page.ColumnMetadata[0].Generated {
				t.Fatal("native identity permission fabricated generated metadata")
			}
			row, err := doc.row(ctx, p.ID, db, o.Name, *page.RowRefs[0])
			if err != nil || !reflect.DeepEqual(row.Snapshot, page.RowSnapshots[0]) || row.Values["i"].Text != "9007199254740993" || row.Values["u"].Text != "18446744073709551614" || row.Values["dec"].Text != "-12345678901234567890.1234567890" || row.Values["txt"].Text != text || !bytes.Equal(row.Values["bin"].Bytes, bytes.Repeat([]byte{255}, 6000)) || !row.Values["n"].Null {
				t.Fatal("complete original values or snapshots changed", err, row)
			}
			g, err := doc.grant(*page.RowRefs[0])
			if err != nil || g.Precision != precision {
				t.Fatal("native precision binding lost", err)
			}
			literal, err := timestampLiteral(g.Keys[0], precision)
			if err != nil || literal != timestamp {
				t.Fatal("native timestamp identity rounded", err, literal)
			}
			other := nativeDataDocument(t, s, o)
			if _, err = other.row(ctx, p.ID, db, o.Name, *page.RowRefs[0]); !errors.Is(err, contract.ErrDataConflict) {
				t.Fatal("row grant crossed original document lease", err)
			}
			mutated := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{{Original: page.RowSnapshots[0]}}}).Edits[0].Original.Ref
			mutated.TypedValues[0].Text = "2025-01-01T00:00:00Z"
			if _, err = doc.row(ctx, p.ID, db, o.Name, mutated); !errors.Is(err, contract.ErrDataConflict) {
				t.Fatal("native key was caller-replaced", err)
			}
			chunk, err := doc.chunk(ctx, p.ID, db, o.Name, "bin", *page.RowRefs[0], 5998, 2)
			if err != nil || !chunk.Binary || chunk.Text != "0xffff" || chunk.TotalUnits != 6000 || chunk.HasMore {
				t.Fatal("original binary field scope lost", err, chunk)
			}
			unicode, err := doc.chunk(ctx, p.ID, db, o.Name, "uni", *page.RowRefs[0], 1, 2)
			if err != nil || unicode.Text != "始 " || unicode.TotalUnits != 4 || unicode.NextOffset != 3 || !unicode.HasMore {
				t.Fatal("native Unicode field offsets counted bytes", err, unicode)
			}
			first, err := doc.chunk(ctx, p.ID, db, o.Name, "txt", *page.RowRefs[0], 0, 1024)
			if err != nil || first.Text != strings.Repeat("x", 1024) || first.NextOffset != 1024 || first.TotalUnits != 6000 || !first.HasMore {
				t.Fatal("explicit original field output exceeded bound", err, first)
			}
			if count, err := doc.count(ctx, p.ID, db, o.Name); err != nil || count != 1 {
				t.Fatal("owned native count unavailable", count, err)
			}
			if _, err = doc.chunk(ctx, p.ID, db, o.Name, "txt", *page.RowRefs[0], 0, 1025); !errors.Is(err, connections.ErrInvalid) {
				t.Fatal("field request exceeded explicit bound")
			}
			exported := 0
			err = doc.export(ctx, p.ID, db, o.Name, contract.DataOptions{Object: o}, nil, func(values []contract.DataValue) error {
				exported++
				if len(values[4].Text) != 6000 || len(values[6].Bytes) != 6000 {
					t.Error("owned export adopted preview limits")
				}
				values[6].Bytes[0] = 0
				return nil
			})
			if err != nil || exported != 1 {
				t.Fatal("owned full export unavailable", err)
			}
			nativeSQL(t, sql, db, "INSERT INTO `owned`(ts,txt) VALUES("+timestamp+",'external change')")
			if _, err = doc.chunk(ctx, p.ID, db, o.Name, "txt", *page.RowRefs[0], 0, 10); !errors.Is(err, contract.ErrDataConflict) {
				t.Fatal("later field replaced original field observation", err)
			}
			if _, err = doc.row(ctx, p.ID, db, o.Name, *page.RowRefs[0]); !errors.Is(err, contract.ErrDataConflict) {
				t.Fatal("later values replaced original row observation", err)
			}
			if err = doc.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = doc.row(ctx, p.ID, db, o.Name, *page.RowRefs[0]); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("closed original document read again", err)
			}
			nativeEvidence(t, "data-document-"+precision, map[string]any{"object": o, "page": page, "complete_row": row, "chunk": chunk, "unicode_chunk": unicode, "bounded_text_output": first, "field_native_scope": "complete original field read plus fingerprint comparison; output at most 1024 characters/bytes", "timestamp_literal": literal, "original_grant": g, "closed_original_lease": true})
			if precision == "ms" {
				fields := []string{"ts TIMESTAMP"}
				for i := 0; i < 80; i++ {
					fields = append(fields, "long_original_field_"+strconv.Itoa(i)+" INT")
				}
				nativeSQL(t, sql, db, "CREATE TABLE wide("+strings.Join(fields, ",")+")")
				wide := nativeDataDocument(t, s, s.Namespace(db).Object("table", "wide"))
				structure, err := wide.metadata(ctx, p.ID, db, "wide")
				if err != nil || len(structure.Definition) <= 4096 || len(structure.Columns) != 81 || !strings.Contains(structure.Definition, "long_original_field_79") {
					t.Fatal("complete native definition adopted SQL preview limit", err, structure)
				}
				nativeEvidence(t, "data-document-complete-definition", structure)
			}
		})
	}
}

func TestNativeTDengineDataDocumentCloseWaitsAndSiblingReadsRemainIndependent(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	sql := nativeLease(t, s, 1)
	db := nativeDatabase(t, sql, "ns")
	nativeSQL(t, sql, db, "CREATE TABLE items(ts TIMESTAMP,v INT);INSERT INTO items VALUES(1704067200123456789,42)")
	o := s.Namespace(db).Object("table", "items")
	doc, sibling := nativeDataDocument(t, s, o), nativeDataDocument(t, s, o)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	finished := make(chan error, 1)
	go func() {
		finished <- doc.export(context.Background(), p.ID, db, o.Name, contract.DataOptions{Object: o}, nil, func(values []contract.DataValue) error {
			if len(values) != 2 || values[1].Text != "42" {
				return contract.ErrDataPage
			}
			close(entered)
			<-release
			return doc.ctx.Err()
		})
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("original native export did not reach its sink")
	}
	closed := make(chan struct{})
	go func() { _ = doc.Close(); close(closed) }()
	select {
	case <-doc.ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("original native document did not cancel")
	}
	select {
	case <-closed:
		t.Fatal("native document close preceded executor acknowledgement")
	default:
	}
	page, err := sibling.page(context.Background(), p.ID, db, o.Name, contract.DataOptions{Limit: 1})
	if err != nil || len(page.Rows) != 1 || page.Rows[0][1].Value.Text != "42" {
		t.Fatal("original close retired sibling physical read", err)
	}
	once.Do(func() { close(release) })
	select {
	case err = <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("original canceled executor did not acknowledge", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("native executor did not finish")
	}
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("native close did not settle acknowledged executor")
	}
	if row, err := sibling.row(context.Background(), p.ID, db, o.Name, *page.RowRefs[0]); err != nil || row.Values["v"].Text != "42" {
		t.Fatal("sibling original grant was revoked by another document", err)
	}
	nativeEvidence(t, "data-document-original-close", map[string]any{"object": o, "sibling_original_page": page, "original_close_waited_for_executor": true, "sibling_original_grant_preserved": true})
}

func TestNativeTDengineDataDocumentStableChildAndMetadataRevocation(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	sql := nativeLease(t, s, 1)
	ctx := context.Background()
	db := nativeDatabase(t, sql, "ns")
	nativeSQL(t, sql, db, "CREATE STABLE `meters`(ts TIMESTAMP,k INT COMPOSITE KEY,v INT) TAGS(site NCHAR(32));CREATE TABLE `child_a` USING `meters` TAGS('A');CREATE TABLE `child_b` USING `meters` TAGS('B');INSERT INTO `child_a` VALUES(1704067200123456789,1,11);INSERT INTO `child_b` VALUES(1704067200123456789,1,22)")
	o := s.Namespace(db).Object("stable", "meters")
	doc := nativeDataDocument(t, s, o)
	page, err := doc.page(ctx, p.ID, db, o.Name, contract.DataOptions{Object: o, Limit: 10, SortColumn: "tbname"})
	if err != nil || len(page.Rows) != 2 || page.Columns[0] != "tbname" || len(page.ColumnEditKinds["site"]) != 0 {
		nativeDataMetadataEvidence(t, s, o)
		t.Fatal("stable original child identities unavailable", err, page)
	}
	for i, name := range []string{"child_a", "child_b"} {
		g, err := doc.grant(*page.RowRefs[i])
		if err != nil || g.Destination != s.Namespace(db).Object("table", name) || len(g.Keys) != 3 {
			t.Fatal("ambiguous stable timestamp identity", err, g)
		}
		row, err := doc.row(ctx, p.ID, db, o.Name, *page.RowRefs[i])
		if err != nil || row.Values["tbname"].Text != name || row.Values["site"].Text != []string{"A", "B"}[i] || !reflect.DeepEqual(row.Snapshot, page.RowSnapshots[i]) {
			t.Fatal("stable read crossed original child", err, row)
		}
	}
	structure, err := doc.metadata(ctx, p.ID, db, o.Name)
	if err != nil || structure.Columns[0].Key != "" || structure.Columns[len(structure.Columns)-1].Extra != "TAG" {
		t.Fatal("document identity view changed native structure metadata", err, structure)
	}
	child := nativeDataDocument(t, s, s.Namespace(db).Object("table", "child_a"))
	childPage, err := child.page(ctx, p.ID, db, "child_a", contract.DataOptions{Limit: 1})
	if err != nil || childPage.Columns[0] != "ts" || len(childPage.RowRefs[0].Columns) != 3 {
		t.Fatal("child timestamp/composite identity changed", err, childPage)
	}
	nativeSQL(t, sql, db, "ALTER STABLE `meters` ADD COLUMN added INT")
	if _, err = doc.row(ctx, p.ID, db, o.Name, *page.RowRefs[0]); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("old stable grant survived schema change", err)
	}
	nativeEvidence(t, "data-document-stable", map[string]any{"object": o, "original_page": page, "native_structure": structure, "child_page": childPage, "old_metadata_grant_revoked": true})
}

func TestNativeTDengineFieldProjectionSemantics(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	sql := nativeLease(t, s, 1)
	db := nativeDatabase(t, sql, "ns")
	nativeSQL(t, sql, db, "CREATE TABLE fields(ts TIMESTAMP,txt VARCHAR(64),uni NCHAR(64),bin VARBINARY(64));INSERT INTO fields VALUES(1704067200123456789,'abc原始 Ω','abc原始 Ω','\\x00ff4142')")
	observations := map[string]any{}
	for _, expression := range []string{"SUBSTR(txt,1,4),CHAR_LENGTH(txt),LENGTH(txt)", "SUBSTR(uni,1,4),CHAR_LENGTH(uni),LENGTH(uni)", "SUBSTR(bin,1,2),LENGTH(bin)", "SUBSTR(HEX(bin),1,4),LENGTH(bin)", "SUBSTR(TO_HEX(bin),1,4),LENGTH(bin)", "TO_BASE64(bin),LENGTH(bin)", "MD5(txt),MD5(bin)", "CAST(bin AS VARCHAR(64)),LENGTH(bin)"} {
		r, err := sql.Execute(context.Background(), db, "SELECT "+expression+" FROM fields")
		entry := map[string]any{"result": r}
		if err != nil {
			entry["error"] = err.Error()
		}
		if strings.Contains(expression, "SUBSTR(txt,") || strings.Contains(expression, "SUBSTR(uni,") {
			if err != nil || len(r.Sets) != 1 || len(r.Sets[0].Rows) != 1 || len(r.Sets[0].Rows[0]) != 3 || r.Sets[0].Rows[0][0].Text != "abc原" || r.Sets[0].Rows[0][1].Text != "7" || r.Sets[0].Rows[0][2].Text != map[bool]string{true: "28", false: "12"}[strings.Contains(expression, "uni")] {
				t.Fatal("verified native text projection changed semantics", expression, err, r)
			}
		} else if !isServerError(err) {
			t.Fatal("unverified binary projection silently substituted text/bytes", expression, err, r)
		}
		observations[expression] = entry
	}
	nativeEvidence(t, "field-native-projection-semantics", observations)
}
