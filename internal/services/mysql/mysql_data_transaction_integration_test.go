package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// Opt in only against a disposable loopback fixture. Provision the dedicated
// nyadb_toolbar_tx account with ALL on nyadb_toolbar_tx_it and no other schemas.
// This test creates and drops its own uniquely named InnoDB table only.
func TestMySQLDataDocumentTransactionIntegration(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_DATA_TX_IT") != "1" {
		t.Skip("requires disposable loopback data-transaction fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MYSQL_DATA_TX_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("explicit disposable loopback port required")
	}
	const database = "nyadb_toolbar_tx_it"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m := NewMySQLSessionManager()
	defer m.Close()
	settings := connections.ActiveSettings{ProfileID: 2026103003, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_toolbar_tx", MySQLAdvanced: connections.DefaultMySQLAdvanced()}
	if err = m.Connect(ctx, settings, os.Getenv("NYADB_MYSQL_DATA_TX_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	db := m.sessions[settings.ProfileID].db
	table := fmt.Sprintf("document_tx_%d", time.Now().UnixNano())
	target, err := mysqlEditTable(database, table)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "CREATE TABLE "+target+" (id BIGINT PRIMARY KEY, note VARCHAR(64) NOT NULL, detail TEXT) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := db.ExecContext(cleanup, "DROP TABLE "+target); err != nil {
			t.Error(err)
		}
	}()
	if _, err = db.ExecContext(ctx, "INSERT INTO "+target+" VALUES(1,'before',REPEAT('长',3000)),(2,'other','detail')"); err != nil {
		t.Fatal(err)
	}
	page := func(token *MySQLDataTransaction) *MySQLDataPage {
		t.Helper()
		var p *MySQLDataPage
		var e error
		o := MySQLDataOptions{Limit: 20}
		if token == nil {
			p, e = m.TableDataPageWithOptions(ctx, settings.ProfileID, database, table, o)
		} else {
			p, e = m.TableDataPageInTransaction(ctx, token, settings.ProfileID, database, table, o)
		}
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	begin := func() *MySQLDataTransaction {
		t.Helper()
		v, e := m.BeginMySQLDataTransaction(ctx, settings.ProfileID, database, table)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(v.Close)
		return v
	}
	save := func(token *MySQLDataTransaction, snapshot *MySQLRowSnapshot, text string) *MySQLDataEditResult {
		t.Helper()
		v, e := m.ApplyMySQLDataEditsInTransaction(ctx, token, settings.ProfileID, database, table, []MySQLDataEdit{{Kind: "update", Original: snapshot, Values: map[string]MySQLDataValue{"note": {Text: text}}}})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	lease, err := m.ReserveSQLTabToken(settings.ProfileID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.ExecuteSQLTabToken(ctx, lease, database, "SET @data_transaction_isolation_marker = 42"); err != nil {
		t.Fatal(err)
	}
	token := begin()
	var marker *int
	if err = token.conn.QueryRowContext(ctx, "SELECT @data_transaction_isolation_marker").Scan(&marker); err != nil || marker != nil {
		t.Fatalf("SQL-tab variables escaped lease: %v %v", marker, err)
	}
	original := page(token).RowSnapshots[0]
	save(token, original, "pending")
	if page(token).Rows[0][1].Text != "pending" || page(nil).Rows[0][1].Text != "before" {
		t.Fatal("dedicated transaction isolation failed")
	}
	full, err := m.ReadMySQLDataRowInTransaction(ctx, token, settings.ProfileID, database, table, *page(token).RowRefs[0])
	if err != nil || full.Values["note"].Text != "pending" || len([]rune(full.Values["detail"].Text)) != 3000 {
		t.Fatalf("transaction row read: %v %v", full, err)
	}
	chunk, err := m.ReadMySQLFieldChunkInTransaction(ctx, token, settings.ProfileID, database, table, "note", *page(token).RowRefs[0], 0, 1024)
	if err != nil || chunk.Text != "pending" {
		t.Fatalf("transaction field chunk read: %v %v", chunk, err)
	}
	current := page(token).RowSnapshots[0]
	_, err = m.ApplyMySQLDataEditsInTransaction(ctx, token, settings.ProfileID, database, table, []MySQLDataEdit{{Kind: "update", Original: current, Values: map[string]MySQLDataValue{"note": {Text: "batch_pending"}}}, {Kind: "update", Original: original, Values: map[string]MySQLDataValue{"note": {Text: "conflict"}}}})
	if !errors.Is(err, ErrMySQLDataConflict) || page(token).Rows[0][1].Text != "pending" {
		t.Fatalf("failed batch did not restore savepoint: %v", err)
	}
	if err = m.RollbackMySQLDataTransaction(ctx, token); err != nil {
		t.Fatal(err)
	}
	if page(nil).Rows[0][1].Text != "before" {
		t.Fatal("rollback leaked writes")
	}
	token = begin()
	save(token, page(token).RowSnapshots[0], "committed")
	if err = m.CommitMySQLDataTransaction(ctx, token); err != nil {
		t.Fatal(err)
	}
	if page(nil).Rows[0][1].Text != "committed" {
		t.Fatal("commit not visible")
	}
	token = begin()
	save(token, page(token).RowSnapshots[0], "abandoned")
	token.Close()
	if page(nil).Rows[0][1].Text != "committed" {
		t.Fatal("close did not roll back")
	}
	token = begin()
	beforeCancel := page(token)
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var locked string
	if err = blocker.QueryRowContext(ctx, "SELECT note FROM "+target+" WHERE id=2 FOR UPDATE").Scan(&locked); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
	}
	blockedCtx, stopBlocked := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err = m.ApplyMySQLDataEditsInTransaction(blockedCtx, token, settings.ProfileID, database, table, []MySQLDataEdit{
		{Kind: "update", Original: beforeCancel.RowSnapshots[0], Values: map[string]MySQLDataValue{"note": {Text: "cancel_pending"}}},
		{Kind: "update", Original: beforeCancel.RowSnapshots[1], Values: map[string]MySQLDataValue{"note": {Text: "cancel_blocked"}}},
	})
	stopBlocked()
	_ = blocker.Rollback()
	if !errors.Is(err, ErrMySQLDataTransactionLost) {
		t.Fatalf("canceled save did not revoke its physical transaction: %v", err)
	}
	if page(nil).Rows[0][1].Text != "committed" {
		t.Fatal("canceled batch leaked its first pending write")
	}
	if _, err = m.TableDataPageInTransaction(ctx, token, settings.ProfileID, database, table, MySQLDataOptions{Limit: 20}); !errors.Is(err, ErrMySQLDataTransactionLost) {
		t.Fatal("canceled physical transaction was replaced")
	}
	token = begin()
	save(token, page(token).RowSnapshots[0], "disconnect_pending")
	if err = m.Disconnect(settings.ProfileID); err != nil {
		t.Fatal(err)
	}
	token.Close()
	if err = m.Connect(ctx, settings, os.Getenv("NYADB_MYSQL_DATA_TX_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	db = m.sessions[settings.ProfileID].db
	if page(nil).Rows[0][1].Text != "committed" {
		t.Fatal("disconnect did not roll back")
	}
	if _, err = m.TableDataPageInTransaction(ctx, token, settings.ProfileID, database, table, MySQLDataOptions{Limit: 20}); !errors.Is(err, ErrMySQLDataTransactionLost) {
		t.Fatalf("old transaction token revived: %v", err)
	}
}
