package engines

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dictionary"
)

func TestSQLiteDictionaryPresentationIncludesIndexesExpressionsAndForeignKeys(t *testing.T) {
	m := NewManager()
	defer m.Close()
	if err := m.Connect(context.Background(), connections.Profile{ID: 5, Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "dictionary.sqlite")}, ""); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Session(5)
	run(t, s, `CREATE TABLE parent(a INTEGER,b INTEGER,PRIMARY KEY(a,b));
CREATE TABLE records(id INTEGER PRIMARY KEY, label TEXT DEFAULT '',a INTEGER,b INTEGER,FOREIGN KEY(a,b) REFERENCES parent(a,b) ON UPDATE CASCADE ON DELETE SET NULL);
CREATE INDEX idx_label ON records(label DESC);
CREATE INDEX idx_expr ON records(lower(label)) WHERE label IS NOT NULL;`)
	meta, err := s.TableMetadata(context.Background(), s.Namespace("main").Object("table", "records"))
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Indexes) != 2 || len(meta.ForeignKeys) != 1 || len(meta.ForeignKeys[0].Columns) != 2 || meta.ForeignKeys[0].OnDelete != "SET NULL" {
		t.Fatalf("missing catalog metadata: %+v", meta)
	}
	var expression bool
	for _, i := range meta.Indexes {
		if i.Name == "idx_expr" {
			expression = i.Type == "PARTIAL INDEX" && i.Columns[0].Name == "<expression>"
		}
		if i.Name == "idx_label" && i.Columns[0].Direction != "DESC" {
			t.Fatal("lost order")
		}
	}
	if !expression {
		t.Fatal("invented expression field")
	}
	d, err := dictionary.Build(context.Background(), dictionary.Reader{Browse: s.Browse, Metadata: s.TableMetadata}, s.Namespace("main"), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := dictionary.Render(context.Background(), d, "markdown", dictionary.EnglishLabels())
	if err != nil || !strings.Contains(string(b), "&#39;&#39;") || !strings.Contains(string(b), "CASCADE") {
		t.Fatal("dictionary lost actual defaults/actions", err)
	}
}

func TestPostgreSQL17DictionaryPresentationFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_dictionary;
CREATE TABLE nyadb_dictionary.parent(a integer,b integer,PRIMARY KEY(a,b));
CREATE TABLE nyadb_dictionary.records(id bigint PRIMARY KEY,label text DEFAULT '',a integer,b integer,CONSTRAINT pair_fk FOREIGN KEY(a,b) REFERENCES nyadb_dictionary.parent(a,b) ON UPDATE CASCADE ON DELETE SET NULL);
COMMENT ON TABLE nyadb_dictionary.records IS 'dictionary table';
COMMENT ON COLUMN nyadb_dictionary.records.label IS 'dictionary field';
CREATE INDEX idx_expr ON nyadb_dictionary.records(lower(label));
CREATE VIEW nyadb_dictionary.records_view AS SELECT label FROM nyadb_dictionary.records;`)
	meta, err := s.TableMetadata(context.Background(), s.Namespace("nyadb_dictionary").Object("table", "records"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Comment != "dictionary table" || meta.Columns[1].Comment != "dictionary field" || len(meta.Indexes) != 2 || len(meta.ForeignKeys) != 1 || len(meta.ForeignKeys[0].Columns) != 2 || meta.ForeignKeys[0].OnDelete != "SET NULL" {
		t.Fatalf("missing PG presentation: %+v", meta)
	}
	d, err := dictionary.Build(context.Background(), dictionary.Reader{Browse: s.Browse, Metadata: s.TableMetadata}, s.Namespace("nyadb_dictionary"), nil)
	if err != nil || len(d.Tables) != 3 {
		t.Fatal("PG dictionary", err)
	}
	data, err := dictionary.Render(context.Background(), d, "html", dictionary.EnglishLabels())
	if err != nil || !strings.Contains(string(data), "lower(label)") || !strings.Contains(string(data), "dictionary field") || !strings.Contains(string(data), "records_view") {
		t.Fatal("incomplete PG dictionary", err)
	}
}
