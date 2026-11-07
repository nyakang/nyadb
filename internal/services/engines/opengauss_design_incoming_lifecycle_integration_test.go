package engines

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignIncomingApplyCancellationSettlesChildLockAndRestoresNativeEffectsFixture(t *testing.T) {
	s, db, target := openGaussTableFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	applyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	childTarget := target
	childTarget.Object.Name = "other"
	parent, child := qualified(target.Object), qualified(childTarget.Object)
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+child+" ADD COLUMN parent_id bigint; ALTER TABLE "+child+" ADD CONSTRAINT incoming_fk FOREIGN KEY(parent_id) REFERENCES "+parent+"(id); INSERT INTO "+child+" VALUES(9,1); COMMENT ON CONSTRAINT incoming_fk ON "+child+" IS 'rollback retains this comment'"); err != nil {
		t.Fatal(err)
	}
	original := gaussDesignOriginal(t, s, target)
	desired := original.Schema.Table.Clone()
	for i, column := range desired.Clauses {
		if column.Kind != "column" || column.Name != "id" {
			continue
		}
		field, err := schemaddl.ParseOpenGaussField(column.SQL)
		if err != nil {
			t.Fatal(err)
		}
		field.Type = "integer"
		desired.Clauses[i].SQL, err = field.SQL()
		if err != nil {
			t.Fatal(err)
		}
	}
	request := contract.SchemaDesignRequest{Original: original, Desired: desired}
	plan, err := schemaddl.PlanOpenGauss(original.Schema, desired, target.Object.Schema, false)
	if err != nil || len(plan.SQL) != 1 {
		t.Fatal("native incoming cancellation type plan", plan.SQL, err)
	}
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	before, err := openGaussReadDesignReference(ctx, c, childTarget)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	written := make(chan error, 1)
	fired := false
	gaussDesignInstallExecutionGate(t, s, plan.SQL[0], func(context.Context) error {
		fired = true
		go func() {
			_, err := writer.ExecContext(ctx, "COMMENT ON TABLE "+child+" IS 'writer after rollback'")
			written <- err
		}()
		openGaussViewWaiting(t, db, childTarget.Object)
		select {
		case err := <-written:
			return errors.Join(err, errors.New("incoming child COMMENT escaped the native type transaction lock"))
		default:
		}
		cancel()
		return nil
	})
	out := s.openGaussDesignApply(applyCtx, request)
	if !fired || out.State != "rolled_back" || out.Verified || out.Actual != nil || !errors.Is(out.Error, context.Canceled) {
		t.Fatal("incoming cancellation returned without driver/rollback settlement", fired, out)
	}
	select {
	case err = <-written:
		if err != nil {
			t.Fatal("blocked child writer failed after rollback acknowledgement", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	fresh := gaussDesignOriginal(t, s, target)
	if fresh.Schema.Revision != original.Schema.Revision {
		t.Fatal("cancelled incoming parent ALTER did not restore original graph")
	}
	after, err := openGaussReadDesignReference(ctx, c, childTarget)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range before.Entries {
		if entry.Kind != "constraint" && entry.Kind != "trigger" {
			continue
		}
		found := false
		for _, current := range after.Entries {
			if current.Kind == entry.Kind && current.OID == entry.OID && current.SubID == entry.SubID && schemaddl.Signature(current.Data) == schemaddl.Signature(entry.Data) {
				found = true
			}
		}
		if !found {
			t.Fatal("cancelled incoming ALTER left replaced child FK/check trigger", entry.Kind, entry.OID)
		}
	}
	var constraintComment, tableComment string
	var parentID int
	if err = db.QueryRowContext(ctx, "SELECT parent_id FROM "+child).Scan(&parentID); err != nil || parentID != 1 {
		t.Fatal("cancelled incoming ALTER changed child values", parentID, err)
	}
	if err = db.QueryRowContext(ctx, "SELECT obj_description(oid,'pg_constraint') FROM pg_constraint WHERE conrelid=$1::regclass AND conname='incoming_fk'", child).Scan(&constraintComment); err != nil || constraintComment != "rollback retains this comment" {
		t.Fatal("cancelled incoming ALTER lost original FK comment", constraintComment, err)
	}
	if err = db.QueryRowContext(ctx, "SELECT obj_description($1::regclass,'pg_class')", child).Scan(&tableComment); err != nil || tableComment != "writer after rollback" {
		t.Fatal("rollback undid independently acknowledged child writer", tableComment, err)
	}
}
