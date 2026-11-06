package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	gausspq "gitcode.com/opengauss/openGauss-connector-go-pq"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func openGaussViewLifecycle(t *testing.T) (*Session, *sql.DB, contract.ViewDesignRequest) {
	t.Helper()
	s, p := openGaussFixture(t)
	observer, err := (openGaussDialect{}).open(p, os.Getenv("NYADB_OPENGAUSS_PASSWORD"), nil)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("nyadb_gauss_view_life_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := observer.ExecContext(ctx, "CREATE SCHEMA "+quote(schema)+"; CREATE VIEW "+quote(schema)+".v AS SELECT 1::bigint AS id; CREATE TABLE "+quote(schema)+".occupied(id bigint)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := observer.ExecContext(ctx, "DROP SCHEMA "+quote(schema)+" CASCADE"); err != nil {
			t.Error(err)
		}
		_ = observer.Close()
	})
	target := contract.SchemaDesignTarget{Object: s.Namespace(schema).Object("view", "v"), Generation: s.Generation()}
	base, err := s.ViewDesign().Snapshot(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	desired := base.Schema.View.Clone()
	desired.SelectSQL = "SELECT 2::bigint AS id"
	return s, observer, contract.ViewDesignRequest{Original: base, Action: "alter", Desired: desired}
}

func openGaussViewLocker(t *testing.T, db *sql.DB, object contract.ObjectRef) *sql.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rollbackDesign(c); _ = c.Close() })
	if _, err := c.ExecContext(ctx, "BEGIN; ALTER VIEW "+qualified(object)+" OWNER TO nyadb_it"); err != nil {
		t.Fatal(err)
	}
	return c
}

func openGaussViewWaiting(t *testing.T, observer *sql.DB, object contract.ObjectRef) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := observer.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation=$1::regclass AND NOT granted)", qualified(object)).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("native view never entered ownership lock wait")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func openGaussViewAwait(t *testing.T, ch <-chan contract.ViewDesignOutcome) contract.ViewDesignOutcome {
	t.Helper()
	select {
	case out := <-ch:
		return out
	case <-time.After(10 * time.Second):
		t.Fatal("native view did not acknowledge completion")
		return contract.ViewDesignOutcome{}
	}
}

func TestOpenGaussViewLockWaitRejectsExternalChangeAndReplacementFixture(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprint(replace), func(t *testing.T) {
			s, observer, r := openGaussViewLifecycle(t)
			locker := openGaussViewLocker(t, observer, r.Original.Target.Object)
			ch := make(chan contract.ViewDesignOutcome, 1)
			ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			go func() { ch <- s.ViewDesign().Apply(ctx, r) }()
			openGaussViewWaiting(t, observer, r.Original.Target.Object)
			q := "COMMENT ON VIEW " + qualified(r.Original.Target.Object) + " IS 'committed during wait'"
			if replace {
				q = "DROP VIEW " + qualified(r.Original.Target.Object) + "; CREATE VIEW " + qualified(r.Original.Target.Object) + " AS SELECT 1::bigint AS id"
			}
			if _, err := locker.ExecContext(ctx, q+"; COMMIT"); err != nil {
				t.Fatal(err)
			}
			_ = locker.Close()
			out := openGaussViewAwait(t, ch)
			if !errors.Is(out.Error, schemaddl.ErrConflict) || out.State != "rolled_back" || out.Verified || out.Actual != nil {
				t.Fatal("native wait overwrote external catalog", out)
			}
			var id int64
			if err := observer.QueryRowContext(ctx, "SELECT id FROM "+qualified(r.Original.Target.Object)).Scan(&id); err != nil || id != 1 {
				t.Fatal("external view replaced by stale SQL", id, err)
			}
			if !replace {
				var comment string
				if err := observer.QueryRowContext(ctx, "SELECT obj_description($1::regclass,'pg_class')", qualified(r.Original.Target.Object)).Scan(&comment); err != nil || comment != "committed during wait" {
					t.Fatal("external comment lost", comment, err)
				}
			}
		})
	}
}

func TestOpenGaussViewCancellationAcknowledgesLockWaitFixture(t *testing.T) {
	s, observer, r := openGaussViewLifecycle(t)
	locker := openGaussViewLocker(t, observer, r.Original.Target.Object)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	ch := make(chan contract.ViewDesignOutcome, 1)
	go func() { ch <- s.ViewDesign().Apply(ctx, r) }()
	openGaussViewWaiting(t, observer, r.Original.Target.Object)
	stop()
	out := openGaussViewAwait(t, ch)
	if !errors.Is(out.Error, context.Canceled) || out.Verified || out.Actual != nil || out.State != "rolled_back" && out.State != "uncertain" {
		t.Fatal("native cancellation acquired replay permission", out)
	}
	if out.State == "uncertain" && !s.isClosed() {
		t.Fatal("uncertain native session remains usable")
	}
	if _, err := locker.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	_ = locker.Close()
	var id int64
	if err := observer.QueryRow("SELECT id FROM " + qualified(r.Original.Target.Object)).Scan(&id); err != nil || id != 1 {
		t.Fatal("cancelled view replayed SQL", id, err)
	}
}

func TestOpenGaussViewLostCommitReceiptClosesOriginalFixture(t *testing.T) {
	s, observer, r := openGaussViewLifecycle(t)
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
	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	out := s.ViewDesign().Apply(ctx, r)
	if out.State != "uncertain" || out.Verified || out.Actual != nil || !errors.Is(out.Error, io.ErrUnexpectedEOF) || !s.isClosed() {
		t.Fatal("lost native commit receipt permitted replay", out)
	}
	var id int64
	if err := observer.QueryRowContext(ctx, "SELECT id FROM "+qualified(r.Original.Target.Object)).Scan(&id); err != nil || id != 2 {
		t.Fatal("native test did not lose a committed acknowledgement", id, err)
	}
	if next := s.ViewDesign().Apply(ctx, r); next.State != "not_executed" || !errors.Is(next.Error, contract.ErrRevoked) {
		t.Fatal("lost commit SQL was replayed", next)
	}
}

func TestOpenGaussViewOccupiedTargetGenerationAndRevokedCreateFixture(t *testing.T) {
	s, observer, r := openGaussViewLifecycle(t)
	wrong := r.Clone()
	wrong.Original.Target.Generation++
	if out := s.ViewDesign().Apply(context.Background(), wrong); out.State != "not_executed" || !errors.Is(out.Error, contract.ErrRevoked) {
		t.Fatal("old generation dispatched native DDL", out)
	}
	occupied := r.Clone()
	occupied.Action, occupied.Desired.Name, occupied.Desired.SelectSQL = "rename", "occupied", r.Original.Schema.View.SelectSQL
	if out := s.ViewDesign().Apply(context.Background(), occupied); out.State != "rolled_back" || out.Error == nil || out.Verified {
		t.Fatal("occupied target replaced", out)
	}
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	schema := r.Original.Target.Object.Schema
	if _, err := observer.ExecContext(ctx, "REVOKE CREATE ON SCHEMA "+quote(schema)+" FROM nyadb_it"); err != nil {
		t.Fatal(err)
	}
	target := r.Original.Target
	target.Object.Name = "new_view"
	if _, err := s.ViewDesign().Snapshot(ctx, target); err == nil {
		t.Fatal("create snapshot ignored revoked schema privilege")
	}
	// Existing owner can still drop without CREATE permission. Post-drop reads
	// must not accidentally require privileges needed only by create.
	base, err := s.ViewDesign().Snapshot(ctx, r.Original.Target)
	if err != nil {
		t.Fatal(err)
	}
	out := s.ViewDesign().Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "drop", Desired: base.Schema.View.Clone()})
	if out.Error != nil || !out.Verified || out.State != "committed" {
		t.Fatal("owner drop incorrectly requires schema CREATE", out)
	}
}
