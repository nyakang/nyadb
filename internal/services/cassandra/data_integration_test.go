package cassandra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func dataFixture(t *testing.T) (connections.CassandraConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_CASSANDRA_DATA_IT") != "1" {
		t.Skip("requires owned Cassandra data acceptance")
	}
	return nativeFixture(t)
}
func fixtureData(t *testing.T, s *Session, o contract.ObjectRef) *dataLease {
	t.Helper()
	v, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	l := v.(*dataLease)
	t.Cleanup(func() { l.Close() })
	return l
}
func loadData(t *testing.T, l *dataLease, o contract.DataOptions) *contract.DataPage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	o.Object = l.object
	p, err := l.page(ctx, l.object.ProfileID, l.object.Catalog, l.object.Name, o)
	if err != nil {
		t.Fatal("original data page", err)
	}
	return p
}

func TestCassandraNativeDataPagingRowsAndExport(t *testing.T) {
	cfg, secret := dataFixture(t)
	c := fixtureClient(t, cfg, secret)
	ks := fixtureKeyspace(t, c, cfg, secret)
	query(t, c, Request{Statement: "CREATE TYPE " + ks + ".payload (empty text, nil_text text, missing int)"})
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".rows (tenant text,bucket int,id int,stamp timestamp,memo text static,amount decimal,empty text,bl blob,n bigint,da date,tm time,du duration,li list<int>,ma map<int,text>,ud frozen<payload>,tp tuple<text,int>,PRIMARY KEY ((tenant,bucket),id,stamp)) WITH CLUSTERING ORDER BY (id DESC,stamp ASC)"})
	statement := "INSERT INTO " + ks + ".rows (tenant,bucket,id,stamp,memo,amount,empty,bl,n,da,tm,du,li,ma,ud,tp) VALUES ('original',1,?,0,'猫',123456789012345678901234567890.123456789,'',0x,9223372036854775807,'1970-01-01','23:59:59.999999999',1mo2d3ns,[1,-2],{1:'one',-2:'two'},{empty:'',nil_text:null},('',42))"
	// The complete stream must cross the SQL preview's 5000-row boundary.
	for i := 0; i < 5103; i++ {
		query(t, c, Request{Statement: statement, Parameters: []Parameter{integerParameter(int32(i))}})
	}
	s := fixtureSession(t, cfg, secret, ks)
	o := s.Namespace(ks).Object("table", "rows")
	l := fixtureData(t, s, o)
	caps := l.Runtime().Capabilities
	if caps.EditableTables || caps.Import || caps.Transactions || !caps.Export || !caps.FieldChunks || l.Runtime().Saver != nil {
		t.Fatal("unsupported CQL data mutations advertised")
	}
	p := loadData(t, l, contract.DataOptions{Limit: 37})
	if len(p.Rows) != 37 || !p.HasMore || p.StableOrder || !p.LiveRead || p.PagingMode != "cursor" || p.NextCursor.Offset != 37 || p.Editable == nil || *p.Editable {
		t.Fatal("native live paging contract", len(p.Rows))
	}
	next := loadData(t, l, contract.DataOptions{Limit: 37, Offset: 37, After: p.NextCursor})
	repeated := loadData(t, l, contract.DataOptions{Limit: 37, Offset: 37, After: next.CurrentCursor})
	ar, _ := json.Marshal(next.Rows)
	br, _ := json.Marshal(repeated.Rows)
	if !bytes.Equal(ar, br) {
		t.Fatal("original native repeat boundary changed without a write")
	}

	ref := *p.RowRefs[0]
	ref.TypedValues = append([]contract.Cell(nil), ref.TypedValues...)
	for i := range ref.TypedValues {
		ref.TypedValues[i] = ref.TypedValues[i].Clone()
	}
	row, err := l.row(context.Background(), o.ProfileID, ks, o.Name, ref)
	if err != nil {
		t.Fatal("complete typed row", err)
	}
	for field, want := range map[string]string{"amount": "123456789012345678901234567890.123456789", "empty": "", "n": "9223372036854775807", "da": "1970-01-01", "tm": "23:59:59.999999999", "du": "1mo2d3ns"} {
		if row.Values[field].Text != want || row.Values[field].Null {
			t.Fatal("exact data field", field)
		}
	}
	if row.Values["bl"].Kind != contract.Binary || row.Values["bl"].Bytes == nil || row.Values["bl"].Null || row.Snapshot.Mode != "values" || len(row.Snapshot.Original) != len(p.Columns) {
		t.Fatal("empty binary/complete snapshot lost")
	}
	if !strings.Contains(row.Values["ma"].Text, `"type":"int"`) || !strings.Contains(row.Values["ud"].Text, `"null":true`) {
		t.Fatal("typed collection/UDT flattened")
	}
	count, err := l.count(context.Background(), o.ProfileID, ks, o.Name)
	if err != nil || count != 5103 {
		t.Fatal("live native count", count, err)
	}
	delivered := 0
	err = l.exportRows(context.Background(), o.ProfileID, ks, o.Name, contract.DataOptions{Object: o}, []string{"id", "amount", "empty", "bl"}, func(v []contract.DataValue) error {
		delivered++
		if len(v) != 4 || v[1].Text != row.Values["amount"].Text || v[2].Null || v[3].Null || v[3].Bytes == nil {
			return errors.New("lossy export")
		}
		return nil
	})
	if err != nil || delivered != 5103 {
		t.Fatal("complete stream", delivered, err)
	}
	var lines bytes.Buffer
	if err = l.jsonl(context.Background(), o, &lines); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(lines.Bytes(), []byte{'\n'}) != 5103 || !bytes.Contains(lines.Bytes(), []byte(`"bytes":""`)) || !bytes.Contains(lines.Bytes(), []byte(`"value":"9223372036854775807"`)) {
		t.Fatal("exact native JSONL")
	}
	foreign := fixtureData(t, s, o)
	for _, test := range []struct {
		lease *dataLease
		opts  contract.DataOptions
	}{{foreign, contract.DataOptions{Object: o, Limit: 37, Offset: 37, After: p.NextCursor}}, {l, contract.DataOptions{Object: o, Limit: 38, Offset: 37, After: p.NextCursor}}} {
		if _, err = test.lease.page(context.Background(), o.ProfileID, ks, o.Name, test.opts); err == nil {
			t.Fatal("foreign/limit cursor accepted")
		}
	}
	tampered := *p.NextCursor
	tampered.Values = [][]byte{bytes.Clone(tampered.Values[0])}
	tampered.Values[0][0] ^= 1
	if _, err = l.page(context.Background(), o.ProfileID, ks, o.Name, contract.DataOptions{Object: o, Limit: 37, Offset: 37, After: &tampered}); !errors.Is(err, ErrInvalid) {
		t.Fatal("tampered cursor", err)
	}
	idValue := row.Values["id"].Text
	query(t, c, Request{Statement: "UPDATE " + ks + ".rows SET empty='changed' WHERE tenant='original' AND bucket=1 AND id=" + idValue + " AND stamp=0"})
	if _, err = l.row(context.Background(), o.ProfileID, ks, o.Name, ref); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("stale value observation accepted", err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = l.count(context.Background(), o.ProfileID, ks, o.Name); err == nil {
		t.Fatal("closed document revived")
	}
}

func TestCassandraNativeDataUUIDStaticFieldsAndSchema(t *testing.T) {
	cfg, secret := dataFixture(t)
	c := fixtureClient(t, cfg, secret)
	ks := fixtureKeyspace(t, c, cfg, secret)
	other := fixtureKeyspace(t, c, cfg, secret)
	for _, space := range []string{ks, other} {
		query(t, c, Request{Statement: "CREATE TABLE " + space + ".rows (p text,c int,s text static,v blob,PRIMARY KEY(p,c))"})
	}
	query(t, c, Request{Statement: "UPDATE " + ks + ".rows SET s='static original' WHERE p='only'"})
	query(t, c, Request{Statement: "INSERT INTO " + other + ".rows(p,c,s) VALUES ('other',1,'foreign')"})
	s := fixtureSession(t, cfg, secret, other)
	o := s.Namespace(ks).Object("table", "rows")
	l := fixtureData(t, s, o)
	page := loadData(t, l, contract.DataOptions{Limit: 20})
	if len(page.Rows) != 1 {
		t.Fatal("static-only row missing")
	}
	r := *page.RowRefs[0]
	identity, err := l.decodeRef(r)
	if err != nil || !identity.StaticOnly {
		t.Fatal("static-only partition invented a clustering key", err)
	}
	row, err := l.row(context.Background(), o.ProfileID, ks, o.Name, r)
	if err != nil || !row.Values["c"].Null || row.Values["s"].Text != "static original" {
		t.Fatal("qualified static reread", err)
	}
	query(t, c, Request{Statement: "INSERT INTO " + ks + ".rows(p,c,s,v) VALUES ('full',1,?,?)", Parameters: []Parameter{{Bytes: []byte(strings.Repeat("猫", 2000))}, {Bytes: bytes.Repeat([]byte{0xab}, 3000)}}})
	page = loadData(t, l, contract.DataOptions{Limit: 20})
	var full contract.RowRef
	for i, r := range page.RowRefs {
		if page.Rows[i][slices.Index(page.Columns, "p")].Text == "full" {
			full = *r
			v := page.Rows[i][slices.Index(page.Columns, "v")]
			if !v.Truncated || len(v.Value.Bytes) != 1024 {
				t.Fatal("binary preview exceeds bounds")
			}
		}
	}
	chunk, err := l.chunk(context.Background(), o.ProfileID, ks, o.Name, "s", full, 900, 1024)
	if err != nil || len([]rune(chunk.Text)) != 1024 || chunk.NextOffset != 1924 || !chunk.HasMore {
		t.Fatal("bounded unicode field", chunk, err)
	}
	chunk, err = l.chunk(context.Background(), o.ProfileID, ks, o.Name, "v", full, 1000, 1024)
	if err != nil || len(chunk.Text) != 2048 || !chunk.Binary || chunk.NextOffset != 2024 {
		t.Fatal("bounded binary field", chunk, err)
	}
	// Original row bytes belong to each read/callback independently.
	a, err := l.row(context.Background(), o.ProfileID, ks, o.Name, full)
	if err != nil {
		t.Fatal(err)
	}
	value := a.Values["v"]
	value.Bytes[0] ^= 1
	b, err := l.row(context.Background(), o.ProfileID, ks, o.Name, full)
	if err != nil || b.Values["v"].Bytes[0] != 0xab {
		t.Fatal("read bytes aliased", err)
	}
	query(t, c, Request{Statement: "INSERT INTO " + ks + ".rows(p,c) VALUES ('only',2)"})
	if _, err = l.row(context.Background(), o.ProfileID, ks, o.Name, r); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("static identity became a normal row", err)
	}
	query(t, c, Request{Statement: "DROP TABLE " + ks + ".rows"})
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".rows (p text,c int,s text static,v blob,PRIMARY KEY(p,c))"})
	if _, err = l.metadata(context.Background(), o.ProfileID, ks, o.Name); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("same-name UUID recreated target", err)
	}
	if _, err = l.count(context.Background(), o.ProfileID, ks, o.Name); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("changed target became usable again", err)
	}
	query(t, c, Request{Statement: "CREATE TYPE " + ks + ".nested (v text)"})
	query(t, c, Request{Statement: "CREATE TYPE " + ks + ".outer (nested frozen<nested>)"})
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".udt_rows (id int PRIMARY KEY,v frozen<outer>)"})
	u := s.Namespace(ks).Object("table", "udt_rows")
	udt := fixtureData(t, s, u)
	loadData(t, udt, contract.DataOptions{Limit: 20})
	query(t, c, Request{Statement: "ALTER TYPE " + ks + ".nested ADD more int"})
	if _, err = udt.metadata(context.Background(), u.ProfileID, ks, u.Name); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("nested UDT structure changed unnoticed", err)
	}
	// Frozen collection keys must use their exact native parameter bytes.
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".native_keys (p frozen<map<int,text>>,c int,v text,PRIMARY KEY(p,c))"})
	query(t, c, Request{Statement: "INSERT INTO " + ks + ".native_keys(p,c,v) VALUES ({1:'one',-2:'two'},7,'native key')"})
	keyObject := s.Namespace(ks).Object("table", "native_keys")
	keys := fixtureData(t, s, keyObject)
	keyPage := loadData(t, keys, contract.DataOptions{Limit: 20})
	if len(keyPage.Rows) != 1 {
		t.Fatal("frozen native key missing")
	}
	keyRow, err := keys.row(context.Background(), keyObject.ProfileID, ks, keyObject.Name, *keyPage.RowRefs[0])
	if err != nil || keyRow.Values["v"].Text != "native key" || keyRow.Values["p"].Kind != contract.JSON {
		t.Fatal("frozen composite identity", err)
	}
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".large_rows (id int PRIMARY KEY,v blob)"})
	query(t, c, Request{Statement: "INSERT INTO " + ks + ".large_rows(id,v) VALUES (1,?)", Parameters: []Parameter{{Bytes: bytes.Repeat([]byte{0x7f}, (1<<20)+1)}}})
	bigObject := s.Namespace(ks).Object("table", "large_rows")
	big := fixtureData(t, s, bigObject)
	bigPage := loadData(t, big, contract.DataOptions{Limit: 20})
	if _, err = big.row(context.Background(), bigObject.ProfileID, ks, bigObject.Name, *bigPage.RowRefs[0]); !errors.Is(err, ErrBounds) {
		t.Fatal("complete row exceeds 1 MiB", err)
	}
	bounded, err := big.chunk(context.Background(), bigObject.ProfileID, ks, bigObject.Name, "v", *bigPage.RowRefs[0], 0, 1024)
	if err != nil || len(bounded.Text) != 2048 || !bounded.HasMore {
		t.Fatal("explicit large native field chunk", err)
	}
	if err = big.exportRows(context.Background(), bigObject.ProfileID, ks, bigObject.Name, contract.DataOptions{Object: bigObject}, nil, func([]contract.DataValue) error { t.Error("oversized complete export published"); return nil }); !errors.Is(err, ErrBounds) {
		t.Fatal("oversized complete export", err)
	}
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".vectors (id int PRIMARY KEY,v vector<float,3>)"})
	query(t, c, Request{Statement: "INSERT INTO " + ks + ".vectors(id,v) VALUES (1,[1.0,2.0,3.0])"})
	vectorObject := s.Namespace(ks).Object("table", "vectors")
	vectors := fixtureData(t, s, vectorObject)
	vectorPage := loadData(t, vectors, contract.DataOptions{Limit: 20})
	vectorRow, err := vectors.row(context.Background(), vectorObject.ProfileID, ks, vectorObject.Name, *vectorPage.RowRefs[0])
	if err != nil || vectorRow.Values["v"].Kind != contract.Binary || len(vectorRow.Values["v"].Bytes) == 0 {
		t.Fatal("opaque custom vector was interpreted as text", err)
	}
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".view_base (id int PRIMARY KEY,n int)"})
	query(t, c, Request{Statement: "CREATE MATERIALIZED VIEW " + ks + ".rows_view AS SELECT * FROM " + ks + ".view_base WHERE id IS NOT NULL PRIMARY KEY(id)"})
	v := s.Namespace(ks).Object("view", "rows_view")
	view := fixtureData(t, s, v)
	loadData(t, view, contract.DataOptions{Limit: 20})
	query(t, c, Request{Statement: "DROP MATERIALIZED VIEW " + ks + ".rows_view"})
	query(t, c, Request{Statement: "DROP TABLE " + ks + ".view_base"})
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".view_base (id int PRIMARY KEY,n int)"})
	query(t, c, Request{Statement: "CREATE MATERIALIZED VIEW " + ks + ".rows_view AS SELECT * FROM " + ks + ".view_base WHERE id IS NOT NULL PRIMARY KEY(id)"})
	if _, err = view.metadata(context.Background(), v.ProfileID, ks, v.Name); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("same-name view/base UUID changed unnoticed", err)
	}
}

func TestCassandraNativeDataCancellationAndOriginalClose(t *testing.T) {
	cfg, secret := dataFixture(t)
	c := fixtureClient(t, cfg, secret)
	ks := fixtureKeyspace(t, c, cfg, secret)
	query(t, c, Request{Statement: "CREATE TABLE " + ks + ".rows (id int PRIMARY KEY,v text)"})
	query(t, c, Request{Statement: "INSERT INTO " + ks + ".rows(id,v) VALUES(1,'original')"})
	for _, mode := range []string{"count", "field", "export", "page", "close", "session_close"} {
		t.Run(mode, func(t *testing.T) {
			match := "SELECT COUNT(*) FROM " + quoteName(ks) + `."rows"`
			if mode == "field" {
				match = ` WHERE "id" = ? LIMIT 2`
			}
			if mode == "export" {
				match = `SELECT "id", "v" FROM `
			}
			if mode == "page" || mode == "close" || mode == "session_close" {
				match = `SELECT "id", "v" FROM `
			}
			var armed atomic.Bool
			seen := make(chan bool, 1)
			proxy := nativeFrameProxy(t, cfg, func(op byte, body []byte, response frame, front net.Conn) bool {
				if !armed.Load() || op != 7 || !bytes.Contains(body, []byte(match)) || !armed.CompareAndSwap(true, false) {
					return false
				}
				seen <- response.opcode == 8
				_, _ = io.Copy(io.Discard, front)
				return true
			})
			s := fixtureSession(t, proxy, secret, ks)
			o := s.Namespace(ks).Object("table", "rows")
			l := fixtureData(t, s, o)
			p := loadData(t, l, contract.DataOptions{Limit: 20})
			original := l.pageClient
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			armed.Store(true)
			go func() {
				var err error
				switch mode {
				case "count":
					_, err = l.count(ctx, o.ProfileID, ks, o.Name)
				case "field":
					_, err = l.chunk(ctx, o.ProfileID, ks, o.Name, "v", *p.RowRefs[0], 0, 100)
				case "export":
					err = l.exportRows(ctx, o.ProfileID, ks, o.Name, contract.DataOptions{Object: o}, nil, func([]contract.DataValue) error { t.Error("canceled export published withheld data"); return nil })
				default:
					_, err = l.page(ctx, o.ProfileID, ks, o.Name, contract.DataOptions{Object: o, Limit: 20})
				}
				finished <- err
			}()
			select {
			case ok := <-seen:
				if !ok {
					t.Fatal("native read did not acknowledge original upstream")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("native fault not reached")
			}
			closed := make(chan struct{})
			if mode == "close" {
				go func() { l.Close(); close(closed) }()
			} else if mode == "session_close" {
				go func() { s.Close(); close(closed) }()
			} else {
				cancel()
				close(closed)
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("canceled original read succeeded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("driver cancellation not acknowledged")
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("original close did not wait")
			}
			if mode == "count" || mode == "field" || mode == "export" {
				if l.pageClient != original || !original.Alive() {
					t.Fatal("auxiliary cancel retired page cursor")
				}
				loadData(t, l, contract.DataOptions{Limit: 20})
			} else {
				if original.Alive() {
					t.Fatal("original page connection retained after interruption")
				}
				if _, err := l.page(context.Background(), o.ProfileID, ks, o.Name, contract.DataOptions{Object: o, Limit: 20}); err == nil {
					t.Fatal("document implicitly reconnected")
				}
			}
			if mode != "session_close" && !s.Alive() {
				t.Fatal("independent page loss revoked metadata")
			}
		})
	}
}
