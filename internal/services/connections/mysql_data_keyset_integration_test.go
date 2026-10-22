package connections

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestMySQLKeysetIntegration84(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_STRUCTURE_IT") != "1" {
		t.Skip("requires opt-in disposable loopback MySQL 8.4")
	}
	m := NewMySQLSessionManager()
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := ActiveSettings{ProfileID: 9903, Host: "127.0.0.1", Port: 34306,
		Username: "nyadb_it_reader", MySQLAdvanced: DefaultMySQLAdvanced()}
	if err := m.Connect(ctx, cfg, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	opts := MySQLDataOptions{Limit: 1, UseKeyset: true, SortColumn: "price", SortDescending: true}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		p, err := m.TableDataPageWithOptions(ctx, cfg.ProfileID, "nyadb_struct_a", "child", opts)
		if err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
		if !p.KeysetAvailable || len(p.Rows) != 1 || p.NextCursor == nil {
			t.Fatalf("missing seek data at %d: %+v", i, p)
		}
		id := p.Rows[0][0].Text
		if seen[id] {
			t.Fatalf("repeated key %s", id)
		}
		seen[id] = true
		if i < 2 && !p.HasMore {
			t.Fatalf("unexpected page end %d", i)
		}
		if i == 2 && p.HasMore {
			t.Fatal("last page unexpectedly has more")
		}
		opts.After = p.NextCursor
		opts.Offset++
	}
	if len(seen) != 3 {
		t.Fatalf("lost rows: %+v", seen)
	}
	opts.After.Table = "other"
	if _, err := m.TableDataPageWithOptions(ctx, cfg.ProfileID, "nyadb_struct_a", "child", opts); !errors.Is(err, ErrMySQLDataPage) {
		t.Fatalf("cross-object cursor accepted: %v", err)
	}
	// Without a unique index the service must not silently use a made-up seek.
	plain, err := m.TableDataPageWithOptions(ctx, cfg.ProfileID, "nyadb_perf", "no_key_entries",
		MySQLDataOptions{UseKeyset: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if plain.KeysetAvailable || plain.NextCursor != nil || plain.StableOrder {
		t.Fatalf("no-key page falsely reports keyset: %+v", plain)
	}
}
