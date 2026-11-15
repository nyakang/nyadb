package schemaddl

import "testing"

func TestSQLiteRowIDColumnPreservesNativePrimaryKeyExceptions(t *testing.T) {
	for _, c := range []struct{ ddl, want string }{
		{`CREATE TABLE t(id INTEGER PRIMARY KEY,v TEXT UNIQUE)`, "id"},
		{`CREATE TABLE t(id INTEGER PRIMARY KEY DESC,v TEXT)`, ""},
		{`CREATE TABLE t(id INTEGER,v TEXT,CONSTRAINT pk PRIMARY KEY(id DESC))`, "id"},
		{`CREATE TABLE t(id INTEGER,v TEXT,PRIMARY KEY(id)) WITHOUT ROWID`, ""},
		{`CREATE TABLE t(id INTEGER,v INTEGER,PRIMARY KEY(id,v))`, ""},
		{`CREATE TABLE t(id INT PRIMARY KEY,v TEXT)`, ""},
		{`CREATE TABLE t(id INTEGER /* retained */ PRIMARY KEY,v TEXT)`, "id"},
		{`CREATE TABLE t(id INTEGER DEFAULT 'PRIMARY KEY',v TEXT)`, ""},
	} {
		table, err := ParseSQLite(c.ddl)
		if err != nil {
			t.Fatal(err)
		}
		if got := SQLiteRowIDColumn(table); got != c.want {
			t.Fatalf("%s: %q != %q", c.ddl, got, c.want)
		}
	}
}
