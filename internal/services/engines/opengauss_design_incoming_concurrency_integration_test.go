package engines

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignIncomingIndependentChildCatalogAndParentBarrierFixture(t *testing.T) {
	for _, operation := range []string{"comment", "foreign_key"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			parent, child := qualified(target.Object), quote(target.Object.Schema)+".other"
			if _, err := db.ExecContext(ctx, "ALTER TABLE "+child+" ADD COLUMN parent_id bigint; ALTER TABLE "+child+" ADD CONSTRAINT incoming_fk FOREIGN KEY(parent_id) REFERENCES "+parent+"(id); INSERT INTO "+child+" VALUES(9,1)"); err != nil {
				t.Fatal(err)
			}
			original := gaussDesignOriginal(t, s, target)
			request := contract.SchemaDesignRequest{Original: original, Desired: original.Schema.Table.Clone()}
			c, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; LOCK TABLE "+parent+" IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			defer rollbackDesign(c)
			incoming, err := openGaussCaptureDesignIncoming(ctx, c, request)
			if err != nil || len(incoming) != 1 {
				t.Fatal("incoming baseline", len(incoming), err)
			}
			if operation == "comment" {
				if _, err = db.ExecContext(ctx, "COMMENT ON TABLE "+child+" IS 'independently committed child metadata'"); err != nil {
					t.Fatal(err)
				}
				// The parent catalog remains unchanged: its AXX lock cannot
				// substitute for independent child metadata evidence.
				parentAfter, err := openGaussReadDesign(ctx, c, target)
				if err != nil || parentAfter.Schema.Revision != original.Schema.Revision {
					t.Fatal("unrelated child comment changed parent evidence", err)
				}
				if err = openGaussVerifyDesignIncomingUnchanged(ctx, c, request, incoming); !errors.Is(err, schemaddl.ErrConflict) {
					t.Fatal("changed child metadata accepted through unchanged parent", err)
				}
				return
			}
			writer, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			done := make(chan error, 1)
			go func() {
				_, err := writer.ExecContext(ctx, "ALTER TABLE "+child+" DROP CONSTRAINT incoming_fk")
				done <- err
			}()
			openGaussViewWaiting(t, db, target.Object)
			if err = openGaussVerifyDesignIncomingUnchanged(ctx, c, request, incoming); err != nil {
				t.Fatal("blocked child FK DDL changed visible baseline", err)
			}
			if err = rollbackDesign(c); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err = openGaussVerifyDesignIncomingUnchanged(ctx, c, request, incoming); !errors.Is(err, schemaddl.ErrConflict) {
				t.Fatal("committed incoming FK deletion retained old evidence", err)
			}
		})
	}
}
