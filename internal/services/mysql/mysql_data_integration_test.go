package mysql

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// Opt-in only: the code never creates a connection to an arbitrary address.
func TestMySQLTableDataIntegration84(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_STRUCTURE_IT") != "1" {
		t.Skip("requires isolated mysql:8.4 fixture on 127.0.0.1:34306")
	}
	m := NewMySQLSessionManager()
	defer m.Close()
	settings := connections.ActiveSettings{
		ProfileID: 84307, Host: "127.0.0.1", Port: 34306,
		Username: "nyadb_it_reader", MySQLAdvanced: connections.DefaultMySQLAdvanced(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.Connect(ctx, settings, mysqlIntegrationPassword(t)); err != nil {
		t.Fatal(err)
	}
	first, err := m.TableDataPage(ctx, settings.ProfileID, "nyadb_struct_a", "child", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !first.StableOrder || !first.HasMore || len(first.Rows) != 2 || len(first.Columns) != 8 {
		t.Fatalf("first bounded page: %+v", first)
	}
	if len(first.ColumnMetadata) != len(first.Columns) ||
		first.ColumnMetadata[0].Type != "bigint unsigned" ||
		first.ColumnMetadata[0].Key != "PRI" ||
		first.ColumnMetadata[0].Extra != "auto_increment" ||
		first.ColumnMetadata[2].Type != "varchar(12)" ||
		first.ColumnMetadata[3].Type != "decimal(10,2)" ||
		first.ColumnMetadata[3].NumericPrecision == nil ||
		*first.ColumnMetadata[3].NumericPrecision != 10 ||
		first.ColumnMetadata[3].NumericScale == nil ||
		*first.ColumnMetadata[3].NumericScale != 2 ||
		first.ColumnMetadata[4].Default != nil ||
		first.ColumnMetadata[5].Default == nil ||
		*first.ColumnMetadata[5].Default != "" ||
		first.ColumnMetadata[2].CharacterSet == nil ||
		*first.ColumnMetadata[2].CharacterSet != "utf8mb4" ||
		first.ColumnMetadata[6].DatetimePrecision == nil ||
		first.ColumnMetadata[7].Comment != "仅供隔离测试" {
		t.Fatalf("live MySQL 8.4 field declarations and attributes lost: %+v", first.ColumnMetadata)
	}
	if !first.Rows[0][4].Null || first.Rows[0][5].Null || first.Rows[0][5].Text != "" || first.Rows[0][7].Text != "中文备注" {
		t.Fatalf("real row NULL/empty/unicode: %+v", first.Rows[0])
	}
	last, err := m.TableDataPage(ctx, settings.ProfileID, "nyadb_struct_a", "child", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Rows) != 1 || last.HasMore || last.Rows[0][0].Text != "3" || last.Rows[0][4].Text != "NULL" {
		t.Fatalf("second page: %+v", last)
	}
	view, err := m.TableDataPage(ctx, settings.ProfileID, "nyadb_struct_a", "parent_view", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if view.StableOrder || len(view.Rows) != 3 || len(view.Columns) != 3 {
		t.Fatalf("view paging: %+v", view)
	}
	other, err := m.TableDataPage(ctx, settings.ProfileID, "nyadb_struct_b", "child", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !other.StableOrder || len(other.Rows) != 1 || other.Rows[0][1].Text != "other schema" {
		t.Fatalf("cross-schema page: %+v", other)
	}
	canceled, abort := context.WithCancel(ctx)
	abort()
	if _, err := m.TableDataPage(canceled, settings.ProfileID, "nyadb_struct_a", "child", 0, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if err := m.Disconnect(settings.ProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.TableDataPage(ctx, settings.ProfileID, "nyadb_struct_a", "child", 0, 2); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatal(err)
	}
}
