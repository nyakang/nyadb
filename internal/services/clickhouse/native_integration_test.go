package clickhouse_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func fixtureProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_CLICKHOUSE_IT") != "1" {
		t.Skip("requires disposable ClickHouse and NYADB_CLICKHOUSE_IT=1")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_CLICKHOUSE_PORT"), 10, 16)
	if err != nil || port == 0 || os.Getenv("NYADB_CLICKHOUSE_PASSWORD") == "" {
		t.Fatal("missing isolated native fixture settings")
	}
	p, _ := connections.Defaults(connections.ClickHouse)
	p.ID, p.Name, p.Host, p.Port, p.Username = 41, "native fixture", "127.0.0.1", uint16(port), os.Getenv("NYADB_CLICKHOUSE_USER")
	return p, os.Getenv("NYADB_CLICKHOUSE_PASSWORD")
}
func TestClickHouseNativeFixture(t *testing.T) {
	p, password := fixtureProfile(t)
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := m.ConnectSession(ctx, p, password)
	if err != nil {
		t.Fatal(err)
	}
	provider, _ := contract.Require[contract.QueryProvider](session)
	l, err := provider.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	database := "nyadb_native_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run := func(sql string) contract.SQLResult {
		t.Helper()
		r, e := l.Execute(ctx, "default", sql)
		if e != nil {
			t.Fatalf("fixture statement failed: %v", e)
		}
		return r
	}
	run("CREATE DATABASE `" + database + "`")
	defer func() {
		cleanup, _ := provider.ReserveQuery(90)
		if cleanup != nil {
			defer cleanup.Release()
			_, _ = cleanup.Execute(context.Background(), "default", "DROP DATABASE IF EXISTS `"+database+"`")
		}
	}()
	qdb := "`" + database + "`."
	run("CREATE TABLE " + qdb + "`full values` (id UInt64, amount Decimal(38,20), nullable Nullable(String), text String, raw String, wide Int256, list Array(UInt64), byte_list Array(UInt8), calculated UInt64 ALIAS id+1) ENGINE=MergeTree ORDER BY id COMMENT 'native fixture'")
	run("INSERT INTO " + qdb + "`full values` (id,amount,nullable,text,raw,wide,list,byte_list) VALUES (9007199254740993,'123456789012345678.12345678901234567890',NULL,'NULL',unhex('ff0001'),170141183460469231731687303715884105727,[18446744073709551615],[0,255]),(1,'0.00000000000000000001','','','',-170141183460469231731687303715884105727,[],[])")
	run("CREATE VIEW " + qdb + "`values view` AS SELECT id,text FROM " + qdb + "`full values`")

	t.Run("namespaces metadata and exact native values", func(t *testing.T) {
		meta, err := m.Metadata(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		names, err := meta.Namespaces(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, n := range names {
			if n.Ref.Catalog == database {
				found = true
				if n.Ref.Schema != "" || n.Ref.ProfileID != p.ID {
					t.Fatal("wrong qualified namespace")
				}
			}
		}
		if !found {
			t.Fatal("database absent")
		}
		n := m.Namespace(p.ID, database)
		objects, err := meta.Browse(ctx, n)
		if err != nil || len(objects) != 2 {
			t.Fatalf("browse: %d %v", len(objects), err)
		}
		o := n.Object("TABLE", "full values")
		structure, err := meta.TableMetadata(ctx, o)
		if err != nil || structure.Editable || structure.Engine != "MergeTree" || len(structure.Columns) != 9 {
			t.Fatalf("structure: %+v %v", structure, err)
		}
		if !structure.Columns[8].Generated {
			t.Fatal("alias not marked generated")
		}
		data := m.Data(p.ID)
		if data.Saver != nil || data.Transactions.Begin != nil || data.Capabilities.EditableTables || data.Capabilities.Keyset {
			t.Fatal("sorting key allowed relational writes")
		}
		page, err := data.AdvancedLoader(ctx, p.ID, database, "full values", contract.DataOptions{Object: o, Limit: 1, SortColumn: "id", SortDescending: true})
		if err != nil || len(page.Rows) != 1 || !page.HasMore || page.StableOrder || page.KeysetAvailable || page.Editable == nil || *page.Editable {
			t.Fatalf("page: %+v %v", page, err)
		}
		want := []string{"9007199254740993", "123456789012345678.1234567890123456789", "NULL", "NULL", "0xff0001", "170141183460469231731687303715884105727", "[18446744073709551615]", "[0,255]"}
		for i, v := range want {
			if page.Rows[0][i].Value == nil || page.Rows[0][i].Value.Display() != v {
				t.Fatalf("typed field %d changed: %+v", i, page.Rows[0][i])
			}
		}
		if page.Rows[0][2].Value.Kind != contract.Null || page.Rows[0][3].Value.Kind != contract.Text {
			t.Fatal("NULL text conflated")
		}
		count, err := data.Count(ctx, p.ID, database, "full values")
		if err != nil || count != 2 {
			t.Fatalf("count %d %v", count, err)
		}
		page, err = data.AdvancedLoader(ctx, p.ID, database, "full values", contract.DataOptions{Object: o, Limit: 5, Filters: []contract.DataFilterRule{{Column: "text", Mode: "equal", Value: "NULL"}}})
		if err != nil || len(page.Rows) != 1 {
			t.Fatalf("filter %v", err)
		}
		var output bytes.Buffer
		if err = data.JSONL(ctx, o, &output); err != nil || !strings.Contains(output.String(), "18446744073709551615") || strings.Contains(output.String(), password) {
			t.Fatalf("exact export failed %v", err)
		}
		if _, err = meta.Browse(ctx, contract.NamespaceRef{ProfileID: p.ID + 1, Catalog: database}); err == nil {
			t.Fatal("cross-profile metadata accepted")
		}
	})
	t.Run("private physical leases namespace streaming and preview limits", func(t *testing.T) {
		first, _ := provider.ReserveQuery(2)
		defer first.Release()
		second, _ := provider.ReserveQuery(3)
		defer second.Release()
		if err = first.Preconnect(ctx); err != nil {
			t.Fatal(err)
		}
		rComment, eComment := first.Execute(ctx, database, "--heading\nSELECT 1 --comment; DROP TABLE `full values`\n; SELECT $body$select; FROM literal$body$")
		if eComment != nil || len(rComment.Sets) != 2 || rComment.Rows[0][0] != "1" || rComment.Sets[1].Rows[0][0].Text != "select; FROM literal" {
			t.Fatalf("native protected SQL changed: %v", eComment)
		}
		if _, err = first.Execute(ctx, database, "SET max_threads=2"); err != nil {
			t.Fatal(err)
		}
		r, err := first.Execute(ctx, database, "SELECT currentDatabase(),getSetting('max_threads')")
		if err != nil || r.Rows[0][0] != database || r.Rows[0][1] != "2" {
			t.Fatalf("private session lost: %+v %v", r, err)
		}
		if _, err = second.Execute(ctx, database, "SET max_threads=3"); err != nil {
			t.Fatal(err)
		}
		r, err = first.Execute(ctx, database, "SELECT getSetting('max_threads')")
		if err != nil || r.Rows[0][0] != "2" {
			t.Fatal("another document changed private settings")
		}
		rows := 0
		r, err = first.ExecuteStream(ctx, database, "SELECT number FROM numbers(2500)", func(index int, columns []contract.Column, row []contract.Cell) error {
			if index != 0 || len(columns) != 1 || len(row) != 1 {
				t.Fatal("stream metadata")
			}
			rows++
			return nil
		})
		if err != nil || rows != 2500 || len(r.Sets[0].Rows) != 2000 || !r.Truncated {
			t.Fatalf("stream bounded preview: %d %v", rows, err)
		}
		r, err = first.Execute(ctx, database, "SELECT repeat('x',5000)")
		if err != nil || !r.Sets[0].Rows[0][0].Truncated || len(r.Sets[0].Rows[0][0].Text) != 4096 {
			t.Fatal("cell preview unbounded")
		}
	})
	t.Run("cancellation explicit recovery and generation revocation", func(t *testing.T) {
		old, _ := provider.ReserveQuery(4)
		defer old.Release()
		if err = old.Preconnect(ctx); err != nil {
			t.Fatal(err)
		}
		short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		_, err = old.Execute(short, "default", "SELECT sleep(3)")
		stop()
		if err == nil {
			t.Fatal("sleep was not cancelled")
		}
		if _, err = old.Execute(ctx, "default", "SELECT 1"); !errors.Is(err, contract.ErrSQLSessionLost) {
			t.Fatal("cancelled lease implicitly reconnected")
		}
		replacement, e := old.Replace()
		if e != nil {
			t.Fatal(e)
		}
		defer replacement.Release()
		if _, e = replacement.Execute(ctx, "default", "SELECT 1"); e != nil {
			t.Fatal(e)
		}
		boundData := m.Data(p.ID)
		generation := session.Generation()
		if err = m.Connect(ctx, p, password); err != nil {
			t.Fatal(err)
		}
		if m.Generation(p.ID) <= generation {
			t.Fatal("generation did not advance")
		}
		if _, err = replacement.Execute(ctx, "default", "SELECT 1"); !errors.Is(err, contract.ErrRevoked) {
			t.Fatal("old lease reached new session")
		}
		if _, err = boundData.Count(ctx, p.ID, database, "full values"); !errors.Is(err, contract.ErrRevoked) {
			t.Fatal("old data callback reached new session")
		}
	})
}
