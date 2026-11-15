package tdengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const nativeImage = "tdengine/tdengine@sha256:79d32ce8a42f64c7dbfc33946a36b3e176ea23c0cd695a1bc9e8aa889ca03b88"

func nativeProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_TDENGINE_IT") != "1" {
		t.Skip("requires explicitly owned official TDengine")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_TDENGINE_CONFIG"))
	if err != nil {
		t.Fatal("missing private fixture configuration")
	}
	var cfg struct {
		Host, Username, PasswordFile, Image, Version, Container string
		Port                                                    uint16
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.Host != "127.0.0.1" || cfg.Port == 0 || cfg.Image != nativeImage || cfg.Version != "3.3.6.13" || cfg.Container == "" {
		t.Fatal("fixture must be pinned official loopback product")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("missing private fixture credential")
	}
	p, _ := connections.Defaults(connections.TDengine)
	p.ID, p.Name, p.Host, p.Port, p.Username = 741, "native TDengine", cfg.Host, cfg.Port, cfg.Username
	t.Logf("official TDengine version=%s image=%s original_container=%s", cfg.Version, cfg.Image, cfg.Container)
	return p, string(secret)
}

func nativeSession(t *testing.T, p connections.Profile, password string) *Session {
	t.Helper()
	value, err := OpenSession(context.Background(), p, password)
	if err != nil {
		t.Fatal("native session failed", err)
	}
	s := value.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func nativeLease(t *testing.T, s *Session, doc uint64) contract.QueryLease {
	t.Helper()
	l, err := s.ReserveQuery(doc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	return l
}
func nativeSQL(t *testing.T, l contract.QueryLease, db, sql string) contract.SQLResult {
	t.Helper()
	r, err := l.Execute(context.Background(), db, sql)
	if err != nil {
		t.Fatal("native SQL failed", err)
	}
	return r
}
func nativeDatabase(t *testing.T, l contract.QueryLease, precision string) string {
	t.Helper()
	db := "native_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	nativeSQL(t, l, "", "CREATE DATABASE "+quote(db)+" PRECISION "+literal(precision))
	t.Cleanup(func() { _, _ = l.Execute(context.Background(), "", "DROP DATABASE "+quote(db)) })
	return db
}
func nativeEvidence(t *testing.T, name string, value any) {
	t.Helper()
	folder := os.Getenv("NYADB_TDENGINE_EVIDENCE")
	if folder == "" {
		return
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(folder, name+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNativeTDengineOriginalTypesMetadataPagingAndFullExport(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	l := nativeLease(t, s, 1)
	db := nativeDatabase(t, l, "ns")
	nativeSQL(t, l, db, "CREATE TABLE `exact`(ts TIMESTAMP,s BIGINT,u BIGINT UNSIGNED,f FLOAT,d DOUBLE,txt VARCHAR(12000),uni NCHAR(255),bin VARBINARY(12000),nullable VARCHAR(32),b BOOL,dec DECIMAL(38,10))")
	insert := nativeSQL(t, l, db, "INSERT INTO `exact` VALUES(1704067200123456789,9007199254740993,18446744073709551614,8.6,8.6,'','原始 Ω','\\x00ff',NULL,true,'-12345678901234567890.1234567890')")
	if insert.AffectedRows != 1 || len(insert.Steps) != 1 || insert.Steps[0].State != "completed" || !strings.Contains(insert.NativeJSON, `"action":"binary_query"`) {
		t.Fatal("original write receipt lost", insert)
	}
	o := s.Namespace(db).Object("table", "exact")
	metadata, err := s.TableMetadata(context.Background(), o)
	if err != nil || len(metadata.Columns) != 11 || metadata.Columns[0].Key != "PRI" || metadata.Editable || !strings.Contains(metadata.Definition, "CREATE TABLE") {
		t.Fatal("original native metadata lost", err, metadata)
	}
	r := nativeSQL(t, l, db, "SELECT * FROM `exact`")
	if len(r.Sets) != 1 || len(r.Sets[0].Rows) != 1 {
		t.Fatal(r)
	}
	row := r.Sets[0].Rows[0]
	if row[0].Text != "2024-01-01T00:00:00.123456789Z" || row[1].Text != "9007199254740993" || row[2].Text != "18446744073709551614" || row[3].Text != "8.6" || row[4].Text != "8.6" || row[5].Kind != contract.Text || row[5].Text != "" || row[6].Text != "原始 Ω" || row[7].Kind != contract.Binary || !bytes.Equal(row[7].Bytes, []byte{0, 255}) || row[8].Kind != contract.Null || row[9].Text != "true" || row[10].Text != "-12345678901234567890.1234567890" {
		t.Fatal("exact native data changed", row)
	}
	nativeSQL(t, l, db, "INSERT INTO `exact`(ts,txt,bin) VALUES(1704067201123456789,"+literal(strings.Repeat("x", 6000))+",'\\x"+strings.Repeat("ff", 6000)+"')")
	page, err := s.page(context.Background(), p.ID, db, "exact", contract.DataOptions{Object: o, Limit: 2, SortColumn: "ts"})
	if err != nil || len(page.Rows) != 2 || page.HasMore || !page.Rows[1][5].Truncated || !page.Rows[1][7].Truncated || page.Rows[0][8].Value.Kind != contract.Null {
		t.Fatal("bounded page data changed", err, page)
	}
	var full []contract.DataValue
	rows := 0
	err = s.export(context.Background(), p.ID, db, "exact", contract.DataOptions{Object: o, SortColumn: "ts"}, nil, func(v []contract.DataValue) error { rows++; full = v; return nil })
	if err != nil || rows != 2 || len(full[5].Text) != 6000 || len(full[7].Bytes) != 6000 {
		t.Fatal("full export lost original values", err, rows)
	}
	var streamed [][]contract.Cell
	streamResult, err := l.ExecuteStream(context.Background(), db, "SELECT * FROM `exact` ORDER BY ts", func(_ int, columns []contract.Column, row []contract.Cell) error {
		streamed = append(streamed, row)
		columns[0].Name = "changed by sink"
		return nil
	})
	if err != nil || !streamResult.Truncated || len(streamed) != 2 || len(streamed[1][5].Text) != 6000 || len(streamed[1][7].Bytes) != 6000 || streamResult.Sets[0].Columns[0].Name != "ts" {
		t.Fatal("preview or sink mutation changed complete original stream", err)
	}
	nativeSQL(t, l, db, "CREATE TABLE `page_rows`(ts TIMESTAMP,v INT)")
	var sql strings.Builder
	sql.WriteString("INSERT INTO `page_rows` VALUES ")
	for i := 0; i < 1051; i++ {
		sql.WriteString("(" + strconv.FormatInt(1704067200123456789+int64(i), 10) + "," + strconv.Itoa(i) + ")")
	}
	nativeSQL(t, l, db, sql.String())
	for _, test := range []struct {
		offset int64
		count  int
		more   bool
	}{{0, 512, true}, {512, 512, true}, {1024, 27, false}} {
		page, err = s.page(context.Background(), p.ID, db, "page_rows", contract.DataOptions{Offset: test.offset, Limit: 512, SortColumn: "ts"})
		if err != nil || len(page.Rows) != test.count || page.HasMore != test.more || page.Rows[0][1].Value.Text != strconv.FormatInt(test.offset, 10) {
			t.Fatal("native cursor/page boundary lost", err, page)
		}
	}
	count, err := s.count(context.Background(), p.ID, db, "page_rows")
	if err != nil || count != 1051 {
		t.Fatal(count, err)
	}
	nativeEvidence(t, "typed-original-result-and-page", struct {
		Result   contract.SQLResult
		Insert   contract.SQLResult
		Metadata *contract.TableStructure
		LastPage *contract.DataPage
	}{r, insert, metadata, page})
}

func TestNativeTDengineStableChildHierarchyAndCompositeIdentity(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	l := nativeLease(t, s, 1)
	db := nativeDatabase(t, l, "ns")
	nativeSQL(t, l, db, "CREATE STABLE `meters`(ts TIMESTAMP,v INT) TAGS(site NCHAR(64)); CREATE TABLE `child_a` USING `meters` TAGS('A'); CREATE TABLE `child_b` USING `meters` TAGS('B'); INSERT INTO `child_a` VALUES(1704067200123456789,1); INSERT INTO `child_b` VALUES(1704067200123456789,2); CREATE TABLE `composite`(ts TIMESTAMP,k INT COMPOSITE KEY,v INT)")
	objects, err := s.Browse(context.Background(), s.Namespace(db))
	if err != nil || len(objects) != 4 {
		t.Fatal(err, objects)
	}
	for _, o := range objects {
		m, err := s.TableMetadata(context.Background(), o.Ref)
		if err != nil {
			t.Fatal(err, o)
		}
		if o.Name == "child_a" || o.Name == "child_b" {
			if o.ParentObject != s.Namespace(db).Object("stable", "meters") || m.Columns[2].Comment != "TAG" {
				t.Fatal("child owner or tag lost", o, m)
			}
		}
		if o.Ref.Kind == "stable" && m.Columns[0].Key != "" {
			t.Fatal("stable timestamp falsely unique")
		}
		if o.Name == "composite" && (m.Columns[0].Key != "PRI" || m.Columns[1].Key != "PRI") {
			t.Fatal("native composite key lost", m)
		}
	}
	page, err := s.page(context.Background(), p.ID, db, "meters", contract.DataOptions{Limit: 10})
	if err != nil || len(page.Rows) != 2 || page.Editable == nil || *page.Editable || s.DataRuntime().Saver != nil {
		t.Fatal("stable rows or unsupported mutation semantics changed", err, page)
	}
	nativeEvidence(t, "original-stable-child-identities", objects)
}

func TestNativeTDengineSequentialFailureReceiptsAndPhysicalIsolation(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	first := nativeLease(t, s, 1)
	second := nativeLease(t, s, 2)
	a := nativeDatabase(t, first, "ms")
	b := nativeDatabase(t, second, "us")
	nativeSQL(t, first, a, "CREATE TABLE `items`(ts TIMESTAMP,v INT)")
	nativeSQL(t, second, b, "CREATE TABLE `items`(ts TIMESTAMP,v INT)")
	r, err := first.Execute(context.Background(), a, "INSERT INTO `items` VALUES(1704067200123,1); UPDATE `items` SET v=2; INSERT INTO `items` VALUES(1704067200124,3)")
	if !isServerError(err) || len(r.Steps) != 2 || r.Steps[0].State != "completed" || r.Steps[1].State != "failed" || r.AffectedRows != 1 || !strings.Contains(r.NativeJSON, `"affected_rows":1`) {
		t.Fatal("non-atomic prefix/error receipt lost", err, r)
	}
	var receipts []json.RawMessage
	if json.Unmarshal([]byte(r.NativeJSON), &receipts) != nil || len(receipts) != 3 {
		t.Fatal("original USE/write/failure acknowledgements lost")
	}
	if n, err := s.count(context.Background(), p.ID, a, "items"); err != nil || n != 1 {
		t.Fatal("acknowledged prefix rolled back", err, n)
	}
	r = nativeSQL(t, second, "", "SELECT count(*) FROM `items`")
	if r.Sets[0].Rows[0][0].Text != "0" {
		t.Fatal("physical selection crossed lease")
	}
	r = nativeSQL(t, first, "", "SELECT * FROM `items`")
	if r.Sets[0].Rows[0][0].Text != "2024-01-01T00:00:00.123Z" {
		t.Fatal("millisecond precision lost", r)
	}
	nativeSQL(t, second, "", "INSERT INTO `items` VALUES(1704067200123456,2)")
	r = nativeSQL(t, second, "", "SELECT * FROM `items`")
	if r.Sets[0].Rows[0][0].Text != "2024-01-01T00:00:00.123456Z" {
		t.Fatal("microsecond precision lost", r)
	}
	nativeSQL(t, first, "", "INSERT INTO `items` VALUES(1704067200123,4)")
	r = nativeSQL(t, first, "", "SELECT * FROM `items`")
	if r.Sets[0].Rows[0][1].Text != "4" {
		t.Fatal("lease could not continue after acknowledged server error", r)
	}
	file, ok := first.(contract.QueryFileLease)
	if !ok {
		t.Fatal("native SQL file executor missing")
	}
	fileResult, err := file.ExecuteFile(context.Background(), a, strings.NewReader("SELECT 'original\\' ; text'; SELECT count(*) FROM `items`"))
	if err != nil || len(fileResult.Steps) != 2 || fileResult.Sets[0].Rows[0][0].Text != "original' ; text" || fileResult.Sets[1].Rows[0][0].Text != "1" {
		t.Fatal("native SQL file splitting changed escaped statement boundaries", err, fileResult)
	}
}

func TestNativeTDengineReadOnlyCredentialsAndClosedGeneration(t *testing.T) {
	p, password := nativeProfile(t)
	if value, err := OpenSession(context.Background(), p, "invalid-original-password"); err == nil || value != nil || strings.Contains(err.Error(), "invalid-original-password") {
		t.Fatal("native authentication or secret redaction failed")
	}
	s := nativeSession(t, p, password)
	l := nativeLease(t, s, 1)
	db := nativeDatabase(t, l, "ns")
	p.Database, p.TDengineOptions.ReadOnly = db, true
	read := nativeSession(t, p, password)
	readonlyLease := nativeLease(t, read, 2)
	for _, q := range []string{"INSERT INTO missing VALUES(1,1)", "SELECT 1; CREATE TABLE missing(ts TIMESTAMP,v INT)"} {
		r, err := readonlyLease.Execute(context.Background(), db, q)
		if !errors.Is(err, contract.ErrUnsupported) || len(r.Steps) > 0 || r.NativeJSON != "" {
			t.Fatal("read-only write prefix dispatched", err, r)
		}
	}
	nativeSQL(t, readonlyLease, db, "SHOW TABLES")
	if _, err := s.Browse(context.Background(), contract.NamespaceRef{ProfileID: p.ID + 1, Catalog: db}); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign profile admitted", err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readonlyLease.Execute(context.Background(), db, "SELECT 1"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original lease remained live", err)
	}
}
