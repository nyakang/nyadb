package localdb

import (
	"context"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestConnectionNotesRoundTripAndDuplicate(t *testing.T) {
	store, _, _ := openStore(t)
	ctx := context.Background()
	const notes = "部署说明：开发环境\n第二行 with emoji 🐈\nSELECT 1;\n"
	for _, profile := range []connections.Profile{
		{Name: "mysql notes", Kind: connections.MySQL, Host: "localhost", Port: 3306, Notes: notes},
		{Name: "sqlite notes", Kind: connections.SQLite, Database: "/tmp/notes.db", Notes: notes},
	} {
		p, err := store.Create(ctx, profile)
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(ctx, p.ID)
		if err != nil || got.Notes != notes {
			t.Fatalf("notes not restored for %s: %q (%v)", p.Kind, got.Notes, err)
		}
		got.Notes += "\n追加一行"
		saved, err := store.Update(ctx, got)
		if err != nil || saved.Notes != got.Notes {
			t.Fatalf("updated notes lost: %q (%v)", saved.Notes, err)
		}
		dup, err := connections.NewService(store).Duplicate(ctx, got.ID, "copy-"+got.Name)
		if err != nil || dup.Notes != got.Notes {
			t.Fatalf("duplicate lost notes: %q (%v)", dup.Notes, err)
		}
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 4 {
		t.Fatalf("note profiles missing from list: %+v %v", list, err)
	}
	for _, p := range list {
		if !strings.Contains(p.Notes, "部署说明") {
			t.Fatalf("notes missing on list item %s", p.Name)
		}
	}
}
