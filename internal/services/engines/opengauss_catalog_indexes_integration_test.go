package engines

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestOpenGaussNativeCatalogContainsConstraintAndOrdinaryIndexRelationsFixture(t *testing.T) {
	_, db, target := openGaussTableFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "CREATE INDEX by_note ON "+qualified(target.Object)+"(note)"); err != nil {
		t.Fatal(err)
	}
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	catalog, err := openGaussReadDesignReference(ctx, c, target)
	if err != nil {
		t.Fatal(err)
	}
	relations := map[uint64]bool{}
	for _, entry := range catalog.Entries {
		if entry.Kind != "relation" {
			continue
		}
		var raw struct {
			Kind   string `json:"relkind"`
			Method uint64 `json:"relam"`
		}
		if json.Unmarshal(entry.Data, &raw) != nil {
			t.Fatal("native raw relation record")
		}
		if raw.Kind == "i" && raw.Method != 0 {
			relations[entry.OID] = true
		}
	}
	rootIndexes := 0
	for _, entry := range catalog.Entries {
		if entry.Kind != "index" {
			continue
		}
		var raw struct {
			Parent uint64 `json:"indrelid"`
		}
		if json.Unmarshal(entry.Data, &raw) != nil || !relations[entry.OID] {
			t.Fatal("pg_index exists without the complete native pg_class index relation", entry.OID)
		}
		if raw.Parent == catalog.RelationOID {
			rootIndexes++
		}
	}
	if rootIndexes != 2 {
		t.Fatal("native primary/ordinary index fixtures missing", rootIndexes)
	}
}
