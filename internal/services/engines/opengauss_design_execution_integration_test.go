package engines

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	gausspq "gitcode.com/opengauss/openGauss-connector-go-pq"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignApplyNativeRenameAlterAndObjectChangesFixture(t *testing.T) {
	for _, operation := range []string{"rename_table", "rename_column", "indexed_type", "metadata", "objects", "drop_generated"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			switch operation {
			case "rename_table":
				desired.Name = `renamed"; other --`
			case "rename_column":
				for i, col := range desired.Clauses {
					if col.Kind == "column" && col.Name == "id" {
						desired.Clauses[i].Name = `next"id`
					}
				}
			case "indexed_type":
				for i, col := range desired.Clauses {
					if col.Kind == "column" && col.Name == "id" {
						f, err := schemaddl.ParseOpenGaussField(col.SQL)
						if err != nil {
							t.Fatal(err)
						}
						f.Type = "numeric(30,0)"
						desired.Clauses[i].SQL, err = f.SQL()
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			case "metadata":
				comment := "完整注释 ' 中文"
				desired.OpenGauss.Comment = &comment
				for i, col := range desired.Clauses {
					if col.Name == "note" {
						desired.Clauses[i].OpenGauss.Comment = &comment
						desired.Clauses[i].SQL = "text NOT NULL DEFAULT '以后'"
					}
				}
			case "objects":
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:check", Kind: "constraint", Name: "positive", SQL: "CONSTRAINT positive CHECK(amount>=0)"})
				desired.Definitions = append(desired.Definitions, schemaddl.Definition{Name: "by_note", Kind: "index", SQL: "CREATE INDEX by_note ON " + qualified(target.Object) + "(note)"})
			case "drop_generated":
				var retained []schemaddl.Clause
				for _, col := range desired.Clauses {
					if col.Kind != "column" || col.Name != "twice" {
						retained = append(retained, col)
					}
				}
				desired.Clauses = retained
			}
			r := contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true}
			out := s.openGaussDesignApply(ctx, r)
			resultTarget := target
			resultTarget.Object.Name = desired.Name
			if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil || out.Actual.Target != resultTarget || out.Rows != 1 || out.Actual.Schema.OpenGauss.RelationOID != original.Schema.OpenGauss.RelationOID {
				t.Fatal("native original identity did not survive verified Apply", operation, out)
			}
			var note, amount string
			if err := db.QueryRowContext(ctx, "SELECT note,amount::text FROM "+qualified(resultTarget.Object)).Scan(&note, &amount); err != nil || note != "原始" || amount != "9007199254740993.12345678901234567890" {
				t.Fatal("committed original values changed", note, amount, err)
			}
			var other int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+quote(target.Object.Schema)+".other").Scan(&other); err != nil || other != 0 {
				t.Fatal("quoted identity reached an unrelated table", other, err)
			}
		})
	}
}

// The gate preserves real driver execution and receipts. A second physical
// session may change its sequence, or the caller may cancel after the original
// ALTER has actually executed; no result or catalog row is synthesized.
type gaussDesignExecutionGateConnector struct {
	driver.Connector
	query string
	after func(context.Context) error
	once  *sync.Once
}

func (c gaussDesignExecutionGateConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return gaussDesignExecutionGateConnection{Conn: conn, query: c.query, after: c.after, once: c.once}, nil
}

type gaussDesignExecutionGateConnection struct {
	driver.Conn
	query string
	after func(context.Context) error
	once  *sync.Once
}

func (c gaussDesignExecutionGateConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && query == c.query {
		c.once.Do(func() { err = c.after(ctx) })
	}
	return result, err
}

func gaussDesignInstallExecutionGate(t *testing.T, s *Session, query string, after func(context.Context) error) {
	t.Helper()
	cfg, err := openGaussConfig(s.backupProfile, os.Getenv("NYADB_OPENGAUSS_PASSWORD"), nil)
	if err != nil {
		t.Fatal(err)
	}
	connector, err := gausspq.NewConnectorConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	originalDB := s.db
	t.Cleanup(func() { _ = originalDB.Close() })
	s.db = sql.OpenDB(gaussDesignExecutionGateConnector{Connector: connector, query: query, after: after, once: &sync.Once{}})
}

func TestOpenGaussDesignApplySequenceRaceAndExecutionCancellationRollbackFixture(t *testing.T) {
	for _, operation := range []string{"sequence", "cancel_after_alter"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			r := gaussDesignApplyRequest(t, s, target)
			plan, err := schemaddl.PlanOpenGauss(r.Original.Schema, r.Desired, target.Object.Schema, true)
			if err != nil || len(plan.SQL) == 0 {
				t.Fatal("native mutation plan", err)
			}
			fired := false
			gaussDesignInstallExecutionGate(t, s, plan.SQL[0], func(ctx context.Context) error {
				fired = true
				if operation == "cancel_after_alter" {
					cancel()
					return nil
				}
				_, err := db.ExecContext(ctx, "SELECT setval($1::regclass,77,true)", quote(target.Object.Schema)+".original_id_seq")
				return err
			})
			out := s.openGaussDesignApply(ctx, r)
			if !fired || out.State != "rolled_back" || out.Verified || out.Actual != nil || out.Error == nil {
				t.Fatal("execution race/cancellation did not roll back acknowledged ALTER", fired, out)
			}
			if operation == "cancel_after_alter" && !errors.Is(out.Error, context.Canceled) || operation == "sequence" && !strings.Contains(out.Error.Error(), "original native sequence changed") {
				t.Fatal("unexpected rollback cause", out)
			}
			verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer verifyCancel()
			fresh, err := s.openGaussDesignSnapshot(verifyCtx, target)
			if err != nil || fresh.Schema.Revision != r.Original.Schema.Revision {
				t.Fatal("rollback did not restore original native catalog", err)
			}
			var last int
			if err := db.QueryRowContext(verifyCtx, "SELECT last_value FROM "+quote(target.Object.Schema)+".original_id_seq").Scan(&last); err != nil || last != map[string]int{"sequence": 77, "cancel_after_alter": 1}[operation] {
				t.Fatal("rollback reset an independent sequence writer", last, err)
			}
		})
	}
}
