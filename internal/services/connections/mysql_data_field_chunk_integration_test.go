package connections

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestMySQLFieldChunkIntegration84(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_STRUCTURE_IT") != "1" {
		t.Skip("requires disposable loopback mysql 8.4")
	}
	m := NewMySQLSessionManager()
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cfg := ActiveSettings{ProfileID: 9904, Host: "127.0.0.1", Port: 34306,
		Username: "nyadb_it_reader", MySQLAdvanced: DefaultMySQLAdvanced()}
	if err := m.Connect(ctx, cfg, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	page, err := m.TableDataPageWithOptions(ctx, cfg.ProfileID, "nyadb_perf", "blob_entries", MySQLDataOptions{UseKeyset: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.RowRefs) != 2 || page.RowRefs[0] == nil {
		t.Fatalf("missing stable row refs: %+v", page)
	}
	ref := *page.RowRefs[0]
	a, err := m.ReadMySQLFieldChunk(ctx, cfg.ProfileID, "nyadb_perf", "blob_entries", "payload", ref, 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Binary || a.NextOffset != 64 || !a.HasMore || a.TotalUnits != 1<<20 || len(a.Text) != 130 {
		t.Fatalf("binary chunk: %+v", a)
	}
	b, err := m.ReadMySQLFieldChunk(ctx, cfg.ProfileID, "nyadb_perf", "blob_entries", "description", ref, 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	if b.Binary || b.NextOffset != 64 || !b.HasMore || len([]rune(b.Text)) != 64 || b.TotalUnits != 400000 {
		t.Fatalf("unicode chunk: %+v", b)
	}
	next, err := m.ReadMySQLFieldChunk(ctx, cfg.ProfileID, "nyadb_perf", "blob_entries", "description", ref, b.NextOffset, 64)
	if err != nil || next.NextOffset != 128 {
		t.Fatalf("unicode continuation: %+v %v", next, err)
	}
	ref.Table = "another_table"
	if _, err := m.ReadMySQLFieldChunk(ctx, cfg.ProfileID, "nyadb_perf", "blob_entries", "payload", ref, 0, 64); !errors.Is(err, ErrMySQLDataPage) {
		t.Fatalf("cross-table row reference accepted: %v", err)
	}
}
