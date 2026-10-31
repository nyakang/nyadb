package mysql

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNontransactionalRestoreCoreFixture(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_CORE_IT") != "1" {
		t.Skip("requires explicit disposable loopback core fixture")
	}
	port, e := strconv.ParseUint(os.Getenv("NYADB_MYSQL_CORE_PORT"), 10, 16)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := NewMySQLSessionManager()
	defer m.Close()
	if e = m.Connect(ctx, connections.ActiveSettings{ProfileID: 922, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_core", Database: "nyadb_core_it", MySQLAdvanced: connections.DefaultMySQLAdvanced()}, ""); e != nil {
		t.Fatal(e)
	}
	s, qctx, stop, e := m.requestContext(ctx, 922, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	c, e := s.db.Conn(qctx)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	for _, stmt := range []string{"CREATE TABLE nyadb_core_it.restore_nontransactional_it (id BIGINT UNSIGNED PRIMARY KEY,amount DECIMAL(65,30),payload BLOB,empty_payload BLOB,note TEXT) ENGINE=MyISAM", "CREATE TABLE nyadb_core_it.restore_nontransactional_warning_it (note VARCHAR(2)) ENGINE=MyISAM"} {
		if _, e = c.ExecContext(qctx, stmt); e != nil {
			t.Fatal(e)
		}
	}
	defer c.ExecContext(context.Background(), "DROP TABLE nyadb_core_it.restore_nontransactional_it,nyadb_core_it.restore_nontransactional_warning_it")
	const precise = "12345678901234567890123456789012345.123456789012345678901234567890"
	rows := &nontransactionalRows{edits: []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"id": {Text: "18446744073709551615"}, "amount": {Text: precise}, "payload": {Binary: true, Bytes: []byte{0, 255, 0}}, "empty_payload": {Binary: true, Bytes: []byte{}}, "note": {Null: true}}}}}
	out := restoreNontransactionalData(qctx, c, "nyadb_core_it", "restore_nontransactional_it", rows, 1, nil)
	if out.Error != nil || out.State != "executed" || out.RowsApplied != 1 {
		t.Fatalf("MyISAM typed restore failed: %+v", out)
	}
	var id, amount, hexPayload string
	var length int
	var isNull int
	if e = c.QueryRowContext(qctx, "SELECT CAST(id AS CHAR),CAST(amount AS CHAR),HEX(payload),OCTET_LENGTH(empty_payload),note IS NULL FROM nyadb_core_it.restore_nontransactional_it").Scan(&id, &amount, &hexPayload, &length, &isNull); e != nil || id != "18446744073709551615" || amount != precise || hexPayload != "00FF00" || length != 0 || isNull != 1 {
		t.Fatalf("exact MyISAM values were lost: %s %s %s %d %d %v", id, amount, hexPayload, length, isNull, e)
	}
	if _, e = c.ExecContext(qctx, "SET SESSION sql_mode=''"); e != nil {
		t.Fatal(e)
	}
	warningRows := &nontransactionalRows{edits: []MySQLDataEdit{{Kind: "insert", Values: map[string]MySQLDataValue{"note": {Text: "too long"}}}}}
	warning := restoreNontransactionalData(qctx, c, "nyadb_core_it", "restore_nontransactional_warning_it", warningRows, 1, nil)
	if warning.State != "partial" || warning.RowState != "executed" || warning.RowsApplied != 1 || warning.Error == nil {
		t.Fatalf("nontransactional truncation falsely claimed rollback or success: %+v", warning)
	}
	var stored string
	if e = c.QueryRowContext(qctx, "SELECT note FROM nyadb_core_it.restore_nontransactional_warning_it").Scan(&stored); e != nil || stored != "to" {
		t.Fatalf("written warning row was not retained: %q %v", stored, e)
	}
}
