package sqltext

import "testing"

func TestSingleMySQLExplainStatement(t *testing.T) {
	for _, source := range []string{"SELECT 'a;b'; -- tail", "SELECT 1 /* ; */", "SELECT `a;b` FROM t", "SELECT 'NULL'"} {
		if err := SingleMySQLStatement(source); err != nil {
			t.Fatalf("%q: %v", source, err)
		}
	}
	for _, source := range []string{"SELECT 1; DELETE FROM t", "SELECT 'a\\'; DELETE FROM t; -- '", "DELIMITER $$\nSELECT 1; DELETE FROM t$$", "SELECT 1 /*! ; DELETE FROM t */", "SELECT 'broken", "-- comment"} {
		if err := SingleMySQLStatement(source); err == nil {
			t.Fatalf("accepted %q", source)
		}
	}
}

func TestSinglePortableExplainStatement(t *testing.T) {
	for _, source := range []string{"SELECT $q$a;b$q$;", `SELECT E'a\';b';`, "SELECT 1 /* nested /* ; */ comment */"} {
		if err := SinglePortableStatement(source); err != nil {
			t.Fatalf("%q: %v", source, err)
		}
	}
	if err := SinglePortableStatement("SELECT $$a;b$$; DELETE FROM t"); err == nil {
		t.Fatal("accepted second statement")
	}
}
