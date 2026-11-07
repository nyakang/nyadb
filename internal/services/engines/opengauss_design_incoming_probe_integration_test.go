package engines

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	gausspq "gitcode.com/opengauss/openGauss-connector-go-pq"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussDesignIncomingIndependentProbesFixture(t *testing.T) {
	for _, operation := range []string{"type", "rename_table", "rename_parent", "child_slot_hole", "composite_reordered", "multiple", "not_valid", "restricted", "restricted_comment", "invalid_type"} {
		t.Run(operation, func(t *testing.T) {
			s, db, target := openGaussTableFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
			defer cancel()
			parent, child := qualified(target.Object), quote(target.Object.Schema)+".other"
			setup := "ALTER TABLE " + child + " ADD COLUMN parent_id bigint; ALTER TABLE " + child + " ADD CONSTRAINT incoming_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id) ON DELETE CASCADE ON UPDATE RESTRICT DEFERRABLE INITIALLY DEFERRED; INSERT INTO " + child + " VALUES(9,1)"
			if operation == "child_slot_hole" {
				setup = "ALTER TABLE " + child + " ADD COLUMN gap text; ALTER TABLE " + child + " ADD COLUMN parent_id bigint; ALTER TABLE " + child + " DROP COLUMN gap; ALTER TABLE " + child + " ADD CONSTRAINT incoming_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id) DEFERRABLE INITIALLY DEFERRED; INSERT INTO " + child + " VALUES(9,1)"
			}
			if operation == "composite_reordered" {
				setup = "DROP TABLE " + parent + "; CREATE TABLE " + parent + "(id bigint,gap text,key text,PRIMARY KEY(id,key)); ALTER TABLE " + parent + " DROP COLUMN gap; INSERT INTO " + parent + " VALUES(1,'native'); DROP TABLE " + child + "; CREATE TABLE " + child + "(child_key text,child_id bigint,CONSTRAINT incoming_fk FOREIGN KEY(child_key,child_id) REFERENCES " + parent + "(key,id) MATCH FULL DEFERRABLE INITIALLY DEFERRED); INSERT INTO " + child + " VALUES('native',1)"
			}
			if operation == "not_valid" {
				setup = "ALTER TABLE " + child + " ADD COLUMN parent_id bigint; INSERT INTO " + child + " VALUES(9,999); ALTER TABLE " + child + " ADD CONSTRAINT incoming_fk FOREIGN KEY(parent_id) REFERENCES " + parent + "(id) NOT VALID"
			}
			if _, err := db.ExecContext(ctx, setup); err != nil {
				t.Fatal(err)
			}
			if operation == "restricted_comment" {
				if _, err := db.ExecContext(ctx, "SET standard_conforming_strings=on; COMMENT ON CONSTRAINT incoming_fk ON "+child+" IS '保留注释 '' and \\ literal'"); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "multiple" {
				if _, err := db.ExecContext(ctx, "ALTER TABLE "+child+" ADD CONSTRAINT incoming_second FOREIGN KEY(parent_id) REFERENCES "+parent+"(id) ON DELETE RESTRICT; CREATE TABLE "+quote(target.Object.Schema)+".third(parent_id bigint REFERENCES "+parent+"(id))"); err != nil {
					t.Fatal(err)
				}
			}
			if operation != "composite_reordered" {
				if _, err := db.ExecContext(ctx, "CREATE SEQUENCE "+quote(target.Object.Schema)+".incoming_side_effect; ALTER TABLE "+child+" ALTER COLUMN parent_id SET DEFAULT nextval('"+quote(target.Object.Schema)+".incoming_side_effect')"); err != nil {
					t.Fatal(err)
				}
			}
			actor := s
			if operation == "restricted" || operation == "restricted_comment" {
				role := fmt.Sprintf("nyadb_incoming_probe_%d", time.Now().UnixNano())
				password := os.Getenv("NYADB_OPENGAUSS_PASSWORD")
				gaussDesignFixtureRoleSQL(t, "CREATE ROLE "+quote(role)+" WITH LOGIN PASSWORD '"+strings.ReplaceAll(password, "'", "''")+"';")
				t.Cleanup(func() {
					gaussDesignFixtureRoleSQL(t, "ALTER TABLE "+child+" DROP CONSTRAINT incoming_fk; DROP OWNED BY "+quote(role)+"; DROP ROLE "+quote(role)+";")
				})
				gaussDesignFixtureRoleSQL(t, "ALTER TABLE "+parent+" OWNER TO "+quote(role)+"; GRANT USAGE,CREATE ON SCHEMA "+quote(target.Object.Schema)+" TO "+quote(role)+";")
				profile := s.backupProfile
				profile.Username = role
				opened, err := OpenSession(ctx, profile, password)
				if err != nil {
					t.Fatal(err)
				}
				actor = opened.(*Session)
				target.Generation = actor.generation
				defer actor.Close()
			}
			original := gaussDesignOriginal(t, actor, target)
			desired := original.Schema.Table.Clone()
			if operation == "rename_table" {
				desired.Name = "renamed_table"
			} else {
				for i, clause := range desired.Clauses {
					if clause.Kind != "column" || clause.Name != "id" {
						continue
					}
					if operation == "rename_parent" {
						desired.Clauses[i].Name = "renamed_key"
						continue
					}
					field, err := schemaddl.ParseOpenGaussField(clause.SQL)
					if err != nil {
						t.Fatal(err)
					}
					field.Type = "integer"
					if operation == "invalid_type" {
						field.Type = "uuid"
						field.HasDefault, field.Default = false, ""
					}
					desired.Clauses[i].SQL, err = field.SQL()
					if err != nil {
						t.Fatal(err)
					}
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
			request := contract.SchemaDesignRequest{Original: original, Desired: desired}
			beforeRequest := schemaddl.Signature(request)
			before, err := openGaussCaptureDesignIncoming(ctx, c, request)
			if err != nil {
				t.Fatal("capture native incoming evidence", err)
			}
			canonical, err := openGaussCanonicalDesign(ctx, c, request)
			if operation == "invalid_type" {
				if err == nil || canonical.Catalog != nil || !strings.Contains(err.Error(), "foreign key") {
					t.Fatal("incompatible incoming equality accepted", err)
				}
			} else if err != nil || len(canonical.Incoming) != len(before) || len(canonical.IncomingProbes) != len(before) {
				t.Fatal("independent incoming probes", len(canonical.Incoming), len(canonical.IncomingProbes), err)
			}
			if err = openGaussVerifyDesignIncomingUnchanged(ctx, c, request, before); err != nil {
				t.Fatal("private incoming declaration changed original child", err)
			}
			if schemaddl.Signature(request) != beforeRequest {
				t.Fatal("incoming probe changed request snapshot")
			}
			if operation != "composite_reordered" {
				var called bool
				if err = db.QueryRowContext(ctx, "SELECT is_called FROM "+quote(target.Object.Schema)+".incoming_side_effect").Scan(&called); err != nil || called {
					t.Fatal("incoming probe evaluated source default", called, err)
				}
			}
			var probes int
			if err = c.QueryRowContext(ctx, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname LIKE '__nyadb_gauss_%'", target.Object.Schema).Scan(&probes); err != nil || probes != 0 {
				t.Fatal("incoming probe left private objects", probes, err)
			}
			if operation == "restricted" || operation == "restricted_comment" {
				var canRead, canManage, canReference bool
				if err = c.QueryRowContext(ctx, "SELECT has_table_privilege($1::regclass,'SELECT'),pg_has_role(current_user,c.relowner,'USAGE'),has_column_privilege($1::regclass,'parent_id','REFERENCES') FROM pg_class c WHERE c.oid=$1::regclass", child).Scan(&canRead, &canManage, &canReference); err != nil || canRead || canManage || canReference {
					t.Fatal("incoming metadata probe elevated original child rights", canRead, canManage, canReference, err)
				}
				if err = rollbackDesign(c); err != nil {
					t.Fatal(err)
				}
				out := actor.openGaussDesignApply(ctx, request)
				if out.State != "rolled_back" || out.Error == nil || !strings.Contains(out.Error.Error(), "permission denied") || out.Verified || out.Actual != nil {
					t.Fatal("private incoming apply bypassed native child REFERENCES", out)
				}
				fresh := gaussDesignOriginal(t, actor, target)
				if fresh.Schema.Revision != original.Schema.Revision {
					t.Fatal("native REFERENCES rejection changed parent metadata")
				}
				if _, err = db.ExecContext(ctx, "GRANT REFERENCES(parent_id) ON "+child+" TO "+quote(actor.backupProfile.Username)); err != nil {
					t.Fatal(err)
				}
				if operation == "restricted_comment" {
					c2, err := actor.db.Conn(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = c2.ExecContext(ctx, "BEGIN; SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err != nil {
						_ = c2.Close()
						t.Fatal(err)
					}
					before, err = openGaussCaptureDesignIncoming(ctx, c2, request)
					err = errors.Join(err, rollbackDesign(c2))
					_ = c2.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				out = actor.openGaussDesignApply(ctx, request)
				if operation == "restricted_comment" {
					var native *gausspq.Error
					if out.State != "rolled_back" || out.Verified || out.Actual != nil || !errors.As(out.Error, &native) || native.Code != "42501" || !strings.Contains(out.Error.Error(), "comment restoration") {
						t.Fatal("REFERENCES-only actor silently lost incoming comment or bypassed native COMMENT rights", out)
					}
					fresh = gaussDesignOriginal(t, actor, target)
					if fresh.Schema.Revision != original.Schema.Revision {
						t.Fatal("denied incoming comment restoration changed parent")
					}
					c2, err := actor.db.Conn(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if err = openGaussVerifyDesignIncomingUnchanged(ctx, c2, request, before); err != nil {
						_ = c2.Close()
						t.Fatal("denied incoming comment restoration changed child", err)
					}
					_ = c2.Close()
					// Retry with independently granted native COMMENT rights. The
					// preceding failure must have retained the entire original FK.
					gaussDesignFixtureRoleSQL(t, "ALTER TABLE "+child+" OWNER TO "+quote(actor.backupProfile.Username)+";")
					out = actor.openGaussDesignApply(ctx, request)
				}
				if out.Error != nil || out.State != "committed" || !out.Verified || out.Actual == nil {
					t.Fatal("private incoming apply rejected native authorized actor", out)
				}
				if operation == "restricted_comment" {
					var comment string
					if err = actor.db.QueryRowContext(ctx, "SELECT obj_description(oid,'pg_constraint') FROM pg_constraint WHERE conrelid=$1::regclass AND conname='incoming_fk'", child).Scan(&comment); err != nil || comment != "保留注释 ' and \\ literal" {
						t.Fatal("native authorized retry lost exact incoming comment", comment, err)
					}
				}
			}
			if ops := actor.SchemaDesign(); ops.Apply != nil || ops.CanApply != nil && ops.CanApply(target) {
				t.Fatal("independent incoming probe opened public Apply")
			}
		})
	}
}
