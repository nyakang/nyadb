package sqltext

import (
	"strings"
	"testing"
)

func TestDamengNativeScriptsPreserveBodiesQuotedSemicolonsAndDivision(t *testing.T) {
	source := `SELECT 'a;b',12/3;
CREATE TABLE t (procedure INT);
CREATE OR REPLACE PROCEDURE "中文" AS BEGIN
  IF 1=1 THEN FOR i IN 1..2 LOOP NULL; END LOOP; END IF;
  SELECT CASE WHEN 1=1 THEN ';' ELSE '/' END;
END;
/
DECLARE v VARCHAR(20):='x;y'; BEGIN BEGIN NULL; END; END;
/
SELECT 2;`
	parts, err := SplitDameng(source)
	if err != nil || len(parts) != 5 || !strings.HasSuffix(parts[2], "END;") || strings.Contains(parts[2], "\n/") || !strings.Contains(parts[0], "12/3") || parts[1] != "CREATE TABLE t (procedure INT)" {
		t.Fatal("native statement boundaries changed", parts, err)
	}
	if !DamengQuery(`WITH a AS (SELECT 1) SELECT * FROM a`) || DamengQuery(`WITH a AS (SELECT 1) DELETE FROM t`) {
		t.Fatal("native SELECT/CTE execution classification changed")
	}
	if DamengQuery("-- header\nCREATE VIEW v AS SELECT 1") || !DamengQuery("-- header\nWITH a AS(SELECT 1) SELECT * FROM a") {
		t.Fatal("leading comments changed native write/query classification")
	}
	for _, source := range []string{"SELECT 1; CREATE PROCEDURE p AS BEGIN NULL;", "BEGIN IF 1=1 THEN NULL; END;", "SELECT 'unfinished", "/* unfinished", "SELECT '\x00'", "SELECT " + strings.Repeat("x", 1<<20)} {
		if _, err := SplitDameng(source); err == nil {
			t.Fatal("invalid native source accepted")
		}
	}
}

func TestDamengDDLForeignSchemaQuoteBugDoesNotCorruptFieldDeclarations(t *testing.T) {
	ddl := "CREATE TABLE \"S\".\"T\"\r\n(\r\n\"ID\" BIGINT NOT NULL,\r\n\"D\" VARCHAR(128) DEFAULT 'line\nCONSTRAINT fake\nvalue',\r\nNOT CLUSTER PRIMARY KEY(\"ID\"),\r\nCONSTRAINT \"FK\" FOREIGN KEY(\"ID\") REFERENCES \"Schema '\"; -- \".\"Parent\"(\"ID\")) STORAGE(ON \"MAIN\");"
	columns, err := DamengDDLColumns(ddl)
	if err != nil || len(columns) != 2 || columns["D"].Type != "VARCHAR(128)" || columns["D"].Generated {
		t.Fatal("native foreign-reference quoting changed field proof", err, columns)
	}
}

func TestDamengDDLReadsNativeGeneratedFieldsWithoutMatchingQuotedDefaults(t *testing.T) {
	ddl := `CREATE TABLE "Schema"."Table" (
"Quoted"" Name" BIGINT IDENTITY(1, 1) NOT NULL,
"N" DECIMAL(38,20),
"Text" VARCHAR(128) DEFAULT 'IDENTITY(1,1), GENERATED ALWAYS AS',
"Virtual" BIGINT GENERATED ALWAYS AS (("N" + 1)) VIRTUAL,
"Date" TIMESTAMP(6) WITH TIME ZONE,
CONSTRAINT "pk" PRIMARY KEY("Quoted"" Name"), CHECK ("N">0)) STORAGE(ON "MAIN", CLUSTERBTR);`
	columns, err := DamengDDLColumns(ddl)
	if err != nil || len(columns) != 5 || !columns[`Quoted" Name`].Identity || !columns["Virtual"].Generated || columns["Text"].Generated || columns["N"].Type != "DECIMAL(38,20)" || columns["Date"].Type != "TIMESTAMP(6) WITH TIME ZONE" {
		t.Fatal("native DDL field proof changed", columns, err)
	}
	for _, ddl := range []string{`CREATE TABLE t ("A" INT`, `CREATE TABLE t ("A" INT,"A" INT)`, `CREATE TABLE t ("A" DEFAULT 1)`} {
		if _, err := DamengDDLColumns(ddl); err == nil {
			t.Fatal("ambiguous native DDL accepted", ddl)
		}
	}
}
