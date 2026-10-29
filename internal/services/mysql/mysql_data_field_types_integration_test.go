package mysql

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// Opt-in only; the test can connect exclusively to the disposable loopback
// MySQL 8.4 container provisioned with mysql_field_types_fixture.sql.
func TestMySQLEnumJSONIntegration84(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_TYPES_IT") != "1" {
		t.Skip("requires disposable mysql:8.4 field-types fixture on 127.0.0.1:34306")
	}
	m := NewMySQLSessionManager()
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const id int64 = 84309
	settings := connections.ActiveSettings{ProfileID: id, Host: "127.0.0.1", Port: 34306,
		Username: "nyadb_it_reader", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	if err := m.Connect(ctx, settings, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	page, err := m.TableDataPageWithOptions(ctx, id, "nyadb_types", "field_types",
		MySQLDataOptions{UseKeyset: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.ColumnMetadata) != 4 || len(page.Rows) != 2 || !page.KeysetAvailable || !page.HasMore {
		t.Fatalf("bounded MySQL 8.4 ENUM/JSON page: %+v", page)
	}
	status, attrs := page.ColumnMetadata[1], page.ColumnMetadata[2]
	if status.DataType != "enum" || status.Type != "enum('active','disabled','待审核')" ||
		status.Default == nil || *status.Default != "active" || status.Comment != "状态枚举" ||
		attrs.DataType != "json" || attrs.Type != "json" {
		t.Fatalf("server declarations/attributes: %+v", page.ColumnMetadata)
	}
	if page.Rows[0][1].Text != "active" || !strings.Contains(page.Rows[0][2].Text, "\"tag\"") ||
		page.Rows[1][1].Text != "disabled" || !page.Rows[1][2].Truncated ||
		len(page.Rows[1][2].Text) > mysqlDataCellMaxBytes {
		t.Fatalf("ENUM and bounded JSON projections: %+v", page.Rows)
	}
	if page.RowRefs[1] == nil {
		t.Fatal("missing unique-key row reference")
	}
	chunk, err := m.ReadMySQLFieldChunk(ctx, id, "nyadb_types", "field_types",
		"attributes", *page.RowRefs[1], 0, 64)
	if err != nil || chunk.Binary || !chunk.HasMore || chunk.NextOffset != 64 {
		t.Fatalf("JSON explicit bounded chunk: %+v %v", chunk, err)
	}
	next, err := m.TableDataPageWithOptions(ctx, id, "nyadb_types", "field_types",
		MySQLDataOptions{UseKeyset: true, Limit: 2, Offset: 2, After: page.NextCursor})
	if err != nil || len(next.Rows) != 1 || next.Rows[0][1].Text != "待审核" ||
		!next.Rows[0][2].Null || next.HasMore {
		t.Fatalf("ENUM UTF-8/JSON SQL NULL on next page: %+v %v", next, err)
	}
	filtered, err := m.TableDataPageWithOptions(ctx, id, "nyadb_types", "field_types",
		MySQLDataOptions{Limit: 10, Filters: []MySQLDataFilterRule{{Column: "status", Mode: "equal", Value: "active"}}})
	if err != nil || len(filtered.Rows) != 1 || filtered.Rows[0][0].Text != "1" {
		t.Fatalf("bound ENUM filter: %+v %v", filtered, err)
	}
	if _, err := m.TableDataPageWithOptions(ctx, id, "nyadb_types", "field_types",
		MySQLDataOptions{Limit: 2, SortColumn: "attributes"}); !errors.Is(err, ErrMySQLDataPage) {
		t.Fatalf("JSON sort should be prohibited: %v", err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := m.TableDataPage(canceled, id, "nyadb_types", "field_types", 0, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("metadata query must observe canceled context: %v", err)
	}
}
