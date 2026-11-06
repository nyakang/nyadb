package engines

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestOpenGaussNativeViewManagementFixture(t *testing.T) {
	s, _ := openGaussFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	l, err := s.Reserve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	run := func(text string) contract.SQLResult {
		t.Helper()
		out, err := l.Execute(ctx, text)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	schema := fmt.Sprintf("nyadb_gauss_views_%d", time.Now().UnixNano())
	run("CREATE SCHEMA " + quote(schema) + "; CREATE TABLE " + quote(schema) + ".source(id BIGINT PRIMARY KEY,note TEXT); INSERT INTO " + quote(schema) + ".source VALUES(1,'原始 alpha');")
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := s.db.ExecContext(cleanup, "DROP SCHEMA "+quote(schema)+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	ops := s.ViewDesign()
	target := contract.SchemaDesignTarget{Object: s.Namespace(schema).Object("view", "v"), Generation: s.Generation()}
	if !ops.CanApply(target) {
		t.Fatal("native view entry is unavailable")
	}
	base, err := ops.Snapshot(ctx, target)
	if err != nil || base.Schema.Exists || base.Schema.Engine != "opengauss" {
		t.Fatal("native absence scope", err)
	}
	desired := schemaddl.View{Name: "v", Schema: schema, Columns: []string{"id", "note"}, SelectSQL: "SELECT id,note FROM source WITH LOCAL CHECK OPTION", Options: []string{"security_barrier=true"}}
	create := ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "create", Desired: desired})
	if create.Error != nil || !create.Verified || create.State != "committed" || create.Actual == nil {
		t.Fatal("native view create", create)
	}
	base = *create.Actual
	if schemaddl.Signature(base.Schema.View.Options) != schemaddl.Signature([]string{"check_option=local", "security_barrier=true"}) {
		t.Fatal("native create flags lost", base.Schema.DDL)
	}
	run("COMMENT ON VIEW " + quote(schema) + ".v IS '保留注释'; COMMENT ON COLUMN " + quote(schema) + ".v.note IS '列注释'; GRANT SELECT ON " + quote(schema) + ".v TO PUBLIC; CREATE VIEW " + quote(schema) + ".dependent AS SELECT note FROM " + quote(schema) + ".v;")
	base, err = ops.Snapshot(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	desired = base.Schema.View.Clone()
	desired.SelectSQL = "SELECT id,upper(note) AS note FROM source"
	alter := ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "alter", Desired: desired})
	if alter.Error != nil || !alter.Verified || alter.State != "committed" {
		t.Fatal("native view alter/preservation", alter)
	}
	if run("SELECT note FROM " + quote(schema) + ".dependent").Rows[0][0] != "原始 ALPHA" {
		t.Fatal("dependency changed unexpected values")
	}
	if run("SELECT obj_description('" + schema + ".v'::regclass,'pg_class')").Rows[0][0] != "保留注释" {
		t.Fatal("native view comment lost")
	}
	base = *alter.Actual
	if schemaddl.Signature(base.Schema.View.Options) != schemaddl.Signature([]string{"check_option=local", "security_barrier=true"}) {
		t.Fatal("native replacement reset view flags", base.Schema.DDL)
	}
	desired = base.Schema.View.Clone()
	desired.Columns = append(desired.Columns, "extra")
	desired.SelectSQL = "SELECT id,upper(note) AS note,id+1 AS extra FROM source"
	desired.Options = []string{"security_barrier=false", "check_option=cascaded"}
	appended := ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "alter", Desired: desired})
	if appended.Error != nil || !appended.Verified || appended.Actual == nil || len(appended.Actual.Schema.View.Columns) != 3 {
		t.Fatal("append column/change flags lost original view", appended)
	}
	base = *appended.Actual
	broken := base.Schema.View.Clone()
	broken.SelectSQL = "SELECT id,missing_column FROM source"
	failed := ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "alter", Desired: broken})
	if failed.State != "rolled_back" || failed.Error == nil || failed.Verified {
		t.Fatal("broken view was not rolled back", failed)
	}
	fresh, err := ops.Snapshot(ctx, target)
	if err != nil || schemaddl.Signature(fresh.Schema) != schemaddl.Signature(base.Schema) {
		t.Fatal("failed view edit changed catalog", err)
	}
	run("COMMENT ON VIEW " + quote(schema) + ".v IS '外部更改'")
	stale := ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "alter", Desired: desired})
	if !errors.Is(stale.Error, schemaddl.ErrConflict) || stale.Verified {
		t.Fatal("stale native view accepted", stale)
	}
	base, err = ops.Snapshot(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	desired = base.Schema.View.Clone()
	desired.Name = "renamed"
	renamed := ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "rename", Desired: desired})
	if renamed.Error != nil || !renamed.Verified || renamed.Actual == nil || renamed.Actual.Target.Object.Name != "renamed" {
		t.Fatal("native view rename", renamed)
	}
	base = *renamed.Actual
	if run("SELECT note FROM " + quote(schema) + ".dependent").Rows[0][0] != "原始 ALPHA" {
		t.Fatal("native rename broke dependent view")
	}
	blocked := ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "drop", Desired: base.Schema.View.Clone()})
	if blocked.State != "rolled_back" || blocked.Error == nil || blocked.Verified {
		t.Fatal("DROP view cascaded dependencies", blocked)
	}
	run("DROP VIEW " + quote(schema) + ".dependent")
	base, err = ops.Snapshot(ctx, base.Target)
	if err != nil {
		t.Fatal(err)
	}
	drop := ops.Apply(ctx, contract.ViewDesignRequest{Original: base, Action: "drop", Desired: base.Schema.View.Clone()})
	if drop.Error != nil || !drop.Verified || drop.State != "committed" || drop.Actual == nil || drop.Actual.Schema.Exists {
		t.Fatal("native view drop", drop)
	}
	if run("SELECT note FROM " + quote(schema) + ".source").Rows[0][0] != "原始 alpha" {
		t.Fatal("view management changed source rows")
	}
	tableTarget := target
	tableTarget.Object.Name = "source"
	if _, err := ops.Snapshot(ctx, tableTarget); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("table was accepted as view", err)
	}
	if !strings.Contains(base.Schema.DDL, "CREATE VIEW") {
		t.Fatal("native definition missing")
	}
}

func TestOpenGaussViewSelectPrivilegeDoesNotGrantManagementFixture(t *testing.T) {
	s, _ := openGaussFixture(t)
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	object := s.Namespace("public").Object("view", "fixture_owned_view")
	page, err := s.Page(ctx, contract.PageRequest{Object: object, Limit: 10})
	if err != nil || len(page.Rows) != 2 || page.Table.Editable {
		t.Fatal("SELECT-only native view unavailable/read-write", err)
	}
	if _, err := s.ViewDesign().Snapshot(ctx, contract.SchemaDesignTarget{Object: object, Generation: s.Generation()}); err == nil {
		t.Fatal("SELECT privilege granted owner management")
	}
}
