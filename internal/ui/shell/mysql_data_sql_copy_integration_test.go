package shell

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// SQL clipboard output is executable SQL, so verify its identity and value
// semantics against the disposable server rather than only its spelling.
func TestMySQLDataSQLCopyIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_DATA_EDIT_IT") != "1" {
		t.Skip("requires explicit disposable MySQL editing fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_DATA_EDIT_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("NYADB_MYSQL_DATA_EDIT_PORT must be an explicit loopback fixture port")
	}
	config := mysqldriver.NewConfig()
	config.User = "data_probe"
	config.Passwd = os.Getenv("NYADB_MYSQL_DATA_EDIT_PASSWORD")
	config.Net, config.Addr = "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	config.DBName = os.Getenv("NYADB_MYSQL_DATA_EDIT_DATABASE")
	if config.DBName == "" {
		config.DBName = "table_edit_it"
	}
	config.Params = map[string]string{"charset": "utf8mb4"}
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	name := fmt.Sprintf("sql_copy_%d", time.Now().UnixNano())
	target := mysqlDataSQLIdentifier(config.DBName) + "." + mysqlDataSQLIdentifier(name)
	if _, err = db.ExecContext(ctx, "CREATE TABLE "+target+" (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, note LONGTEXT, amount DECIMAL(30,6), payload VARBINARY(80), derived DECIMAL(30,6) GENERATED ALWAYS AS (amount + 1) STORED) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec("DROP TABLE " + target); err != nil {
			t.Errorf("fixture cleanup failed: %v", err)
		}
	}()
	if _, err = db.ExecContext(ctx, "INSERT INTO "+target+" (id,note,amount) VALUES (9007199254740992,'neighbor',1),(9007199254740993,'target',2)"); err != nil {
		t.Fatal(err)
	}
	page := &mysqlsvc.MySQLDataPage{Database: config.DBName, Table: name,
		Columns:        []string{"id", "note", "amount", "payload", "derived"},
		ColumnKinds:    []string{"bigint", "longtext", "decimal", "varbinary", "bigint"},
		ColumnMetadata: []mysqlsvc.MySQLStructureColumn{{Name: "id", Key: "PRI"}, {Name: "note"}, {Name: "amount"}, {Name: "payload"}, {Name: "derived", Extra: "STORED GENERATED"}},
		Rows:           [][]mysqlsvc.MySQLDataCell{{{Text: "9007199254740993"}, {Text: "target"}, {Text: "2.000000"}, {Null: true}, {Text: "generated-preview", Truncated: true}}},
		RowRefs:        []*mysqlsvc.MySQLRowRef{{Database: config.DBName, Table: name, Columns: []string{"id"}, Values: [][]byte{[]byte("9007199254740993")}}},
	}
	doc := &tab{dataPage: page, dataSelectedRow: 0}
	doc.dataState.Cells.Anchor, doc.dataState.Cells.Cursor = "note", "note"
	long := strings.Repeat("中文🙂'\t\n", 600)
	draft := &mysqlRowDraft{Key: mysqlDataRowKey(doc, 0), Row: 0, Kind: "update", Values: map[string]mysqlsvc.MySQLDataValue{
		"note": {Text: long}, "amount": {Text: "9007199254740993.123456"},
	}}
	doc.dataEdit.Drafts = map[string]*mysqlRowDraft{draft.Key: draft}
	page.Rows[0][1] = mysqlDataValuePreview(draft.Values["note"])
	statement, err := mysqlDataSQLSelection(doc, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(statement, "SET `id`") || !strings.Contains(statement, "`amount` =") {
		t.Fatal("single-cell UPDATE export failed to include other writable fields or included the primary key")
	}
	result, err := db.ExecContext(ctx, statement)
	if err != nil {
		t.Fatalf("exported UPDATE failed: %v", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		t.Fatalf("UPDATE copied identity matched %d rows; expected exactly one", count)
	}
	var id, note, amount string
	var payload []byte
	if err = db.QueryRowContext(ctx, "SELECT id,note,amount,payload FROM "+target+" WHERE id=9007199254740993").Scan(&id, &note, &amount, &payload); err != nil {
		t.Fatal(err)
	}
	if id != "9007199254740993" || note != long || amount != "9007199254740993.123456" || payload != nil {
		t.Fatal("copied UPDATE changed exact numeric, complete text or NULL semantics")
	}
	if err = db.QueryRowContext(ctx, "SELECT note FROM "+target+" WHERE id=9007199254740992").Scan(&note); err != nil || note != "neighbor" {
		t.Fatalf("neighboring BIGINT record was overwritten: %q %v", note, err)
	}
	t.Log("UPDATE matched only the exact >2^53 key; complete Unicode text, DECIMAL scale and binary NULL survived")

	draft.Kind = "insert"
	draft.Values = map[string]mysqlsvc.MySQLDataValue{"note": {Text: ""}, "payload": {Binary: true, Bytes: []byte{0, 0xff, '\'', '\n'}}}
	statement, err = mysqlDataSQLSelection(doc, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err = db.ExecContext(ctx, statement)
	if err != nil {
		t.Fatalf("exported INSERT failed: %v", err)
	}
	inserted, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	var nullable sql.NullString
	if err = db.QueryRowContext(ctx, "SELECT note,amount,payload FROM "+target+" WHERE id=?", inserted).Scan(&note, &nullable, &payload); err != nil {
		t.Fatal(err)
	}
	if note != "" || nullable.Valid || string(payload) != string(draft.Values["payload"].Bytes) {
		t.Fatal("copied INSERT lost empty text, DEFAULT NULL or raw binary bytes")
	}
	t.Log("INSERT preserved DEFAULT/autoincrement, empty text and binary bytes; generated field excluded")
}
