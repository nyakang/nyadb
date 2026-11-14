package dbruntime_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// tools/verify_mysql_products_fixture.py owns these disposable loopback servers.
// TDSQL and PolarDB use a MySQL protocol fixture; GreatSQL and Dolt use the
// actual products. The protocol fixture does not verify managed cloud features.
func TestMySQLProductsRuntimeDisposableFixture(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_PRODUCTS_IT") != "1" {
		t.Skip("requires disposable MySQL-compatible product fixtures")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_PRODUCTS_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	for _, name := range strings.Split(os.Getenv("NYADB_MYSQL_PRODUCTS_KINDS"), ",") {
		kind := connections.Kind(name)
		if kind != connections.TDSQL && kind != connections.PolarDB && kind != connections.GreatSQL && kind != connections.Dolt {
			t.Fatal("fixture product must be explicit")
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			m := dbruntime.New()
			defer m.Close()
			dbruntime.RegisterBuiltins(m, nil)
			p, _ := connections.Defaults(kind)
			p.ID, p.Name, p.Host, p.Port, p.Database = 801, name+" fixture", "unused.invalid", 1, "nyadb_products_it"
			p.Variants = []connections.ConnectionVariant{{Name: "Original", Host: "127.0.0.1", Port: uint16(port), Database: p.Database, Username: "root", MySQLAdvanced: connections.DefaultMySQLAdvanced()}}
			p.ActiveVariant = "Original"
			password := os.Getenv("NYADB_MYSQL_PRODUCTS_PASSWORD")
			if err := m.Connect(ctx, p, password); err != nil {
				t.Fatal(err)
			}
			session, err := m.Session(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			generation := session.Generation()
			query, err := contract.Require[contract.QueryProvider](session)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := query.ReserveQuery(1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			execute := func(sql string) contract.SQLResult {
				t.Helper()
				result, err := lease.Execute(ctx, p.Database, sql)
				if err != nil {
					t.Fatal("SQL execution", err)
				}
				return result
			}
			execute("DROP TABLE IF EXISTS records")
			execute("CREATE TABLE records (id BIGINT PRIMARY KEY, note VARCHAR(80), amount DECIMAL(40,20), payload BLOB)")
			execute("INSERT INTO records VALUES (1,'alpha',9007199254740993.12345678901234567890,X'00ff'),(2,NULL,2,NULL)")
			version := execute("SELECT VERSION()")
			t.Log("product", name, "server", version.Rows[0][0])
			if kind == connections.Dolt {
				for _, projection := range []string{"HEX(payload)", "HEX(UNHEX(SUBSTRING(HEX(payload),1,2050)))", "SHA2(HEX(payload),256)", "SHA2(HEX(CAST(payload AS BINARY)),256)"} {
					if _, err := lease.Execute(ctx, p.Database, "SELECT "+projection+" FROM records WHERE id=1"); err != nil {
						t.Fatal("Dolt binary projection", projection, err)
					}
				}
			}
			metadata, err := m.Metadata(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			namespace := m.Namespace(p.ID, p.Database)
			if namespace.ProfileID != p.ID || namespace.Catalog != p.Database || namespace.Schema != "" {
				t.Fatal("namespace changed original identity")
			}
			objects, err := metadata.Browse(ctx, namespace)
			if err != nil || len(objects) == 0 {
				t.Fatal("metadata browse", err)
			}
			data := m.Data(p.ID)
			page, err := data.AdvancedLoader(ctx, p.ID, p.Database, "records", contract.DataOptions{Limit: 1, UseKeyset: true})
			if err != nil {
				t.Fatal("typed page", err)
			}
			if !page.KeysetAvailable || page.NextCursor == nil || page.Rows[0][2].Value.Kind != contract.Decimal || page.Rows[0][2].Text != "9007199254740993.12345678901234567890" || page.Rows[0][3].Value.Kind != contract.Binary {
				t.Fatal("page lost cursor or typed values")
			}
			next, err := data.AdvancedLoader(ctx, p.ID, p.Database, "records", contract.DataOptions{Limit: 1, UseKeyset: true, After: page.NextCursor, Offset: 1})
			if err != nil || next.Rows[0][0].Text != "2" || !next.Rows[0][1].Null {
				t.Fatal("cursor boundary or NULL", err)
			}
			original := page.RowSnapshots[0]
			edit := contract.DataEdit{Kind: "update", Original: original, Values: map[string]contract.DataValue{"note": {Text: "saved"}, "payload": {Binary: true, Bytes: []byte{1, 0, 255}}}}
			result, err := data.Saver(ctx, p.ID, p.Database, "records", []contract.DataEdit{edit})
			if err != nil || !result.CommitConfirmed || result.Applied != 1 {
				t.Fatal("typed edit", err)
			}
			if _, err := data.Saver(ctx, p.ID, p.Database, "records", []contract.DataEdit{edit}); !errors.Is(err, mysqlsvc.ErrMySQLDataConflict) {
				t.Fatal("stale fingerprint accepted", err)
			}
			// Arbitrary bytes must remain complete across bounded previews,
			// explicit chunk/full reads and streaming export (including NULL).
			payload := bytes.Repeat([]byte{0, 255}, 700)
			execute("UPDATE records SET payload=X'" + hex.EncodeToString(payload) + "' WHERE id=1")
			large, err := data.AdvancedLoader(ctx, p.ID, p.Database, "records", contract.DataOptions{Limit: 2})
			if err != nil || !large.Rows[0][3].Truncated || large.Rows[0][3].Value == nil {
				t.Fatal("bounded binary preview", err)
			}
			preview := large.Rows[0][3].Value.Bytes
			if len(preview) == 0 || len(preview) > 1024 || !bytes.Equal(preview, payload[:len(preview)]) {
				t.Fatal("binary preview changed bytes or exceeded its bound")
			}
			ref := large.RowSnapshots[0].Ref
			chunk, err := data.Chunk(ctx, p.ID, p.Database, "records", "payload", ref, 1023, 1024)
			if err != nil || !chunk.Binary || chunk.Text != "0x"+hex.EncodeToString(payload[1023:]) || chunk.TotalUnits != 1400 || chunk.NextOffset != 1400 || chunk.HasMore {
				t.Fatal("explicit binary chunk", chunk, err)
			}
			full, err := data.RowReader(ctx, p.ID, p.Database, "records", ref)
			if err != nil || !full.Values["payload"].Binary || !bytes.Equal(full.Values["payload"].Bytes, payload) {
				t.Fatal("complete binary row", err)
			}
			var exported [][]contract.DataValue
			err = data.Exporter(ctx, p.ID, p.Database, "records", contract.DataOptions{SortColumn: "id"}, []string{"id", "note", "amount", "payload"}, func(values []contract.DataValue) error {
				exported = append(exported, values)
				return nil
			})
			if err != nil || len(exported) != 2 || !bytes.Equal(exported[0][3].Bytes, payload) || exported[0][2].Text != "9007199254740993.12345678901234567890" || !exported[1][1].Null || !exported[1][3].Null {
				t.Fatal("streaming complete typed export", err)
			}
			for _, commit := range []bool{false, true} {
				tx, err := data.Transactions.Begin(ctx, p.ID, p.Database, "records")
				if err != nil {
					t.Fatal("begin data transaction", err)
				}
				defer tx.Close()
				inside, err := data.Transactions.Page(ctx, tx, p.ID, p.Database, "records", contract.DataOptions{Limit: 1})
				if err != nil {
					t.Fatal(err)
				}
				_, err = data.Transactions.Save(ctx, tx, p.ID, p.Database, "records", []contract.DataEdit{{Kind: "update", Original: inside.RowSnapshots[0], Values: map[string]contract.DataValue{"note": {Text: "transaction"}}}})
				if err != nil {
					t.Fatal("transaction edit", err)
				}
				outside := execute("SELECT note FROM records WHERE id=1")
				if outside.Rows[0][0] != "saved" {
					t.Fatal("uncommitted edit escaped its original transaction")
				}
				if commit {
					err = data.Transactions.Commit(ctx, tx)
				} else {
					err = data.Transactions.Rollback(ctx, tx)
				}
				if err != nil {
					t.Fatal("transaction acknowledgement", err)
				}
			}
			if execute("SELECT note FROM records WHERE id=1").Rows[0][0] != "transaction" {
				t.Fatal("committed edit was lost")
			}
			admin := m.Administration(p.ID)
			snapshot, err := admin.TableDesign(ctx, p.ID, p.Database, "records")
			if err != nil || snapshot.Engine != name {
				t.Fatal("table design changed product identity", err)
			}
			table, err := mysqlddl.Parse(snapshot.Database, snapshot.Table, snapshot.DDL)
			if err != nil {
				t.Fatal(err)
			}
			column, err := mysqlddl.ParseClause("`extra` INT NULL")
			if err != nil {
				t.Fatal(err)
			}
			table.Clauses = append(table.Clauses, column)
			ddl, err := mysqlddl.Build(snapshot, mysqlddl.Draft{Table: table, Triggers: snapshot.Triggers})
			if err != nil {
				t.Fatal("table design plan", err)
			}
			outcome := admin.ApplyTableDesign(ctx, p.ID, ddl)
			if outcome.Error != nil || !outcome.Verified {
				t.Fatal("table design execution", outcome.Error)
			}
			planSQL, err := queryplan.Build(name, "SELECT id FROM records WHERE id=1")
			if err != nil {
				t.Fatal("plan generation", err)
			}
			planResult := execute(planSQL)
			if kind == connections.Dolt {
				for _, row := range planResult.Rows {
					t.Logf("Dolt plan line: %q", row[0])
				}
			}
			plan, err := queryplan.Parse(name, planResult)
			if err != nil || len(plan.Nodes) == 0 || plan.Engine != name {
				t.Fatal("product execution plan", err)
			}
			if kind == connections.Dolt {
				for _, sql := range []string{
					"SELECT a.id,b.note FROM records a JOIN records b ON a.id=b.id WHERE a.id=1 ORDER BY b.note",
					"SELECT COUNT(*),note FROM records GROUP BY note ORDER BY note",
				} {
					request, err := queryplan.Build(name, sql)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := queryplan.Parse(name, execute(request)); err != nil {
						t.Fatal("nested Dolt plan", err)
					}
				}
			}
			if err := m.Connect(ctx, p, password); err != nil {
				t.Fatal(err)
			}
			replacement, err := m.Session(p.ID)
			if err != nil || replacement.Generation() <= generation {
				t.Fatal("generation did not advance", err)
			}
			if _, err := lease.Execute(ctx, p.Database, "INSERT INTO records VALUES(3,'late',3,NULL)"); err == nil {
				t.Fatal("original SQL lease executed on replacement session")
			}
			if _, err := data.AdvancedLoader(ctx, p.ID, p.Database, "records", contract.DataOptions{Limit: 1}); err == nil {
				t.Fatal("original data runtime executed on replacement session")
			}
		})
	}
}
