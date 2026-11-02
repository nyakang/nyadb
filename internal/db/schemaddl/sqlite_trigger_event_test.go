package schemaddl

import "testing"

func TestSQLiteTriggerEventIgnoresQuotedNamesAndBodyKeywords(t *testing.T) {
	for _, item := range []struct{ ddl, event string }{
		{`CREATE TRIGGER "INSERT" INSTEAD OF UPDATE OF "DELETE" ON v BEGIN INSERT INTO t VALUES('DELETE'); END`, "update"},
		{`CREATE TRIGGER IF NOT EXISTS main.tr DELETE ON t BEGIN UPDATE t SET value='INSERT'; END`, "delete"},
		{`CREATE TEMP TRIGGER [UPDATE] BEFORE INSERT ON t WHEN 1 BEGIN DELETE FROM t; END`, "insert"},
	} {
		got, err := SQLiteTriggerEvent(item.ddl)
		if err != nil || got != item.event {
			t.Fatalf("event %s: %s %v", item.ddl, got, err)
		}
	}
	if _, err := SQLiteTriggerEvent("CREATE VIEW v AS SELECT 'INSERT'"); err == nil {
		t.Fatal("view accepted as trigger")
	}
}
