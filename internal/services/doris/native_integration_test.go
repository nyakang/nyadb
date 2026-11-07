package doris

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func fixtureProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_DORIS_IT") != "1" {
		t.Skip("requires explicit disposable native Doris fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_DORIS_CONFIG"))
	if err != nil {
		t.Fatal("missing private Doris fixture configuration")
	}
	var cfg struct {
		Endpoint, Username, Database string
		PasswordFile                 string `json:"password_file"`
		CAFile                       string `json:"ca_file"`
		ServerName                   string `json:"server_name"`
	}
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal("invalid private fixture configuration")
	}
	if !strings.HasPrefix(cfg.Endpoint, "127.0.0.1:") || !strings.HasPrefix(cfg.Database, "nyadb_doris_") {
		t.Fatal("requires owned loopback fixture")
	}
	port, err := strconv.ParseUint(strings.TrimPrefix(cfg.Endpoint, "127.0.0.1:"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("invalid fixture port")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("missing private fixture credential")
	}
	p, _ := connections.Defaults(connections.Doris)
	p.ID = 41
	p.Name = "native fixture"
	p.Host = "127.0.0.1"
	p.Port = uint16(port)
	p.Database = cfg.Database
	p.Username = cfg.Username
	p.TLSMode = "disable"
	if cfg.CAFile != "" {
		p.TLSMode = "verify-full"
		p.DorisOptions.CAFile = cfg.CAFile
		p.DorisOptions.ServerName = cfg.ServerName
	}
	return p, string(secret)
}

func TestDorisNativeWorkbenchFixture(t *testing.T) {
	p, password := fixtureProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	raw, err := OpenSession(ctx, p, password)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	l, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	name := "records_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	object := s.Namespace(p.Database).Object("TABLE", name)
	run := func(sql string) contract.SQLResult {
		t.Helper()
		r, e := l.Execute(ctx, p.Database, sql)
		if e != nil {
			t.Fatalf("native query failed: %v", e)
		}
		return r
	}
	run("CREATE TABLE " + qualified(object) + " (ID BIGINT NOT NULL,W LARGEINT,N DECIMAL(76,38),F DOUBLE,T STRING,E VARCHAR(256),D DATEV2,TS DATETIMEV2(6),J JSONB,A ARRAY<INT>,M MAP<STRING,INT>,S STRUCT<a:INT,b:STRING>) DUPLICATE KEY(ID) DISTRIBUTED BY HASH(ID) BUCKETS 1 PROPERTIES ('replication_num'='1')")
	defer func() {
		cleanup, e := s.ReserveQuery(90)
		if e == nil {
			defer cleanup.Release()
			_, _ = cleanup.Execute(context.Background(), p.Database, "DROP TABLE IF EXISTS "+qualified(object))
		}
	}()
	run("INSERT INTO " + qualified(object) + " VALUES(9007199254740993,170141183460469231731687303715884105727,12345678901234567890123456789012345678.12345678901234567890123456789012345678,1.2345678901234567,'中文🙂','', '2026-11-07','2026-11-07 12:34:56.123456',CAST('{\"n\":9007199254740993}' AS JSONB),[1,NULL,2147483647],MAP('key',1),NAMED_STRUCT('a',1,'b','中文'))")
	ns, err := s.Namespaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range ns {
		if n.Ref == object.Namespace() {
			found = true
		}
	}
	if !found {
		t.Fatal("qualified internal catalog/database absent")
	}
	objects, err := s.Browse(ctx, object.Namespace())
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, o := range objects {
		if o.Ref == object {
			found = true
		}
	}
	if !found {
		t.Fatal("qualified native object absent")
	}
	table, err := s.TableMetadata(ctx, object)
	if err != nil {
		t.Fatal(err)
	}
	if table.Editable || len(table.Columns) != 12 || len(table.Indexes) != 0 || !strings.Contains(table.Definition, "DUPLICATE KEY") {
		t.Fatal("Doris model misreported as relational identity")
	}
	page, err := s.page(ctx, p.ID, p.Database, name, contract.DataOptions{Object: object, Limit: 2, SortColumn: "ID"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Editable == nil || *page.Editable || page.StableOrder || page.KeysetAvailable || len(page.Rows) != 1 {
		t.Fatal("incorrect read-only paging semantics")
	}
	row := page.Rows[0]
	if row[0].Text != "9007199254740993" || row[1].Text != "170141183460469231731687303715884105727" || row[1].Value.Kind != contract.Integer || row[2].Text != "12345678901234567890123456789012345678.12345678901234567890123456789012345678" || row[3].Text != "1.2345678901234567" || row[4].Text != "中文🙂" || row[5].Null || row[5].Text != "" || row[7].Text != "2026-11-07 12:34:56.123456" {
		t.Fatal("native precise scalar read mismatch")
	}
	count, err := s.count(ctx, p.ID, p.Database, name)
	if err != nil || count != 1 {
		t.Fatalf("count failed: %v", err)
	}
	var exported []contract.DataValue
	err = s.export(ctx, p.ID, p.Database, name, contract.DataOptions{Object: object}, []string{"W", "N", "F"}, func(v []contract.DataValue) error { exported = v; return nil })
	if err != nil || len(exported) != 3 || exported[0].Kind != contract.Integer || exported[2].Text != "1.2345678901234567" {
		t.Fatalf("typed exact export failed: %v", err)
	}
	var out bytes.Buffer
	if err = s.jsonl(ctx, object, &out); err != nil {
		t.Fatal(err)
	}
	var jsonrow []contract.Cell
	if err = json.Unmarshal(bytes.TrimSpace(out.Bytes()), &jsonrow); err != nil || len(jsonrow) != 12 || jsonrow[2].Text != row[2].Text {
		t.Fatal("JSONL lost decimal precision")
	}
	result := run("SET @nyadb_value=123; SELECT @nyadb_value,F FROM " + qualified(object) + "; EXPLAIN SELECT * FROM " + qualified(object))
	if len(result.Sets) != 2 || result.Sets[0].Rows[0][1].Text != "1.2345678901234567" || len(result.Sets[1].Rows) == 0 {
		t.Fatal("native SQL binary results or text EXPLAIN failed")
	}
	run("INSERT INTO " + qualified(object) + " (ID) VALUES(9007199254740993)")
	page, err = s.page(ctx, p.ID, p.Database, name, contract.DataOptions{Object: object, Limit: 1, SortColumn: "ID"})
	if err != nil || !page.HasMore {
		t.Fatalf("duplicate-key model or LIMIT+1 failed: %v", err)
	}
	page, err = s.page(ctx, p.ID, p.Database, name, contract.DataOptions{Object: object, Limit: 2, FilterColumn: "F", FilterMode: "is_null"})
	if err != nil || len(page.Rows) != 1 || !page.Rows[0][3].Null {
		t.Fatalf("NULL predicate failed: %v", err)
	}
	for _, bad := range []contract.DataOptions{{Object: object, Limit: 1, UseKeyset: true}, {Object: object, Limit: 1, FilterColumn: "A", FilterMode: "equal", FilterValue: "[]"}, {Object: contract.ObjectRef{ProfileID: p.ID, Catalog: "other", Schema: p.Database, Kind: "TABLE", Name: name}, Limit: 1}} {
		if _, err = s.page(ctx, p.ID, p.Database, name, bad); err == nil {
			t.Fatal("unsupported or foreign identity accepted")
		}
	}
	if _, err = l.Execute(ctx, p.Database, "INSERT INTO "+qualified(object)+"(ID) VALUES(2); BEGIN"); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("unsupported transaction batch accepted")
	}
	if n, _ := s.count(ctx, p.ID, p.Database, name); n != 2 {
		t.Fatal("preflight rejection executed preceding INSERT")
	}
	if _, err = l.Execute(ctx, p.Database, "SELECT definitely_missing FROM "+qualified(object)); err == nil || errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("acknowledged server rejection retired lease")
	}
	run("SELECT @nyadb_value")
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	_, err = l.Execute(short, p.Database, "SELECT SLEEP(5)")
	if !errors.Is(err, contract.ErrSQLSessionLost) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel did not retire owning physical lease: %v", err)
	}
	if _, err = l.Execute(ctx, p.Database, "INSERT INTO "+qualified(object)+"(ID) VALUES(3)"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("lost lease implicitly redialed")
	}
	if n, _ := s.count(ctx, p.ID, p.Database, name); n != 2 {
		t.Fatal("lost command replayed")
	}
	replacement, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Release()
	result, err = replacement.Execute(ctx, p.Database, "SELECT @nyadb_value")
	if err != nil || result.Sets[0].Rows[0][0].Kind != contract.Null {
		t.Fatal("explicit recovery retained retired physical settings")
	}
}

func TestDorisNativeOpaqueStateFixture(t *testing.T) {
	p, password := fixtureProfile(t)
	ctx := context.Background()
	raw, err := OpenSession(ctx, p, password)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	l, _ := s.ReserveQuery(2)
	defer l.Release()
	name := "agg_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	o := s.Namespace(p.Database).Object("TABLE", name)
	for _, q := range []string{"CREATE TABLE " + qualified(o) + "(ID BIGINT,B BITMAP BITMAP_UNION,H HLL HLL_UNION) AGGREGATE KEY(ID) DISTRIBUTED BY HASH(ID) BUCKETS 1 PROPERTIES ('replication_num'='1')", "INSERT INTO " + qualified(o) + " VALUES(1,TO_BITMAP(123),HLL_HASH('value'))"} {
		if _, err = l.Execute(ctx, p.Database, q); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { _, _ = l.Execute(ctx, p.Database, "DROP TABLE "+qualified(o)) }()
	page, err := s.page(ctx, p.ID, p.Database, name, contract.DataOptions{Object: o, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{1, 2} {
		c := page.Rows[0][i]
		if c.Null || c.Value.Kind != contract.Binary || len(c.Value.Bytes) != 10 {
			t.Fatal("opaque aggregate state became NULL or text")
		}
	}
	var values []contract.DataValue
	err = s.export(ctx, p.ID, p.Database, name, contract.DataOptions{Object: o}, nil, func(v []contract.DataValue) error { values = v; return nil })
	if err != nil || !bytes.Equal(values[1].Bytes, page.Rows[0][1].Value.Bytes) || !bytes.Equal(values[2].Bytes, page.Rows[0][2].Value.Bytes) {
		t.Fatal("opaque export changed native bytes")
	}
	r, err := l.Execute(ctx, p.Database, "SELECT B,H,BITMAP_COUNT(B),HLL_CARDINALITY(H) FROM "+qualified(o))
	if err != nil || r.Sets[0].Rows[0][0].Kind != contract.Binary || r.Sets[0].Rows[0][2].Text != "1" {
		t.Fatal("SQL opaque state lost bytes")
	}
}

func TestDorisNativeAuthenticationFixture(t *testing.T) {
	p, password := fixtureProfile(t)
	ctx := context.Background()
	if s, err := OpenSession(ctx, p, password+"wrong"); err == nil {
		_ = s.Close()
		t.Fatal("wrong password accepted")
	}
	if p.TLSMode == "verify-full" {
		untrusted := p
		untrusted.DorisOptions.CAFile = ""
		if s, err := OpenSession(ctx, untrusted, password); err == nil {
			_ = s.Close()
			t.Fatal("untrusted TLS CA accepted")
		}
		p.DorisOptions.ServerName = "wrong.invalid"
		if s, err := OpenSession(ctx, p, password); err == nil {
			_ = s.Close()
			t.Fatal("wrong TLS server name accepted")
		}
	}
}

func TestDorisNativeCompleteExportFixture(t *testing.T) {
	p, password := fixtureProfile(t)
	ctx := context.Background()
	raw, err := OpenSession(ctx, p, password)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	l, err := s.ReserveQuery(3)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	name := "full_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	o := s.Namespace(p.Database).Object("TABLE", name)
	q := "CREATE TABLE " + qualified(o) + "(ID BIGINT,T STRING) DUPLICATE KEY(ID) DISTRIBUTED BY HASH(ID) BUCKETS 1 PROPERTIES ('replication_num'='1'); INSERT INTO " + qualified(o) + " VALUES(1,REPEAT('🙂',262144)),(2,''),(3,NULL)"
	if _, err = l.Execute(ctx, p.Database, q); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = l.Execute(ctx, p.Database, "DROP TABLE "+qualified(o)) }()
	page, err := s.page(ctx, p.ID, p.Database, name, contract.DataOptions{Object: o, Limit: 3, SortColumn: "ID"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 3 || !page.Rows[0][1].Truncated || page.Rows[0][1].Text != strings.Repeat("🙂", 1024) || page.Rows[1][1].Null || page.Rows[1][1].Text != "" || !page.Rows[2][1].Null {
		t.Fatal("bounded Unicode, empty or NULL preview failed")
	}
	n := 0
	err = s.export(ctx, p.ID, p.Database, name, contract.DataOptions{Object: o, SortColumn: "ID"}, []string{"T"}, func(v []contract.DataValue) error {
		n++
		if n == 1 && v[0].Text != strings.Repeat("🙂", 262144) {
			t.Fatal("complete export reused truncated preview")
		}
		if n == 2 && (v[0].Null || v[0].Text != "") {
			t.Fatal("empty export became NULL")
		}
		if n == 3 && !v[0].Null {
			t.Fatal("NULL export became empty")
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("complete Unicode export failed: %v", err)
	}
	if _, err = l.Execute(ctx, p.Database, "INSERT INTO "+qualified(o)+" VALUES(4,REPEAT('x',1048577))"); err != nil {
		t.Fatal(err)
	}
	err = s.export(ctx, p.ID, p.Database, name, contract.DataOptions{Object: o, FilterColumn: "ID", FilterValue: "4"}, nil, func([]contract.DataValue) error { t.Fatal("oversized field silently truncated"); return nil })
	if err == nil {
		t.Fatal("over-limit complete field exported")
	}
}
