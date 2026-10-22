package connections

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// This test is opt-in and is deliberately hard-coded to a disposable localhost
// port. Never use a production endpoint. The fixture must be initialized in the
// isolated mysql:8.4 container first (see docs). No DDL is executed by NyaDB.
func TestMySQLStructureIntegrationMySQL84(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_STRUCTURE_IT") != "1" {
		t.Skip("requires explicitly provisioned disposable MySQL 8.4 fixture on 127.0.0.1:34306")
	}
	password := mysqlIntegrationPassword(t)

	m := NewMySQLSessionManager()
	t.Cleanup(func() { _ = m.Close() })
	settings := ActiveSettings{
		ProfileID: 84306, ConnectionName: "isolated-structure-fixture",
		Host: "127.0.0.1", Port: 34306, Username: "nyadb_it_reader",
		MySQLAdvanced: DefaultMySQLAdvanced(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.Connect(ctx, settings, password); err != nil {
		t.Fatalf("connect to disposable loopback fixture: %v", err)
	}
	schemas, err := m.Databases(ctx, settings.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nyadb_struct_a", "nyadb_struct_b"} {
		if !slices.Contains(schemas, name) {
			t.Fatalf("missing schema %s: %v", name, schemas)
		}
	}
	objects, err := m.Objects(ctx, settings.ProfileID, "nyadb_struct_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 3 {
		t.Fatalf("unexpected object count: %+v", objects)
	}
	var viewFound bool
	for _, o := range objects {
		if o.Name == "parent_view" && o.Kind == "视图" {
			viewFound = true
		}
	}
	if !viewFound {
		t.Fatalf("view not found: %+v", objects)
	}

	child, err := m.TableStructure(ctx, settings.ProfileID, "nyadb_struct_a", "child")
	if err != nil {
		t.Fatal(err)
	}
	if child.Kind != "BASE TABLE" || child.Engine != "InnoDB" || child.Comment != "NyaDB fixture child" {
		t.Fatalf("table metadata: %+v", child)
	}
	var columns = map[string]MySQLStructureColumn{}
	for _, col := range child.Columns {
		columns[col.Name] = col
	}
	if len(columns) != 8 {
		t.Fatalf("expected 8 fixture columns, got %+v", child.Columns)
	}
	id := columns["id"]
	if id.Nullable || id.Key != "PRI" || id.Extra != "auto_increment" || id.Default != nil || id.Type != "bigint unsigned" {
		t.Fatalf("identity column: %+v", id)
	}
	price := columns["price"]
	if price.Type != "decimal(10,2)" || price.NumericPrecision == nil || *price.NumericPrecision != 10 ||
		price.NumericScale == nil || *price.NumericScale != 2 || price.Default == nil || *price.Default != "0.00" {
		t.Fatalf("decimal column: %+v", price)
	}
	if columns["nullable_note"].Default != nil || !columns["nullable_note"].Nullable {
		t.Fatalf("nullable default not preserved: %+v", columns["nullable_note"])
	}
	blank := columns["blank_note"]
	if blank.Default == nil || *blank.Default != "" || blank.CharacterMaxLength == nil || *blank.CharacterMaxLength != 32 {
		t.Fatalf("empty string default or size not preserved: %+v", blank)
	}
	if columns["remark"].Comment != "仅供隔离测试" {
		t.Fatalf("unicode comment lost: %+v", columns["remark"])
	}
	if len(child.ForeignKeys) != 1 {
		t.Fatalf("foreign keys: %+v", child.ForeignKeys)
	}
	fk := child.ForeignKeys[0]
	if fk.Name != "fk_child_parent" || fk.OnUpdate != "CASCADE" || fk.OnDelete != "RESTRICT" ||
		fk.ReferencedDatabase != "nyadb_struct_a" || fk.ReferencedTable != "parent" ||
		len(fk.Columns) != 2 || fk.Columns[0].Name != "tenant_id" || fk.Columns[1].ReferencedName != "code" {
		t.Fatalf("composite foreign key: %+v", fk)
	}
	var uniqueFound, prefixFound bool
	for _, idx := range child.Indexes {
		switch idx.Name {
		case "uk_tenant_code":
			uniqueFound = idx.Unique && len(idx.Columns) == 2 &&
				idx.Columns[0].Position == 1 && idx.Columns[1].Position == 2
		case "ix_note_prefix":
			prefixFound = len(idx.Columns) == 1 && idx.Columns[0].SubPart != nil && *idx.Columns[0].SubPart == 12
		}
	}
	if !uniqueFound || !prefixFound {
		t.Fatalf("composite/prefix indexes: %+v", child.Indexes)
	}

	view, err := m.TableStructure(ctx, settings.ProfileID, "nyadb_struct_a", "parent_view")
	if err != nil {
		t.Fatal(err)
	}
	if view.Kind != "VIEW" || len(view.Columns) != 3 {
		t.Fatalf("view columns: %+v", view)
	}

	other, err := m.TableStructure(ctx, settings.ProfileID, "nyadb_struct_b", "child")
	if err != nil {
		t.Fatal(err)
	}
	if other.Database != "nyadb_struct_b" || len(other.Columns) != 2 || other.Columns[0].Type != "int" {
		t.Fatalf("same-name table improperly cached or qualified: %+v", other)
	}

	if _, err := m.TableStructure(ctx, settings.ProfileID, "nyadb_struct_a", "not_a_table"); !errors.Is(err, ErrMySQLTableNotFound) {
		t.Fatalf("missing table must be explicit: %v", err)
	}
	canceled, abort := context.WithCancel(ctx)
	abort()
	if _, err := m.TableStructure(canceled, settings.ProfileID, "nyadb_struct_a", "child"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel was ignored: %v", err)
	}
	if err := m.Disconnect(settings.ProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.TableStructure(ctx, settings.ProfileID, "nyadb_struct_a", "child"); !errors.Is(err, ErrMySQLNotConnected) {
		t.Fatalf("disconnected session accessible: %v", err)
	}
}

// mysqlIntegrationPassword accepts an explicitly supplied password or a private
// file path. The file option keeps credentials out of commands and test logs.
// Both callers require NYADB_MYSQL_STRUCTURE_IT=1 before reaching this helper.
func mysqlIntegrationPassword(t *testing.T) string {
	t.Helper()
	value := os.Getenv("NYADB_MYSQL_IT_PASSWORD")
	if value != "" {
		return value
	}
	file := os.Getenv("NYADB_MYSQL_IT_PASSWORD_FILE")
	if file == "" {
		t.Fatal("explicit test credential is required via NYADB_MYSQL_IT_PASSWORD or NYADB_MYSQL_IT_PASSWORD_FILE")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read isolated test credential: %v", err)
	}
	value = strings.TrimSpace(string(data))
	if value == "" {
		t.Fatal("isolated test credential is empty")
	}
	return value
}
