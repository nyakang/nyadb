package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func verifyPlan(kind, raw string) ObjectPlan {
	return ObjectPlan{Action: "create", Original: ObjectDefinition{ObjectIdentity: ObjectIdentity{Database: "db", Name: "o", Kind: kind}}, SQL: raw}
}
func verifyActual(kind, raw, mode string) ObjectDefinition {
	return ObjectDefinition{ObjectIdentity: ObjectIdentity{Database: "db", Name: "o", Kind: kind}, SQL: raw, SQLMode: mode, SQLModeKnown: kind == "procedure" || kind == "function" || kind == "event"}
}
func TestObjectDefinitionEquivalentPreservesRoutineBodyCommentsLiteralsAndMode(t *testing.T) {
	p := verifyPlan("procedure", "CREATE PROCEDURE db.o(IN p INT) BEGIN /* preserve this */ SELECT 'NULL' AS value; END")
	actual := verifyActual("procedure", "CREATE DEFINER=`u`@`h` PROCEDURE `o`(IN p int) BEGIN /* preserve this */ SELECT 'NULL' AS `value`; END", "STRICT_TRANS_TABLES")
	execution := ObjectVerificationContext{CurrentUser: "u@h", SQLMode: "STRICT_TRANS_TABLES"}
	if e := ObjectDefinitionEquivalent(p, actual, execution); e != nil {
		t.Fatal(e)
	}
	for _, change := range []struct {
		name   string
		actual ObjectDefinition
	}{{"literal", verifyActual("procedure", strings.Replace(actual.SQL, "'NULL'", "'null'", 1), actual.SQLMode)}, {"comment", verifyActual("procedure", strings.Replace(actual.SQL, "preserve this", "changed comment", 1), actual.SQLMode)}, {"body", verifyActual("procedure", strings.Replace(actual.SQL, "SELECT 'NULL'", "SELECT 2", 1), actual.SQLMode)}, {"mode", verifyActual("procedure", actual.SQL, "NO_ZERO_DATE")}, {"definer", verifyActual("procedure", strings.Replace(actual.SQL, "`u`@`h`", "`other`@`h`", 1), actual.SQLMode)}} {
		t.Run(change.name, func(t *testing.T) {
			if e := ObjectDefinitionEquivalent(p, change.actual, execution); e == nil {
				t.Fatal("changed stored definition was accepted")
			}
		})
	}
}
func TestObjectDefinitionEquivalentViewChecksQueryColumnsAndSecurity(t *testing.T) {
	p := verifyPlan("view", "CREATE VIEW db.o AS SELECT 'NULL' AS value")
	actual := verifyActual("view", "CREATE ALGORITHM=UNDEFINED DEFINER=`u`@`h` SQL SECURITY DEFINER VIEW `o` AS select 'NULL' AS `value`", "")
	execution := ObjectVerificationContext{CurrentUser: "u@h"}
	if e := ObjectDefinitionEquivalent(p, actual, execution); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{strings.Replace(actual.SQL, "'NULL'", "NULL", 1), strings.Replace(actual.SQL, "SECURITY DEFINER", "SECURITY INVOKER", 1), strings.Replace(actual.SQL, "AS `value`", "AS `other`", 1), strings.Replace(actual.SQL, "UNDEFINED", "TEMPTABLE", 1)} {
		actual.SQL = raw
		if e := ObjectDefinitionEquivalent(p, actual, execution); e == nil {
			t.Fatal("view query/header drift was accepted")
		}
	}
}
func TestObjectDefinitionEquivalentEventRequiresScheduleBodyAndProvenDefaults(t *testing.T) {
	p := verifyPlan("event", "CREATE EVENT db.o ON SCHEDULE EVERY 1 DAY DISABLE DO SELECT 'NULL'")
	actual := verifyActual("event", "CREATE DEFINER=`u`@`h` EVENT `o` ON SCHEDULE EVERY 1 DAY STARTS '2026-10-31 10:00:00' ON COMPLETION NOT PRESERVE DISABLE DO SELECT 'NULL'", "STRICT_TRANS_TABLES")
	execution := ObjectVerificationContext{CurrentUser: "u@h", SQLMode: actual.SQLMode, EventDefaultStart: verifiedTokens([]sqltext.Token{{Text: "'2026-10-31 10:00:00'", Quoted: true}})}
	if e := ObjectDefinitionEquivalent(p, actual, execution); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{strings.Replace(actual.SQL, "EVERY 1 DAY", "EVERY 2 DAY", 1), strings.Replace(actual.SQL, "10:00:00", "12:00:00", 1), strings.Replace(actual.SQL, "NOT PRESERVE", "PRESERVE", 1), strings.Replace(actual.SQL, "DISABLE", "ENABLE", 1), strings.Replace(actual.SQL, "'NULL'", "'changed'", 1)} {
		actual.SQL = raw
		if e := ObjectDefinitionEquivalent(p, actual, execution); e == nil {
			t.Fatal("event body/options drift was accepted")
		}
	}
}
func TestObjectDefinitionEquivalentDatabaseChecksResolvedDefaults(t *testing.T) {
	p := verifyPlan("database", "CREATE DATABASE db CHARACTER SET utf8mb4")
	p.Original.Name = ""
	actual := verifyActual("database", "CREATE DATABASE `db` /*!40100 DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci */ /*!80016 DEFAULT ENCRYPTION='N' */", "")
	actual.Name = ""
	execution := ObjectVerificationContext{DatabaseCharset: "utf8mb4", DatabaseCollation: "utf8mb4_0900_ai_ci", DatabaseEncryption: verifiedTokens([]sqltext.Token{{Text: "'N'", Quoted: true}})}
	if e := ObjectDefinitionEquivalent(p, actual, execution); e != nil {
		t.Fatal(e)
	}
	actual.SQL = strings.Replace(actual.SQL, "utf8mb4_0900_ai_ci", "utf8mb4_bin", 1)
	if e := ObjectDefinitionEquivalent(p, actual, execution); e == nil {
		t.Fatal("default collation drift was accepted")
	}
}

type objectVerifyFixture struct {
	actual string
	writes int
	mode   string
}
type objectVerifyConnector struct{ f *objectVerifyFixture }

func (c objectVerifyConnector) Connect(context.Context) (driver.Conn, error) {
	return &objectVerifyConn{c.f}, nil
}
func (objectVerifyConnector) Driver() driver.Driver { return mysqlSessionMockDriver{} }

type objectVerifyConn struct{ f *objectVerifyFixture }

func (*objectVerifyConn) Close() error { return nil }
func (*objectVerifyConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*objectVerifyConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected begin") }
func (c *objectVerifyConn) ExecContext(_ context.Context, sql string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(sql, "CREATE ") {
		c.f.writes++
	}
	if strings.HasPrefix(sql, "SET SESSION sql_mode") && len(args) == 1 {
		c.f.mode = args[0].Value.(string)
	}
	return driver.RowsAffected(0), nil
}
func (c *objectVerifyConn) QueryContext(_ context.Context, sql string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.HasPrefix(sql, "SHOW CREATE VIEW"):
		return &structureRows{cols: []string{"View", "Create View"}, values: [][]driver.Value{{"o", c.f.actual}}}, nil
	case strings.HasPrefix(sql, "SHOW CREATE PROCEDURE"):
		return &structureRows{cols: []string{"Procedure", "Create Procedure", "sql_mode"}, values: [][]driver.Value{{"o", c.f.actual, c.f.mode}}}, nil
	case strings.Contains(sql, "CURRENT_USER()"):
		return &structureRows{cols: []string{"mode", "user"}, values: [][]driver.Value{{c.f.mode, "u@h"}}}, nil
	case strings.Contains(sql, "COUNT(*)"):
		return &structureRows{cols: []string{"count"}, values: [][]driver.Value{{int64(0)}}}, nil
	}
	return nil, errors.New("unexpected query")
}
func TestApplyObjectPostAcknowledgementDriftFailsVerificationWithoutReplay(t *testing.T) {
	f := &objectVerifyFixture{actual: "CREATE ALGORITHM=UNDEFINED DEFINER=`u`@`h` SQL SECURITY DEFINER VIEW `o` AS SELECT 'externally changed' AS `value`"}
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(objectVerifyConnector{f}), nil
	}
	if e := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 78}, ""); e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	p := verifyPlan("view", "CREATE VIEW db.o AS SELECT 'requested' AS value")
	p.Original.Generation = m.SessionGeneration(78)
	out := m.ApplyObject(context.Background(), 78, p)
	if out.Verified || out.Error == nil || out.State != "executed" || f.writes != 1 || len(out.Steps) != 1 || out.Steps[0].State != "executed" || out.ActualDefinition == nil {
		t.Fatalf("ACK/readback drift was falsely successful or replayed: %+v", out)
	}
}
func TestApplyObjectPreservesKnownEmptySQLModeOnCopiedRoutine(t *testing.T) {
	f := &objectVerifyFixture{actual: "CREATE DEFINER=`u`@`h` PROCEDURE `o`() SELECT 1", mode: "STRICT_TRANS_TABLES"}
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(objectVerifyConnector{f}), nil
	}
	if e := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 79}, ""); e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	p := verifyPlan("procedure", "CREATE PROCEDURE db.o() SELECT 1")
	p.Original.Generation = m.SessionGeneration(79)
	p.Original.SQLModeKnown = true
	out := m.ApplyObject(context.Background(), 79, p)
	if !out.Verified || out.Error != nil || f.mode != "" || f.writes != 1 {
		t.Fatalf("known empty mode was replaced by a connection default: %+v", out)
	}
}

func TestObjectDefinitionEquivalentKnownEmptyModeAndUnknownNormalizationFailClosed(t *testing.T) {
	p := verifyPlan("procedure", "CREATE PROCEDURE db.o() SELECT 1")
	p.Original.SQLModeKnown = true
	actual := verifyActual("procedure", "CREATE DEFINER=`u`@`h` PROCEDURE `o`() SELECT 1", "STRICT_TRANS_TABLES")
	if e := ObjectDefinitionEquivalent(p, actual, ObjectVerificationContext{SQLMode: "STRICT_TRANS_TABLES", CurrentUser: "u@h"}); e == nil {
		t.Fatal("an explicitly empty source mode was replaced by strict mode")
	}
	p.Original.SQLModeKnown = false
	actual.SQLModeKnown = false
	if e := ObjectDefinitionEquivalent(p, actual, ObjectVerificationContext{SQLMode: "STRICT_TRANS_TABLES", CurrentUser: "u@h"}); e == nil {
		t.Fatal("an unknown stored mode was treated as known")
	}
	actual.SQLModeKnown = true
	p.SQL = "CREATE PROCEDURE db.o() BEGIN /*!90000 SELECT 1 */; END"
	actual.SQL = "CREATE DEFINER=`u`@`h` PROCEDURE `o`() BEGIN /*!90000 SELECT 1 */; END"
	if e := ObjectDefinitionEquivalent(p, actual, ObjectVerificationContext{SQLMode: actual.SQLMode, CurrentUser: "u@h"}); e == nil {
		t.Fatal("unrecognized executable comments were assumed equivalent")
	}
}
func TestObjectDefinitionEquivalentAlterEventRetainsUnchangedOptions(t *testing.T) {
	original := "CREATE DEFINER=`u`@`h` EVENT `o` ON SCHEDULE EVERY 1 DAY STARTS '2026-10-31 10:00:00' ON COMPLETION PRESERVE DISABLE COMMENT 'keep' DO SELECT 1"
	p := verifyPlan("event", "ALTER EVENT db.o DO SELECT 2")
	p.Action = "alter"
	p.Original.SQL = original
	actual := verifyActual("event", strings.Replace(original, "SELECT 1", "SELECT 2", 1), "")
	if e := ObjectDefinitionEquivalent(p, actual, ObjectVerificationContext{CurrentUser: "u@h"}); e != nil {
		t.Fatal(e)
	}
	actual.SQL = strings.Replace(actual.SQL, "COMMENT 'keep'", "COMMENT 'changed'", 1)
	if e := ObjectDefinitionEquivalent(p, actual, ObjectVerificationContext{CurrentUser: "u@h"}); e == nil {
		t.Fatal("partial event ALTER ignored an unrelated option drift")
	}
}
