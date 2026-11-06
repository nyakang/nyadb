package mysql

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func tidbFixture(t *testing.T) (*SessionAdapter, context.Context, connections.Profile) {
	t.Helper()
	if os.Getenv("NYADB_TIDB_IT") != "1" {
		t.Skip("requires an explicitly enabled disposable TiDB/PD/TiKV fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_TIDB_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit loopback fixture port required")
	}
	p, _ := connections.Defaults(connections.TiDB)
	p.ID, p.Name, p.Host, p.Port, p.Username, p.Database = 703, "TiDB fixture", "127.0.0.1", uint16(port), "nyadb_tidb", "nyadb_tidb_it"
	p.TLSMode, p.MySQLAdvanced.SSL.Enabled = "verify-full", true
	p.MySQLAdvanced.SSL.CACertificate = os.Getenv("NYADB_TIDB_CA_FILE")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	x, err := OpenSession(ctx, p, os.Getenv("NYADB_TIDB_PASSWORD"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := x.(*SessionAdapter)
	t.Cleanup(func() { s.Close() })
	return s, ctx, p
}

func TestTiDBFixtureCoreMetadataAndTools(t *testing.T) {
	s, ctx, p := tidbFixture(t)
	l, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	execute := func(sql string) contract.SQLResult {
		t.Helper()
		r, e := l.Execute(ctx, p.Database, sql)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	prefix := fmt.Sprintf("core_%d", time.Now().UnixNano())
	table, view, seq := prefix, prefix+"_view", prefix+"_seq"
	execute("CREATE TABLE `" + table + "` (id BIGINT PRIMARY KEY, note VARCHAR(80), amount DECIMAL(40,20), payload BLOB, detail LONGTEXT, document JSON, doubled DECIMAL(40,20) GENERATED ALWAYS AS (amount*2) STORED) ENGINE=InnoDB")
	execute("INSERT INTO `" + table + "` (id,note,amount,payload,detail,document) VALUES (1,'alpha',9007199254740993.12345678901234567890,X'00ff',REPEAT('长',2000),JSON_OBJECT('valid',true)),(2,NULL,2,NULL,'',NULL),(3,'gamma',3,X'','NULL',JSON_OBJECT('empty',''))")
	execute("CREATE VIEW `" + view + "` AS SELECT id,note FROM `" + table + "`")
	execute("CREATE SEQUENCE `" + seq + "` START WITH 9223372036854775800 INCREMENT BY -2 MINVALUE 1 MAXVALUE 9223372036854775806 CACHE 3 NOCYCLE")
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = l.Execute(cleanup, p.Database, "DROP VIEW IF EXISTS `"+view+"`")
		_, _ = l.Execute(cleanup, p.Database, "DROP TABLE IF EXISTS `"+table+"`")
		_, _ = l.Execute(cleanup, p.Database, "DROP SEQUENCE IF EXISTS `"+seq+"`")
	})
	t.Run("server and TLS", func(t *testing.T) {
		t.Log(execute("SELECT VERSION()").Rows)
		if r := execute("SHOW SESSION STATUS LIKE 'Ssl_cipher'"); len(r.Rows) != 1 || r.Rows[0][1] == "" {
			t.Fatal("SQL lease did not verify TLS")
		}
	})
	t.Run("qualified metadata", func(t *testing.T) {
		objects, e := s.Browse(ctx, s.Namespace(p.Database))
		if e != nil {
			t.Fatal(e)
		}
		found := map[string]bool{}
		for _, o := range objects {
			if o.Ref.ProfileID != p.ID || o.Ref.Catalog != p.Database || o.Ref.Schema != "" {
				t.Fatal("metadata lost qualified identity")
			}
			found[o.Name] = true
			if o.Name == seq && (o.Ref.Kind != "sequence" || o.Sequence == nil || o.Sequence.Start != 9223372036854775800 || o.Sequence.Increment != -2 || o.Definition == "") {
				t.Fatal("sequence lost exact parameters or complete definition")
			}
		}
		if !found[table] || !found[view] || !found[seq] {
			t.Fatal("table, view or sequence missing")
		}
	})
	t.Run("table design snapshot", func(t *testing.T) {
		snap, e := s.manager.TableDesign(ctx, p.ID, p.Database, table)
		if e != nil {
			t.Fatal(e)
		}
		if snap.Engine != "tidb" || snap.TriggersUnknown {
			t.Fatal("incorrect product/dependency identity")
		}
		t.Log(snap.DDL)
	})
	t.Run("grid", func(t *testing.T) {
		page, e := s.DataRuntime().AdvancedLoader(ctx, p.ID, p.Database, table, contract.DataOptions{Limit: 2})
		if e != nil {
			t.Fatal(e)
		}
		if len(page.Rows) != 2 || page.Rows[0][2].Text != "9007199254740993.12345678901234567890" {
			t.Fatal("grid lost exact decimal")
		}
		if page.RowSnapshots[0] == nil || page.RowRefs[0].Object.ProfileID != p.ID {
			t.Fatal("grid lost original row identity")
		}
	})
	t.Run("TiDB execution plan", func(t *testing.T) {
		sql, e := queryplan.Build("tidb", "SELECT note FROM `"+table+"` WHERE id=1")
		if e != nil {
			t.Fatal(e)
		}
		result := execute(sql)
		plan, e := queryplan.Parse("tidb", result)
		if e != nil || len(plan.Nodes) == 0 {
			t.Fatal("TiDB plan", e)
		}
	})
	t.Run("accounts", func(t *testing.T) {
		accounts, e := s.manager.Accounts(ctx, p.ID)
		if e != nil || len(accounts) == 0 {
			t.Fatal("TiDB accounts", e)
		}
	})
}
