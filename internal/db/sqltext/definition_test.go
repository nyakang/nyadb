package sqltext

import "testing"

func TestSinglePublicDefinitionKeepsCompoundBodiesAndRejectsTrailingSQL(t *testing.T) {
	for _, raw := range []string{"CREATE PROCEDURE p() BEGIN IF 1 THEN SELECT 1; END IF; SELECT CASE WHEN 1 THEN 2 END; END", "CREATE FUNCTION f() RETURNS INT RETURN IF(1,2,3)", "CREATE TRIGGER tr BEFORE INSERT ON t FOR EACH ROW BEGIN SET NEW.v=1; SET NEW.x=2; END"} {
		if e := SingleDefinition(raw); e != nil {
			t.Fatal(raw, e)
		}
	}
	for _, raw := range []string{"CREATE PROCEDURE p() BEGIN SELECT 1; END; DROP TABLE x", "CREATE TRIGGER tr BEFORE INSERT ON t FOR EACH ROW SET NEW.v=1; DROP TABLE x", "CREATE VIEW v AS SELECT 1; DELETE FROM t"} {
		if e := SingleDefinition(raw); e == nil {
			t.Fatal("multiple definitions accepted", raw)
		}
	}
}
