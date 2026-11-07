package engines

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Verify the native parent ALTER path before selecting a production incoming
// reference lock strategy. The child table belongs to a different actor in
// the restricted case; parent ownership must not grant child management.
func TestOpenGaussDesignIncomingNativeTypeChangeLocksChildFixture(t *testing.T) {
	for _, restrictedActor := range []bool{false, true} {
		t.Run(fmt.Sprint(restrictedActor), func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			parent := qualified(target.Object)
			child := quote(target.Object.Schema) + ".other"
			if _, err := db.ExecContext(ctx, "ALTER TABLE "+child+" ADD COLUMN parent_id bigint; ALTER TABLE "+child+" ADD CONSTRAINT incoming_fk FOREIGN KEY(parent_id) REFERENCES "+parent+"(id); INSERT INTO "+child+" VALUES(9,1)"); err != nil {
				t.Fatal(err)
			}
			actor := s
			if restrictedActor {
				role := fmt.Sprintf("nyadb_incoming_%d", time.Now().UnixNano())
				password := os.Getenv("NYADB_OPENGAUSS_PASSWORD")
				gaussDesignFixtureRoleSQL(t, "CREATE ROLE "+quote(role)+" WITH LOGIN PASSWORD '"+strings.ReplaceAll(password, "'", "''")+"';")
				t.Cleanup(func() {
					gaussDesignFixtureRoleSQL(t, "ALTER TABLE "+child+" DROP CONSTRAINT incoming_fk; DROP OWNED BY "+quote(role)+"; DROP ROLE "+quote(role)+";")
				})
				// Administrator changes only this test's owned fixture objects.
				gaussDesignFixtureRoleSQL(t, "ALTER TABLE "+parent+" OWNER TO "+quote(role)+"; GRANT USAGE,CREATE ON SCHEMA "+quote(target.Object.Schema)+" TO "+quote(role)+";")
				profile := s.backupProfile
				profile.Username = role
				opened, err := OpenSession(ctx, profile, password)
				if err != nil {
					t.Fatal(err)
				}
				actor = opened.(*Session)
				defer actor.Close()
				var allowed bool
				if err := actor.db.QueryRowContext(ctx, "SELECT has_table_privilege($1::regclass,'SELECT')", child).Scan(&allowed); err != nil || allowed {
					t.Fatal("restricted parent owner acquired child SELECT", allowed, err)
				}
			}
			c, err := actor.db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on; LOCK TABLE "+parent+" IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			defer rollbackDesign(c)
			statement := "ALTER TABLE " + parent + " ALTER COLUMN id TYPE integer USING id::integer"
			if restrictedActor {
				if _, err = c.ExecContext(ctx, statement); err == nil || !strings.Contains(err.Error(), "permission denied") {
					t.Fatal("native parent owner bypassed child REFERENCES permission", err)
				}
				if err = rollbackDesign(c); err != nil {
					t.Fatal(err)
				}
				// openGauss re-adds the FK after parent type changes and checks
				// REFERENCES on both key sides. Child SELECT is insufficient.
				if _, err = db.ExecContext(ctx, "GRANT SELECT ON "+child+" TO "+quote(actor.backupProfile.Username)); err != nil {
					t.Fatal(err)
				}
				if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; LOCK TABLE "+parent+" IN ACCESS EXCLUSIVE MODE"); err != nil {
					t.Fatal(err)
				}
				if _, err = c.ExecContext(ctx, statement); err == nil || !strings.Contains(err.Error(), "permission denied") {
					t.Fatal("child SELECT replaced native REFERENCES requirement", err)
				}
				if err = rollbackDesign(c); err != nil {
					t.Fatal(err)
				}
				if _, err = db.ExecContext(ctx, "REVOKE SELECT ON "+child+" FROM "+quote(actor.backupProfile.Username)+"; GRANT REFERENCES(parent_id) ON "+child+" TO "+quote(actor.backupProfile.Username)); err != nil {
					t.Fatal(err)
				}
				var canRead, canReference, canReferenceTable bool
				if err = c.QueryRowContext(ctx, "SELECT has_table_privilege($1::regclass,'SELECT'),has_column_privilege($1::regclass,'parent_id','REFERENCES'),has_table_privilege($1::regclass,'REFERENCES')", child).Scan(&canRead, &canReference, &canReferenceTable); err != nil || canRead || !canReference || canReferenceTable {
					t.Fatal("restricted child column REFERENCES fixture", canRead, canReference, canReferenceTable, err)
				}
				if _, err = c.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on; LOCK TABLE "+parent+" IN ACCESS EXCLUSIVE MODE"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = c.ExecContext(ctx, statement); err != nil {
				t.Fatal("native parent type transition with child column REFERENCES", err)
			}
			var held bool
			if err = c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND relation=$1::regclass AND mode='AccessExclusiveLock' AND granted)", child).Scan(&held); err != nil || !held {
				t.Fatal("native parent type change did not retain child AccessExclusiveLock", held, err)
			}
			writer, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if _, err = writer.ExecContext(ctx, "BEGIN"); err != nil {
				t.Fatal(err)
			}
			defer rollbackDesign(writer)
			done := make(chan error, 1)
			go func() {
				_, err := writer.ExecContext(ctx, "ALTER TABLE "+child+" ADD COLUMN committed_after_parent_rollback bigint; COMMIT")
				done <- err
			}()
			childObject := target.Object
			childObject.Name = "other"
			openGaussViewWaiting(t, db, childObject)
			if err = rollbackDesign(c); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}
