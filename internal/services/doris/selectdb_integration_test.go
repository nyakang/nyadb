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
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func selectDBFixtureProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_SELECTDB_IT") != "1" {
		t.Skip("requires an explicitly owned real SelectDB fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_SELECTDB_CONFIG"))
	if err != nil {
		t.Fatal("missing private SelectDB fixture configuration")
	}
	var cfg struct {
		Endpoint, Username, Database string
		PasswordFile                 string `json:"password_file"`
		CAFile                       string `json:"ca_file"`
		ServerName                   string `json:"server_name"`
	}
	if json.Unmarshal(raw, &cfg) != nil || !strings.HasPrefix(cfg.Endpoint, "127.0.0.1:") || !strings.HasPrefix(cfg.Database, "nyadb_selectdb_") {
		t.Fatal("requires a nonce-owned loopback SelectDB fixture")
	}
	port, err := strconv.ParseUint(strings.TrimPrefix(cfg.Endpoint, "127.0.0.1:"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("invalid SelectDB fixture endpoint")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("missing private SelectDB fixture credential")
	}
	p, _ := connections.Defaults(connections.SelectDB)
	p.ID, p.Name, p.Host, p.Port, p.Database, p.Username, p.TLSMode = 171, "SelectDB fixture", "127.0.0.1", uint16(port), cfg.Database, cfg.Username, "disable"
	if cfg.CAFile != "" {
		p.TLSMode, p.SelectDBOptions.CAFile, p.SelectDBOptions.ServerName = "verify-full", cfg.CAFile, cfg.ServerName
	}
	return p, string(secret)
}

func TestSelectDBNativeCatalogPrecisionExportAndOriginalLeaseFixture(t *testing.T) {
	p, password := selectDBFixtureProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	raw, err := OpenSelectDBSession(ctx, p, password)
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
	o := s.Namespace(p.Database).Object("TABLE", name)
	run := func(q string) contract.SQLResult {
		t.Helper()
		r, e := l.Execute(ctx, p.Database, q)
		if e != nil {
			t.Fatalf("native SelectDB command failed: %v", e)
		}
		return r
	}
	run("CREATE TABLE " + qualified(o) + "(id BIGINT NOT NULL,w LARGEINT,n DECIMAL(38,20),note STRING,e VARCHAR(128)) DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES ('replication_num'='1')")
	defer func() {
		cleanup, e := s.ReserveQuery(90)
		if e == nil {
			defer cleanup.Release()
			_, _ = cleanup.Execute(context.Background(), p.Database, "DROP TABLE IF EXISTS "+qualified(o))
		}
	}()
	run("INSERT INTO " + qualified(o) + " VALUES(9007199254740993,170141183460469231731687303715884105727,9007199254740993.12345678901234567890,REPEAT('🙂',2048),''),(2,NULL,NULL,NULL,NULL)")
	namespaces, err := s.Namespaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, namespace := range namespaces {
		found = found || namespace.Ref == o.Namespace()
	}
	if !found {
		t.Fatal("SelectDB namespace identity lost")
	}
	objects, err := s.Browse(ctx, o.Namespace())
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, object := range objects {
		found = found || object.Ref == o
	}
	if !found {
		t.Fatal("SelectDB four-column native catalog shape lost the table")
	}
	structure, err := s.TableMetadata(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if structure.Editable || len(structure.Columns) != 5 || len(structure.Indexes) != 0 || structure.Columns[0].Key != "SORT" || !strings.Contains(structure.Definition, "DUPLICATE KEY") || !strings.Contains(structure.Definition, "DISTRIBUTED BY HASH") {
		t.Fatal("SelectDB storage model was replaced by MySQL relational semantics")
	}
	reads := s.DataReads()
	page, err := reads.AdvancedLoader(ctx, p.ID, p.Database, name, contract.DataOptions{Object: o, Limit: 2, SortColumn: "id", SortDescending: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 || page.Editable == nil || *page.Editable || page.KeysetAvailable || page.StableOrder {
		t.Fatal("SelectDB duplicate-model rows gained a writable or stable identity")
	}
	row := page.Rows[0]
	if row[0].Text != "9007199254740993" || row[1].Text != "170141183460469231731687303715884105727" || row[1].Value.Kind != contract.Integer || row[2].Text != "9007199254740993.12345678901234567890" || row[2].Value.Kind != contract.Decimal || !row[3].Truncated || row[3].Text != strings.Repeat("🙂", 1024) || row[4].Null || row[4].Text != "" || !page.Rows[1][3].Null {
		t.Fatal("native exact values, bounded Unicode or NULL semantics changed")
	}
	var exported []contract.DataValue
	err = s.DataExports().Exporter(ctx, p.ID, p.Database, name, contract.DataOptions{Object: o, FilterColumn: "id", FilterValue: "9007199254740993"}, []string{"w", "n", "note"}, func(v []contract.DataValue) error { exported = v; return nil })
	if err != nil || len(exported) != 3 || exported[0].Kind != contract.Integer || exported[1].Text != row[2].Text || exported[2].Text != strings.Repeat("🙂", 2048) {
		t.Fatalf("SelectDB export lost complete original values: %v", err)
	}
	var out bytes.Buffer
	if err = s.DataExports().JSONL(ctx, o, &out); err != nil || !bytes.Contains(out.Bytes(), []byte("9007199254740993.12345678901234567890")) {
		t.Fatal("SelectDB JSONL lost decimal precision")
	}
	page, err = reads.AdvancedLoader(ctx, p.ID, p.Database, name, contract.DataOptions{Object: o, Limit: 1, SortColumn: "id"})
	if err != nil || !page.HasMore {
		t.Fatal("SelectDB LIMIT+1 paging failed")
	}
	page, err = reads.AdvancedLoader(ctx, p.ID, p.Database, name, contract.DataOptions{Object: o, Limit: 2, FilterColumn: "e", FilterValue: "' OR 1=1 --"})
	if err != nil || len(page.Rows) != 0 {
		t.Fatal("SelectDB text-protocol filter escaped the original value")
	}
	count, err := reads.Count(ctx, p.ID, p.Database, name)
	if err != nil || count != 2 {
		t.Fatal("SelectDB count failed")
	}
	run("SET @nyadb_original=987654321")
	if _, err = l.Execute(ctx, p.Database, "SELECT missing_column FROM "+qualified(o)); err == nil || errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("acknowledged SelectDB rejection retired the original lease")
	}
	if result := run("SELECT @nyadb_original; EXPLAIN SELECT * FROM " + qualified(o)); len(result.Sets) != 2 || result.Sets[0].Rows[0][0].Text != "987654321" || len(result.Sets[1].Rows) == 0 {
		t.Fatal("SelectDB original physical state or textual plan lost")
	}
	for _, source := range []string{
		"SELECT 1", "SELECT note FROM " + qualified(o) + " WHERE id>0 ORDER BY id LIMIT 2",
		"SELECT a.id,SUM(b.id) FROM " + qualified(o) + " a JOIN " + qualified(o) + " b ON a.id=b.id GROUP BY a.id",
		"SELECT id FROM " + qualified(o) + " UNION ALL SELECT id FROM " + qualified(o),
		"WITH t AS (SELECT id FROM " + qualified(o) + ") SELECT id FROM t WHERE id>0",
		"SELECT id,ROW_NUMBER() OVER(ORDER BY id) FROM " + qualified(o),
		"INSERT INTO " + qualified(o) + "(id) VALUES(99)",
	} {
		q, err := queryplan.Build("selectdb", source)
		if err != nil {
			t.Fatal(err)
		}
		r := run(q)
		plan, err := queryplan.Parse("selectdb", r)
		if err != nil || plan.Engine != "selectdb" || len(plan.Nodes) != 1 || plan.Nodes[0].Cost == nil || plan.Nodes[0].Rows != nil {
			t.Fatalf("SelectDB native optimized plan shape was lost: %v (%+v)", err, r.Sets)
		}
	}
	if count, err = reads.Count(ctx, p.ID, p.Database, name); err != nil || count != 2 {
		t.Fatal("SelectDB EXPLAIN executed its INSERT")
	}
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	_, err = l.Execute(short, p.Database, "SELECT SLEEP(5)")
	if !errors.Is(err, contract.ErrSQLSessionLost) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SelectDB cancellation did not settle the original executor: %v", err)
	}
	if _, err = l.Execute(ctx, p.Database, "INSERT INTO "+qualified(o)+"(id) VALUES(3)"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("SelectDB lost lease automatically reconnected")
	}
	if count, err = reads.Count(ctx, p.ID, p.Database, name); err != nil || count != 2 {
		t.Fatal("SelectDB lost command was replayed")
	}
	replacement, err := l.Replace()
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Release()
	result, err := replacement.Execute(ctx, p.Database, "SELECT @nyadb_original")
	if err != nil || result.Sets[0].Rows[0][0].Kind != contract.Null {
		t.Fatal("SelectDB explicit recovery reused retired physical state")
	}
	wrong := o
	wrong.Catalog = "other"
	if _, err = s.TableMetadata(ctx, wrong); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("SelectDB metadata admitted another catalog")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = replacement.Execute(ctx, p.Database, "SELECT 1"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("SelectDB close did not revoke captured leases")
	}
}

func TestSelectDBNativeAuthenticationAndProductIdentityFixture(t *testing.T) {
	p, password := selectDBFixtureProfile(t)
	if s, err := OpenSelectDBSession(context.Background(), p, password+"wrong"); err == nil {
		_ = s.Close()
		t.Fatal("SelectDB accepted incorrect credentials")
	}
	if p.TLSMode == "verify-full" {
		untrusted := p
		untrusted.SelectDBOptions.CAFile = ""
		if s, err := OpenSelectDBSession(context.Background(), untrusted, password); err == nil {
			_ = s.Close()
			t.Fatal("SelectDB accepted an untrusted certificate")
		}
		wrongName := p
		wrongName.SelectDBOptions.ServerName = "wrong.invalid"
		if s, err := OpenSelectDBSession(context.Background(), wrongName, password); err == nil {
			_ = s.Close()
			t.Fatal("SelectDB accepted a mismatched TLS server name")
		}
	}
	other := p
	other.Kind, other.SelectDBOptions = connections.Doris, connections.SelectDBOptions{}
	if s, err := OpenSession(context.Background(), other, password); err == nil {
		_ = s.Close()
		t.Fatal("SelectDB was accepted as native Doris 4.1.3")
	}
}

func TestSelectDBNativeAggregateStateBytesFixture(t *testing.T) {
	p, password := selectDBFixtureProfile(t)
	ctx := context.Background()
	raw, err := OpenSelectDBSession(ctx, p, password)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	l, err := s.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	o := s.Namespace(p.Database).Object("TABLE", "state_"+strconv.FormatInt(time.Now().UnixNano(), 36))
	for _, q := range []string{
		"CREATE TABLE " + qualified(o) + "(id BIGINT,b BITMAP BITMAP_UNION,h HLL HLL_UNION) AGGREGATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES ('replication_num'='1')",
		"INSERT INTO " + qualified(o) + " VALUES(1,TO_BITMAP(123),HLL_HASH('value'))",
	} {
		if _, err = l.Execute(ctx, p.Database, q); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { _, _ = l.Execute(ctx, p.Database, "DROP TABLE "+qualified(o)) }()
	page, err := s.DataReads().AdvancedLoader(ctx, p.ID, p.Database, o.Name, contract.DataOptions{Object: o, Limit: 2})
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("SelectDB native aggregate-state read failed: %v", err)
	}
	for _, i := range []int{1, 2} {
		c := page.Rows[0][i]
		if c.Null || c.Value.Kind != contract.Binary || len(c.Value.Bytes) == 0 {
			t.Fatal("SelectDB aggregate state became NULL or a text value")
		}
	}
	var values []contract.DataValue
	err = s.DataExports().Exporter(ctx, p.ID, p.Database, o.Name, contract.DataOptions{Object: o}, nil, func(v []contract.DataValue) error { values = v; return nil })
	if err != nil || len(values) != 3 || !bytes.Equal(values[1].Bytes, page.Rows[0][1].Value.Bytes) || !bytes.Equal(values[2].Bytes, page.Rows[0][2].Value.Bytes) {
		t.Fatal("SelectDB aggregate-state export changed its native bytes")
	}
	r, err := l.Execute(ctx, p.Database, "SELECT b,h,BITMAP_COUNT(b),HLL_CARDINALITY(h) FROM "+qualified(o))
	if err != nil || r.Sets[0].Rows[0][0].Kind != contract.Binary || r.Sets[0].Rows[0][2].Text != "1" {
		t.Fatal("SelectDB original SQL result lost its native state")
	}
}
