package schemaddl

import (
	"errors"
	"strings"
	"testing"
)

func TestOpenGaussViewOptionsRoundTripAndPreview(t *testing.T) {
	ddl := `CREATE VIEW "odd AS
name"."v AS
name" (id,note) WITH (security_barrier=TRUE) AS SELECT id,note FROM source WITH LOCAL CHECK OPTION;`
	v, err := ParseView("opengauss", ddl)
	if err != nil || strings.Contains(v.SelectSQL, "CHECK OPTION") || Signature(v.Options) != Signature([]string{"check_option=local", "security_barrier=true"}) {
		t.Fatal("native flags not parsed", v, err)
	}
	canonical, err := CreateView("opengauss", v, v.Schema)
	if err != nil || !strings.Contains(canonical, `"v AS`+"\n"+`name" ("id","note") WITH (check_option=local,security_barrier=true) AS`+"\n") {
		t.Fatal("options damaged a quoted identifier", canonical, err)
	}
	round, err := ParseView("opengauss", canonical)
	if err != nil || Signature(round) != Signature(v) {
		t.Fatal("native flags not preserved", round, err)
	}
	base := ViewSnapshot{Engine: "opengauss", Revision: "captured", Exists: true, View: v}
	desired := v.Clone()
	desired.SelectSQL = "SELECT id,upper(note) AS note FROM source"
	p, err := PlanView(base, "alter", desired, v.Schema)
	if err != nil || len(p.SQL) != 1 || !strings.HasPrefix(p.SQL[0], "CREATE OR REPLACE VIEW") || !strings.Contains(p.SQL[0], "security_barrier=true") || Signature(p.Desired.Options) != Signature(v.Options) {
		t.Fatal("preview lost flags", p, err)
	}
	desired = v.Clone()
	desired.Name = "renamed"
	p, err = PlanView(base, "rename", desired, v.Schema)
	if err != nil || len(p.SQL) != 1 || !strings.HasPrefix(p.SQL[0], "ALTER VIEW") {
		t.Fatal("native rename plan", p, err)
	}
	desired.Options[0] = "check_option=cascaded"
	if _, err = PlanView(base, "rename", desired, v.Schema); !errors.Is(err, ErrUnsupported) {
		t.Fatal("rename silently edited flags", err)
	}
	if v.Options[0] != "check_option=local" {
		t.Fatal("clone changed original flags")
	}
	for _, engine := range []string{"postgresql", "sqlite"} {
		if _, err = CreateView(engine, v, v.Schema); !errors.Is(err, ErrUnsupported) {
			t.Fatal("native flags leaked into", engine, err)
		}
	}
}

func TestOpenGaussViewRejectsUnsupportedDefinitionsAndScope(t *testing.T) {
	for _, ddl := range []string{
		"CREATE VIEW v WITH(security_barrier=true,security_barrier=false) AS SELECT 1",
		"CREATE VIEW v WITH(check_option=local) AS SELECT 1 WITH CHECK OPTION",
		"CREATE VIEW v WITH(unknown=true) AS SELECT 1",
		"CREATE VIEW v WITH(security_barrier=maybe) AS SELECT 1",
		"CREATE VIEW v WITH(check_option=global) AS SELECT 1",
		"CREATE VIEW v AS SELECT 1; DROP TABLE source",
		"CREATE MATERIALIZED VIEW v AS SELECT 1",
		"CREATE VIEW v AS SELECT 'unterminated",
	} {
		if _, err := ParseView("opengauss", ddl); err == nil {
			t.Fatal("unsupported definition accepted", ddl)
		}
	}
	v := View{Schema: "original", Name: "v", SelectSQL: "SELECT 1"}
	base := ViewSnapshot{Engine: "opengauss", Revision: "captured", Exists: true, View: v}
	for _, action := range []string{"alter", "rename", "drop"} {
		if _, err := PlanView(base, action, v, "replacement"); !errors.Is(err, ErrUnsupported) {
			t.Fatal("cross-schema original accepted", action, err)
		}
	}
	v.Schema = "replacement"
	if _, err := PlanView(base, "alter", v, "original"); !errors.Is(err, ErrUnsupported) {
		t.Fatal("cross-schema desired accepted", err)
	}
	for _, suffix := range []string{"WITH CHECK OPTION", "WITH LOCAL CHECK OPTION", "WITH CASCADED CHECK OPTION"} {
		v, err := ParseView("opengauss", "CREATE VIEW v AS SELECT 1 "+suffix)
		if err != nil || len(v.Options) != 1 {
			t.Fatal("native check option", suffix, err)
		}
	}
}
