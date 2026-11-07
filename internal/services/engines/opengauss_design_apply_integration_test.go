package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	gausspq "gitcode.com/opengauss/openGauss-connector-go-pq"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func gaussDesignApplyRequest(t *testing.T, s *Session, target contract.SchemaDesignTarget) contract.SchemaDesignRequest {
	t.Helper()
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	desired.Clauses = append(desired.Clauses,
		schemaddl.Clause{ID: "new:constant", Kind: "column", Name: "constant", SQL: "numeric(40,20) DEFAULT 9007199254740993.12345678901234567890"},
		schemaddl.Clause{ID: "new:serial", Kind: "column", Name: "new_id", SQL: "bigserial"},
		schemaddl.Clause{ID: "new:generated", Kind: "column", Name: "new_generated", SQL: "numeric(40,20) GENERATED ALWAYS AS(amount*3+new_id) STORED"},
	)
	return contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true}
}

func TestOpenGaussDesignApplyCommitPreservesOriginalAndRejectsReplayFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "UPDATE "+qualified(target.Object)+" SET note=$1", strings.Repeat("完整值", 1001)); err != nil {
		t.Fatal(err)
	}
	r := gaussDesignApplyRequest(t, s, target)
	before := schemaddl.Signature(r)
	out := s.openGaussDesignApply(ctx, r)
	if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil || out.Rows != 1 || out.Actual.Target != target || out.Actual.Schema.OpenGauss.RelationOID != r.Original.Schema.OpenGauss.RelationOID {
		t.Fatal("native verified transaction", out)
	}
	if before != schemaddl.Signature(r) {
		t.Fatal("executor changed the captured request")
	}
	var note, constant, generated string
	var id, newID int64
	if err := db.QueryRowContext(ctx, "SELECT note,constant::text,new_generated::text,id,new_id FROM "+qualified(target.Object)).Scan(&note, &constant, &generated, &id, &newID); err != nil || note != strings.Repeat("完整值", 1001) || constant != "9007199254740993.12345678901234567890" || generated != "27021597764222980.37037036703703703670" || id != 1 || newID != 1 {
		t.Fatal("complete committed values", len(note), constant, generated, id, newID, err)
	}
	if next := s.openGaussDesignApply(ctx, r); next.State != "rolled_back" || !errors.Is(next.Error, schemaddl.ErrConflict) || next.Verified || next.Actual != nil {
		t.Fatal("captured design confirmation was replayed", next)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(target.Object)).Scan(&count); err != nil || count != 1 {
		t.Fatal("replay changed rows", count, err)
	}
	if ops := s.SchemaDesign(); ops.Apply != nil || ops.CanApply != nil && ops.CanApply(target) {
		t.Fatal("private executor was publicly enabled before full verification")
	}
}

func TestOpenGaussDesignApplyEmptyCreateAndCompleteReplacementFixture(t *testing.T) {
	for _, operation := range []string{"create", "replace_all"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			if operation == "create" {
				target.Object.Name = "created"
			}
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			desired.Clauses = []schemaddl.Clause{
				{ID: "new:serial", Kind: "column", Name: "id", SQL: "bigserial"},
				{ID: "new:generated", Kind: "column", Name: "next_id", SQL: "bigint GENERATED ALWAYS AS(id+1) STORED"},
			}
			desired.Definitions = nil
			out := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired, AllowColumnDrop: true})
			if out.State != "committed" || out.Error != nil || !out.Verified || out.Actual == nil || out.Actual.Target != target {
				t.Fatal("native create/replacement", out)
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+qualified(target.Object)).Scan(&count); err != nil || count != map[string]int{"create": 0, "replace_all": 1}[operation] {
				t.Fatal("native row cardinality", count, err)
			}
			if operation == "replace_all" {
				var id, next int
				if err := db.QueryRowContext(ctx, "SELECT id,next_id FROM "+qualified(target.Object)).Scan(&id, &next); err != nil || id != 1 || next != 2 {
					t.Fatal("replacement values", id, next, err)
				}
			}
		})
	}
}

func TestOpenGaussDesignApplyLossyValuesAndVolatileDefaultsRollbackFixture(t *testing.T) {
	for _, change := range []string{"varchar", "numeric", "volatile"} {
		t.Run(change, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			if change == "numeric" {
				// Native openGauss rejects changing an input type while a stored
				// generated column depends on it. Isolate the lossy conversion
				// itself; generated-input rejection has its own native boundary.
				if _, err := db.ExecContext(ctx, "ALTER TABLE "+qualified(target.Object)+" DROP COLUMN twice RESTRICT"); err != nil {
					t.Fatal(err)
				}
			}
			original := gaussDesignOriginal(t, s, target)
			desired := original.Schema.Table.Clone()
			for i, col := range desired.Clauses {
				if change == "varchar" && col.Name == "note" {
					desired.Clauses[i].SQL = "varchar(1)"
				}
				if change == "numeric" && col.Name == "amount" {
					desired.Clauses[i].SQL = "numeric(40,2)"
				}
			}
			if change == "volatile" {
				desired.Clauses = append(desired.Clauses, schemaddl.Clause{ID: "new:random", Kind: "column", Name: "random_value", SQL: "double precision DEFAULT random()"})
			}
			out := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired})
			if out.State != "rolled_back" || out.Error == nil || out.Verified || out.Actual != nil {
				t.Fatal("lossy/nonrepeatable values committed", change, out)
			}
			if change != "volatile" && !errors.Is(out.Error, schemaddl.ErrDataChanged) || change == "volatile" && !errors.Is(out.Error, errOpenGaussValueRepeatability) {
				t.Fatal("value proof did not decide rollback", change, out)
			}
			var note, amount string
			if err := db.QueryRowContext(ctx, "SELECT note,amount::text FROM "+qualified(target.Object)).Scan(&note, &amount); err != nil || note != "原始" || amount != "9007199254740993.12345678901234567890" {
				t.Fatal("rollback did not restore original values", note, amount, err)
			}
		})
	}
}

func TestOpenGaussDesignApplySerialRejectsUserUpdateTriggerBeforeEffectsFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ns := quote(target.Object.Schema)
	if _, err := db.ExecContext(ctx, "CREATE SEQUENCE "+ns+".effects; CREATE FUNCTION "+ns+".on_update() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM nextval('"+target.Object.Schema+".effects'); RETURN NEW; END$$; CREATE TRIGGER effect BEFORE UPDATE ON "+qualified(target.Object)+" FOR EACH ROW EXECUTE PROCEDURE "+ns+".on_update()"); err != nil {
		t.Fatal(err)
	}
	out := s.openGaussDesignApply(ctx, gaussDesignApplyRequest(t, s, target))
	if out.State != "not_executed" || out.Error == nil || out.Verified || out.Actual != nil {
		t.Fatal("serial backfill authorized a user UPDATE trigger", out)
	}
	var called bool
	if err := db.QueryRowContext(ctx, "SELECT is_called FROM "+ns+".effects").Scan(&called); err != nil || called {
		t.Fatal("serial planning executed a user trigger", called, err)
	}
}

func TestOpenGaussDesignApplyRejectsRetainedGeneratedInputTypeChangeFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	for i, col := range desired.Clauses {
		if col.Name == "amount" {
			desired.Clauses[i].SQL = "numeric(40,2)"
		}
	}
	out := s.openGaussDesignApply(ctx, contract.SchemaDesignRequest{Original: original, Desired: desired})
	if out.State != "not_executed" || out.Error == nil || !strings.Contains(out.Error.Error(), "dependent stored generated") || out.Verified || out.Actual != nil {
		t.Fatal("native generated-input restriction reached mutating DDL", out)
	}
	var amount string
	if err := db.QueryRowContext(ctx, "SELECT amount::text FROM "+qualified(target.Object)).Scan(&amount); err != nil || amount != "9007199254740993.12345678901234567890" {
		t.Fatal("rejected type change altered values", amount, err)
	}
}

func TestOpenGaussDesignApplyNewRuleActionsRemainClosedFixture(t *testing.T) {
	for _, scope := range []string{"rule"} {
		t.Run(scope, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			r := gaussDesignApplyRequest(t, s, target)
			if scope == "rule" {
				r.Desired.Definitions = append(r.Desired.Definitions, schemaddl.Definition{Name: "new_rule", Kind: "rule", SQL: "CREATE RULE new_rule AS ON UPDATE TO " + qualified(target.Object) + " DO ALSO NOTHING"})
			}
			out := s.openGaussDesignApply(ctx, r)
			if out.State != "not_executed" || out.Error == nil || out.Verified || out.Actual != nil {
				t.Fatal("unverified native scope gained execution permission", scope, out)
			}
			var exists bool
			if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=$1::oid AND attname='new_id' AND NOT attisdropped)", r.Original.Schema.OpenGauss.RelationOID).Scan(&exists); err != nil || exists {
				t.Fatal("rejected scope changed the original table", scope, exists, err)
			}
		})
	}
}

func TestOpenGaussDesignApplyWaitRejectsCommittedChangeAndReplacementFixture(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprint(replace), func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			r := gaussDesignApplyRequest(t, s, target)
			locker := openGaussTableLocker(t, db, target)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			ch := make(chan contract.SchemaDesignOutcome, 1)
			go func() { ch <- s.openGaussDesignApply(ctx, r) }()
			openGaussViewWaiting(t, db, target.Object)
			q := "COMMENT ON TABLE " + qualified(target.Object) + " IS 'external change'"
			if replace {
				q = "DROP TABLE " + qualified(target.Object) + "; CREATE TABLE " + qualified(target.Object) + "(id bigint,note text); INSERT INTO " + qualified(target.Object) + " VALUES(2,'replacement')"
			}
			if _, err := locker.ExecContext(ctx, q+"; COMMIT"); err != nil {
				t.Fatal(err)
			}
			out := openGaussTableAwait(t, ch)
			if out.State != "rolled_back" || !errors.Is(out.Error, schemaddl.ErrConflict) || out.Verified || out.Actual != nil {
				t.Fatal("stale design changed an externally committed object", out)
			}
		})
	}
}

func TestOpenGaussDesignApplyCancellationAndRevocationAwaitAcknowledgementFixture(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			r := gaussDesignApplyRequest(t, s, target)
			locker := openGaussTableLocker(t, db, target)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch := make(chan contract.SchemaDesignOutcome, 1)
			go func() { ch <- s.openGaussDesignApply(ctx, r) }()
			openGaussViewWaiting(t, db, target.Object)
			if revoke {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			out := openGaussTableAwait(t, ch)
			if out.State != "rolled_back" && out.State != "uncertain" || out.Error == nil || out.Verified || out.Actual != nil || !revoke && !errors.Is(out.Error, context.Canceled) {
				t.Fatal("cancel/revocation left the design replayable", out)
			}
			if out.State == "uncertain" && !s.isClosed() {
				t.Fatal("uncertain original session remained usable")
			}
			if err := rollbackDesign(locker); err != nil {
				t.Fatal(err)
			}
			var columns int
			if err := db.QueryRow("SELECT count(*) FROM pg_attribute WHERE attrelid=$1::oid AND attnum>0 AND NOT attisdropped", r.Original.Schema.OpenGauss.RelationOID).Scan(&columns); err != nil || columns != 4 {
				t.Fatal("cancelled design changed original columns", columns, err)
			}
		})
	}
}

func TestOpenGaussDesignApplyLostCommitReceiptRetiresCapturedOriginalFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	r := gaussDesignApplyRequest(t, s, target)
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
	s.db = sql.OpenDB(pgLostCommitConnector{Connector: connector})
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	out := s.openGaussDesignApply(ctx, r)
	if out.State != "uncertain" || out.Verified || out.Actual != nil || !errors.Is(out.Error, io.ErrUnexpectedEOF) || !errors.Is(out.Error, ErrUncertain) || !s.isClosed() {
		t.Fatal("lost commit receipt permitted automatic replay", out)
	}
	var value int
	if err := db.QueryRowContext(ctx, "SELECT new_id FROM "+qualified(target.Object)).Scan(&value); err != nil || value != 1 {
		t.Fatal("fixture did not actually commit before losing its receipt", value, err)
	}
	if next := s.openGaussDesignApply(ctx, r); next.State != "not_executed" || !errors.Is(next.Error, contract.ErrRevoked) {
		t.Fatal("lost original replayed table design", next)
	}
}
