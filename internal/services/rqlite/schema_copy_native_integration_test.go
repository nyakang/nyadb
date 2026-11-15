package rqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestNativeRQLiteSchemaCopyPreservesCompleteTypedRowsProgramsAndOriginalDocument(t *testing.T) {
	p, _ := nativeFixtureConfigured(t, "", "", true)
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 701)
	for _, c := range []struct {
		name, setup, columns string
		data, rebind, rowid  bool
	}{
		{"typed", `CREATE TABLE typed(id INTEGER PRIMARY KEY AUTOINCREMENT,v ANY,r REAL,g TEXT GENERATED ALWAYS AS (hex(v)) STORED,CONSTRAINT valid CHECK(id>0)) STRICT;CREATE INDEX typed_v ON typed(v);CREATE TABLE audit(v TEXT);INSERT INTO typed(id,v,r) VALUES(9007199254740993,X'00FF0100',1.0000000000000002),(9223372036854775807,CAST(X'610062' AS TEXT),-1.7976931348623157e308),(3,CAST(X'80FF' AS TEXT),5e-324);CREATE TRIGGER typed_insert AFTER INSERT ON typed BEGIN INSERT INTO audit VALUES(new.g); END`, "id,typeof(v),hex(v),printf('%!.26g',r),g", true, true, true},
		{"self", `CREATE TABLE self(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES self(id));INSERT INTO self VALUES(1,NULL),(2,1)`, "id,parent", true, true, true},
		{"self_original", `CREATE TABLE self_original(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES self_original(id));INSERT INTO self_original VALUES(1,NULL),(2,1)`, "id,parent", true, false, true},
		{"duplicates", `CREATE TABLE duplicates(v COLLATE NOCASE);INSERT INTO duplicates(rowid,v) VALUES(-9,'A'),(33,'A'),(91,'a'),(99,X'41'),(111,NULL),(123,1),(125,1.0)`, "typeof(v),hex(CASE WHEN typeof(v)='real' THEN printf('%!.26g',v) ELSE v END)", true, true, false},
		{"group_aliases", `CREATE TABLE group_aliases(token,n,frames,current,v);INSERT INTO group_aliases(rowid,token,n,frames,current,v) VALUES(42,'same',1,'f','c','A'),(91,'same',2,'f','c','A'),(99,'same',3,'f','c',X'41')`, "rowid,token,n,frames,current,typeof(v),hex(v)", true, true, true},
		{"shadowed", `CREATE TABLE shadowed(rowid TEXT,_rowid_ TEXT,oid TEXT,id INTEGER,PRIMARY KEY(id DESC));INSERT INTO shadowed VALUES('r','_r','o',9007199254740993)`, "rowid,_rowid_,oid,id", true, true, true},
		{"without_rowid", `CREATE TABLE without_rowid(k TEXT COLLATE NOCASE,v BLOB,PRIMARY KEY(k)) WITHOUT ROWID;INSERT INTO without_rowid VALUES('X',X'00FF')`, "k,hex(v)", true, true, true},
		{"structure_only", `CREATE TABLE structure_only(id INTEGER PRIMARY KEY AUTOINCREMENT,v TEXT);INSERT INTO structure_only VALUES(42,'retained')`, "id,v", false, true, true},
		{"empty_sequence", `CREATE TABLE empty_sequence(id INTEGER PRIMARY KEY AUTOINCREMENT,v TEXT);INSERT INTO empty_sequence VALUES(42,'deleted');DELETE FROM empty_sequence`, "id,v", true, true, true},
		{"absent_sequence", `CREATE TABLE absent_sequence(id INTEGER PRIMARY KEY AUTOINCREMENT,v TEXT)`, "id,v", true, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			nativeExecute(t, l, c.setup)
			d := reserveSchemaUnit(t, s, "copy-"+c.name, c.name)
			original := nativeSchemaWriteSnapshot(t, d)
			before := nativeExecute(t, l, "SELECT "+c.columns+" FROM "+schemaddl.Quote(c.name)+" ORDER BY 1,2").Sets[0].Rows
			sequences := nativeExecute(t, l, "SELECT rowid,name,typeof(seq),seq FROM sqlite_sequence ORDER BY rowid").Sets[0].Rows
			r := contract.SchemaCopyRequest{Original: original, Destination: c.name + "_copy", WithData: c.data, RebindSelfReferences: c.rebind, PreserveRowID: c.rowid}
			out := d.Operations().Copy(context.Background(), r)
			actual := requireSchemaCommit(t, "schema-copy-"+c.name, out)
			if out.Receipt.Operation != "copy" || out.Receipt.Desired != nil || out.Receipt.WithData != c.data || out.Receipt.RebindSelfReferences != c.rebind || out.Receipt.PreserveRowID != c.rowid || out.Receipt.Original.Target != original.Target || actual.Target.Object.Name != r.Destination || d.target != original.Target {
				t.Fatal("copy relabelled original document, immutable flags or receipt")
			}
			after := nativeExecute(t, l, "SELECT "+c.columns+" FROM "+schemaddl.Quote(c.name)+" ORDER BY 1,2").Sets[0].Rows
			copied := nativeExecute(t, l, "SELECT "+c.columns+" FROM "+schemaddl.Quote(r.Destination)+" ORDER BY 1,2").Sets[0].Rows
			if !reflect.DeepEqual(before, after) || c.data && !reflect.DeepEqual(before, copied) || !c.data && len(copied) != 0 || out.Rows != int64(len(copied)) {
				t.Fatal("copy changed complete original values, duplicates or row count")
			}
			current := nativeSchemaWriteSnapshot(t, d)
			if current.Target != original.Target || schemaddl.Signature(current.Schema.Table) != schemaddl.Signature(original.Schema.Table) || d.OriginalOutcome().Receipt.NativeJSON != out.Receipt.NativeJSON {
				t.Fatal("copy lost source schema or a source read replaced original evidence")
			}
			retained := nativeExecute(t, l, "SELECT rowid,name,typeof(seq),seq FROM sqlite_sequence WHERE name<>"+schemaTextLiteral(r.Destination)+" ORDER BY rowid").Sets[0].Rows
			if !reflect.DeepEqual(sequences, retained) {
				t.Fatal("copy changed another original sequence identity/value")
			}
			if c.data && schemaddl.SQLiteAutoIncrement(original.Schema.Table) {
				values := nativeExecute(t, l, "SELECT typeof(seq),seq FROM sqlite_sequence WHERE name="+schemaTextLiteral(c.name)).Sets[0].Rows
				copyValues := nativeExecute(t, l, "SELECT typeof(seq),seq FROM sqlite_sequence WHERE name="+schemaTextLiteral(r.Destination)).Sets[0].Rows
				if !reflect.DeepEqual(values, copyValues) || len(copyValues) > 1 {
					t.Fatal("copy did not retain exact empty/absent sequence state")
				}
			}
			if c.name == "typed" && nativeExecute(t, l, "SELECT count(*) FROM audit").Sets[0].Rows[0][0].Text != "0" {
				t.Fatal("copy executed the installed INSERT trigger during loading")
			}
			if c.name == "self" || c.name == "self_original" {
				parent := nativeExecute(t, l, "PRAGMA foreign_key_list("+schemaddl.Quote(r.Destination)+")").Sets[0].Rows[0][2].Text
				expected := c.name
				if c.rebind {
					expected = r.Destination
				}
				if parent != expected {
					t.Fatal("copy changed the chosen self-reference binding", parent)
				}
			}
		})
	}
}

func TestNativeRQLiteSchemaCopyCancellationCloseAndLostAcknowledgementKeepOriginalEvidence(t *testing.T) {
	for _, mode := range []string{"caller", "document", "session", "lost"} {
		t.Run(mode, func(t *testing.T) {
			p := nativeFixture(t, "")
			s := openNative(t, p, "original-fixture-password")
			l := lease(t, s, 704)
			nativeExecute(t, l, "CREATE TABLE copy_held(id INTEGER PRIMARY KEY,v BLOB);INSERT INTO copy_held VALUES(9007199254740993,X'00FF')")
			d := reserveSchemaUnit(t, s, "original-copy-held", "copy_held")
			original := nativeSchemaWriteSnapshot(t, d)
			h := &heldSchemaWrite{base: s.transport, marker: "copy_held_destination", started: make(chan struct{}), ack: make(chan struct{}), raw: make(chan string, 1), lose: mode == "lost"}
			defer h.release.Do(func() { close(h.ack) })
			s.client.Transport = h
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := contract.SchemaCopyRequest{Original: original, Destination: "copy_held_destination", WithData: true, PreserveRowID: true, RebindSelfReferences: true}
			done := make(chan contract.SchemaDesignOutcome, 1)
			go func() { done <- d.Operations().Copy(ctx, request) }()
			select {
			case <-h.started:
			case <-time.After(10 * time.Second):
				t.Fatal("original native copy did not acknowledge")
			}
			raw := <-h.raw
			retainNativeFileReceipt(t, "schema-copy-held-"+mode+"-transport-original", raw)
			cancel()
			closed := make(chan struct{})
			if mode == "document" || mode == "session" {
				go func() {
					if mode == "session" {
						_ = s.Close()
					} else {
						_ = d.Close()
					}
					close(closed)
				}()
			}
			select {
			case <-done:
				t.Fatal("cancel abandoned original native copy acknowledgement")
			case <-closed:
				t.Fatal("close abandoned original copy executor")
			case <-time.After(30 * time.Millisecond):
			}
			h.release.Do(func() { close(h.ack) })
			var out contract.SchemaDesignOutcome
			select {
			case out = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("original native copy did not settle")
			}
			retainSchemaWriteOutcome(t, "schema-copy-held-"+mode, out)
			if out.Receipt == nil || out.Receipt.Original.Target != original.Target || out.Receipt.Destination != request.Destination || d.target != original.Target {
				t.Fatal("copy settlement lost its original source/document identity")
			}
			if mode == "lost" {
				if out.State != "uncertain" || out.Verified || out.Receipt.NativeJSON != "" {
					t.Fatal("lost native copy receipt was reconstructed from a later read")
				}
				retained := d.OriginalOutcome()
				_ = nativeSchemaWriteSnapshot(t, d)
				request.Destination = "another_copy"
				next := d.Operations().Copy(context.Background(), request)
				if next.State != "not_executed" || !reflect.DeepEqual(retained, d.OriginalOutcome()) || h.requests.Load() != 1 {
					t.Fatal("read erased original uncertainty or copy replayed")
				}
			} else {
				requireSchemaCommit(t, "schema-copy-held-"+mode, out)
				if out.Receipt.NativeJSON != raw || h.requests.Load() != 1 {
					t.Fatal("acknowledged copy changed original bytes or replayed")
				}
			}
			if mode == "document" || mode == "session" {
				select {
				case <-closed:
				case <-time.After(5 * time.Second):
					t.Fatal("copy close did not wait for original executor completion")
				}
				if d.OriginalOutcome() != nil {
					t.Fatal("closed copy lease kept original receipt caches")
				}
			}
		})
	}
}

type nativeCopyMutation struct {
	base http.RoundTripper
	edit func(string) string
}

func (m nativeCopyMutation) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/db/execute" {
		return m.base.RoundTrip(r)
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	var sql []string
	if err := json.Unmarshal(raw, &sql); err != nil {
		return nil, err
	}
	for i, statement := range sql {
		sql[i] = m.edit(statement)
	}
	modified, err := json.Marshal(sql)
	if err != nil {
		return nil, err
	}
	next := r.Clone(r.Context())
	next.Body, next.ContentLength = io.NopCloser(bytes.NewReader(modified)), int64(len(modified))
	return m.base.RoundTrip(next)
}

func TestNativeRQLiteSchemaCopyOriginalWriterRejectsSameCountTypeMultiplicityAndRowIDFaults(t *testing.T) {
	// Explicit transport faults exercise the real writer guards. They do not
	// establish that a mutated transport executed the original request bytes.
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 705)
	for _, kind := range []string{"multiplicity", "storage_type", "rowid", "grouping_alias"} {
		t.Run(kind, func(t *testing.T) {
			source, destination := "guard_"+kind, "guard_"+kind+"_copy"
			setup := "CREATE TABLE " + schemaddl.Quote(source) + "(v);INSERT INTO " + schemaddl.Quote(source) + "(rowid,v) VALUES(42,'A'),(91,'A'),(99,X'41')"
			if kind == "grouping_alias" {
				setup = "CREATE TABLE " + schemaddl.Quote(source) + "(token,n,frames,current,v);INSERT INTO " + schemaddl.Quote(source) + "(rowid,token,n,frames,current,v) VALUES(42,'same',1,'f','c','A'),(91,'same',2,'f','c','A'),(99,'same',3,'f','c',X'41')"
			}
			nativeExecute(t, l, setup)
			d := reserveSchemaUnit(t, s, "copy-guard-"+kind, source)
			original := nativeSchemaWriteSnapshot(t, d)
			before := nativeExecute(t, l, "SELECT rowid,typeof(v),hex(v) FROM "+schemaddl.Quote(source)+" ORDER BY rowid").Sets[0].Rows
			base := s.client.Transport
			s.client.Transport = nativeCopyMutation{base: base, edit: func(sql string) string {
				if !strings.HasPrefix(sql, "INSERT INTO main."+schemaddl.Quote(destination)+" (") {
					return sql
				}
				switch kind {
				case "multiplicity":
					return "INSERT INTO main." + schemaddl.Quote(destination) + "(v) SELECT 'A' FROM main." + schemaddl.Quote(source)
				case "grouping_alias":
					return "INSERT INTO main." + schemaddl.Quote(destination) + "(rowid,token,n,frames,current,v) SELECT rowid,token,n,frames,current,CASE WHEN rowid=91 THEN 'changed' ELSE v END FROM main." + schemaddl.Quote(source)
				case "storage_type":
					return "INSERT INTO main." + schemaddl.Quote(destination) + "(v) SELECT CAST(v AS BLOB) FROM main." + schemaddl.Quote(source)
				default:
					return "INSERT INTO main." + schemaddl.Quote(destination) + "(v) SELECT v FROM main." + schemaddl.Quote(source)
				}
			}}
			out := d.Operations().Copy(context.Background(), contract.SchemaCopyRequest{Original: original, Destination: destination, WithData: true, PreserveRowID: kind == "rowid"})
			s.client.Transport = base
			retainSchemaWriteOutcome(t, "schema-copy-controlled-fault-"+kind, out)
			if out.State != "rolled_back" || out.Verified || out.Error == nil || out.Receipt == nil {
				t.Fatal("original writer accepted changed complete row semantics", out.State, out.Error)
			}
			after := nativeExecute(t, l, "SELECT rowid,typeof(v),hex(v) FROM "+schemaddl.Quote(source)+" ORDER BY rowid").Sets[0].Rows
			if !reflect.DeepEqual(before, after) || nativeExecute(t, l, "SELECT count(*) FROM sqlite_schema WHERE name="+schemaTextLiteral(destination)).Sets[0].Rows[0][0].Text != "0" {
				t.Fatal("controlled copy failure kept a destination or changed source rows")
			}
		})
	}
}

func TestNativeRQLiteSchemaCopyRejectsDestinationAndSequenceConflictsWithoutMutatingSource(t *testing.T) {
	p, _ := nativeFixtureConfigured(t, "", "", true)
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 702)
	nativeExecute(t, l, "CREATE TABLE source(id INTEGER PRIMARY KEY AUTOINCREMENT,v TEXT);INSERT INTO source VALUES(42,'original');CREATE TABLE existing(v TEXT);INSERT INTO existing VALUES('keep');CREATE TABLE shadow(rowid TEXT,_rowid_ TEXT,oid TEXT,id INTEGER PRIMARY KEY DESC);INSERT INTO shadow VALUES('r','_r','o',1)")
	for _, kind := range []string{"existing", "late_destination", "duplicate_sequence", "invalid_sequence", "inaccessible_rowid"} {
		t.Run(kind, func(t *testing.T) {
			source, destination := "source", "copy_"+kind
			if kind == "existing" {
				destination = "existing"
			}
			if kind == "inaccessible_rowid" {
				source = "shadow"
			}
			d := reserveSchemaUnit(t, s, "copy-reject-"+kind, source)
			original := nativeSchemaWriteSnapshot(t, d)
			if kind == "late_destination" {
				nativeExecute(t, l, "CREATE TABLE "+schemaddl.Quote(destination)+"(v TEXT);INSERT INTO "+schemaddl.Quote(destination)+" VALUES('keep')")
			}
			if kind == "duplicate_sequence" {
				nativeExecute(t, l, "INSERT INTO sqlite_sequence(name,seq) VALUES('source',99)")
			}
			if kind == "invalid_sequence" {
				nativeExecute(t, l, "UPDATE sqlite_sequence SET seq='invalid' WHERE name='source'")
			}
			out := d.Operations().Copy(context.Background(), contract.SchemaCopyRequest{Original: original, Destination: destination, WithData: true, PreserveRowID: true})
			retainSchemaWriteOutcome(t, "schema-copy-rejected-"+kind, out)
			if out.Verified || out.Error == nil || kind == "inaccessible_rowid" && out.State != "not_executed" || kind != "inaccessible_rowid" && out.State != "rolled_back" {
				t.Fatal("copy admitted an original identity/sequence conflict", out.State, out.Error)
			}
			if nativeExecute(t, l, "SELECT v FROM source").Sets[0].Rows[0][0].Text != "original" || nativeExecute(t, l, "SELECT v FROM existing").Sets[0].Rows[0][0].Text != "keep" {
				t.Fatal("rejected copy changed source or existing destination")
			}
			if kind == "duplicate_sequence" || kind == "invalid_sequence" {
				nativeExecute(t, l, "DELETE FROM sqlite_sequence WHERE name='source';INSERT INTO sqlite_sequence(name,seq) VALUES('source',42)")
			}
		})
	}
}

func TestNativeRQLiteSchemaCopy2000Columns200IndexesRetainsOriginalCompleteCatalog(t *testing.T) {
	for _, mode := range []string{"integer-primary-key", "hidden-rowid", "regenerated-rowid", "hidden-rowid-controlled-fault"} {
		t.Run(mode, func(t *testing.T) {
			p := nativeFixture(t, "")
			s := openNative(t, p, "original-fixture-password")
			l := lease(t, s, 703)
			columns := make([]string, 2000)
			columns[0] = "c0000 INTEGER"
			if mode == "integer-primary-key" {
				columns[0] += " PRIMARY KEY"
			}
			for i := 1; i < len(columns); i++ {
				columns[i] = fmt.Sprintf("c%04d TEXT DEFAULT 'retained'", i)
			}
			nativeExecute(t, l, "CREATE TABLE wide_copy_source("+strings.Join(columns, ",")+",CONSTRAINT positive CHECK(c0000>0))")
			if mode == "integer-primary-key" {
				nativeExecute(t, l, "INSERT INTO wide_copy_source(c0000,c1999) VALUES(9007199254740993,'last'),(9223372036854775807,'first')")
			} else {
				nativeExecute(t, l, "INSERT INTO wide_copy_source(rowid,c0000,c1999) VALUES(-9223372036854775808,9007199254740993,'last'),(9223372036854775807,9223372036854775807,'first')")
			}
			var indexes []string
			for i := 0; i < 200; i++ {
				indexes = append(indexes, fmt.Sprintf("CREATE INDEX wide_copy_i%03d ON wide_copy_source(c%04d)", i, i+1))
			}
			nativeExecute(t, l, strings.Join(indexes, ";"))
			d := reserveSchemaUnit(t, s, "native-copy-wide", "wide_copy_source")
			before := nativeSchemaWriteSnapshot(t, d)
			preserve := mode != "regenerated-rowid"
			base := s.client.Transport
			if mode == "hidden-rowid-controlled-fault" {
				// This explicitly mutated transport tests native rollback and TEMP
				// cleanup; its request bytes are not the unmodified original receipt.
				s.client.Transport = nativeCopyMutation{base: base, edit: func(sql string) string {
					if strings.HasPrefix(sql, "CREATE TEMP TRIGGER ") {
						return strings.ReplaceAll(sql, "SELECT copied_source.\"c1999\"", "SELECT CAST(copied_source.\"c1999\" AS BLOB)")
					}
					return sql
				}}
			}
			out := d.Operations().Copy(context.Background(), contract.SchemaCopyRequest{Original: before, Destination: "wide_copy", WithData: true, PreserveRowID: preserve, RebindSelfReferences: true})
			s.client.Transport = base
			if mode == "hidden-rowid-controlled-fault" {
				retainSchemaWriteOutcome(t, "schema-copy-wide-"+mode, out)
				if out.State != "rolled_back" || out.Verified || out.Error == nil || out.Receipt == nil {
					t.Fatal("wide original writer accepted changed complete values", out.State, out.Error)
				}
				after := nativeSchemaWriteSnapshot(t, d)
				if !reflect.DeepEqual(before.Schema.SQLite, after.Schema.SQLite) || nativeExecute(t, l, "SELECT count(*) FROM temp.sqlite_schema WHERE name LIKE '__nyadb_copy_%'").Sets[0].Rows[0][0].Text != "0" {
					t.Fatal("wide rollback changed original catalog or retained temporary transfer programs")
				}
				return
			}
			actual := requireSchemaCommit(t, "schema-copy-wide-"+mode, out)
			if len(actual.Schema.Table.Columns()) != 2000 || len(actual.Schema.Table.Clauses) != 2001 || len(actual.Schema.Table.Definitions) != 200 || out.Rows != 2 || len(actual.Schema.SQLite.Entries) != len(before.Schema.SQLite.Entries)+201 {
				t.Fatal("wide copy omitted complete columns, constraint or indexes")
			}
			fields := "c0000,c1999"
			if preserve {
				fields = "rowid," + fields
			}
			values := nativeExecute(t, l, "SELECT "+fields+" FROM wide_copy ORDER BY c0000").Sets[0].Rows
			originalValues := nativeExecute(t, l, "SELECT "+fields+" FROM wide_copy_source ORDER BY c0000").Sets[0].Rows
			if !reflect.DeepEqual(values, originalValues) {
				t.Fatal("wide copy lost exact original edge columns or physical rowids")
			}
			if nativeExecute(t, l, "SELECT count(*) FROM temp.sqlite_schema WHERE name LIKE '__nyadb_copy_%'").Sets[0].Rows[0][0].Text != "0" {
				t.Fatal("copy left temporary writer programs or data caches")
			}
		})
	}
}
