package schemaddl

import (
	"strings"
	"testing"
)

func TestSQLiteStructuredFieldsPreserveUnsupportedDefinitions(t *testing.T) {
	for _, body := range []string{"INTEGER PRIMARY KEY DESC", "TEXT REFERENCES parent(id)", "TEXT CHECK (length(value)>0)", "TEXT GENERATED ALWAYS AS (other || 'x') STORED", "INT NOT NULL ON CONFLICT FAIL", "TEXT /* retained */ NOT NULL"} {
		f, err := ParseSQLiteField(Clause{Name: "value", Kind: "column", SQL: body})
		if err != nil || !f.Unsupported || f.Definition != body {
			t.Fatalf("lost unknown attributes: %+v %v", f, err)
		}
		if _, err = f.SQL(); err == nil {
			t.Fatalf("silently dropped attributes: %s", body)
		}
	}
}
func TestSQLiteStructuredFieldAttributesAndDefaults(t *testing.T) {
	for _, body := range []string{"INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL", "TEXT DEFAULT 'end\\' COLLATE NOCASE UNIQUE", "NUMERIC(18,4) DEFAULT (-1.25)", "BLOB DEFAULT X'00ff'", "TEXT DEFAULT NULL"} {
		f, err := ParseSQLiteField(Clause{Name: "value", Kind: "column", SQL: body})
		if err != nil || f.Unsupported {
			t.Fatalf("common form: %+v %v", f, err)
		}
		sql, err := f.SQL()
		if err != nil {
			t.Fatal(err)
		}
		reparsed, err := ParseSQLiteField(Clause{Name: "value", Kind: "column", SQL: sql})
		if err != nil || reparsed.Unsupported {
			t.Fatalf("render: %s %v", sql, err)
		}
		f.Definition, reparsed.Definition = "", ""
		if f != reparsed {
			t.Fatalf("attributes changed: %+v => %+v", f, reparsed)
		}
	}
	f := SQLiteField{Name: "id", Type: "BIGINT", Primary: true, AutoIncrement: true}
	if _, err := f.SQL(); err == nil {
		t.Fatal("unsafe autoincrement type")
	}
	f = SQLiteField{Name: "value", Type: "TEXT", HasDefault: true, Default: "1); DROP TABLE t; --"}
	if _, err := f.SQL(); err == nil {
		t.Fatal("default fragment escaped")
	}
	f.Default = "(1) + (2)"
	if _, err := f.SQL(); err == nil {
		t.Fatal("unwrapped default expression")
	}
	f.Default = "NULL"
	f.Type = "TEXT UNIQUE"
	if _, err := f.SQL(); err == nil {
		t.Fatal("type embedded an attribute")
	}
	f.Type = "TEXT"
	f.Collation = `custom"collation`
	sql, err := f.SQL()
	if err != nil || !strings.Contains(sql, `"custom""collation"`) {
		t.Fatalf("collation quoting: %s %v", sql, err)
	}
}
