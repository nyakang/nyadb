package mysql

import (
	"context"
	"fmt"
	"github.com/nyakang/nyadb/internal/services/backups"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/queries"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMySQLBackupRestoreCoreFixture(t *testing.T) {
	if os.Getenv("NYADB_MYSQL_CORE_IT") != "1" {
		t.Skip("requires disposable core fixture")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_MYSQL_CORE_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("invalid fixture port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	m := NewMySQLSessionManager()
	defer m.Close()
	if e = m.Connect(ctx, connections.ActiveSettings{ProfileID: 903, Host: "127.0.0.1", Port: uint16(port), Username: "nyadb_core", Database: "nyadb_core_it", MySQLAdvanced: connections.DefaultMySQLAdvanced()}, ""); e != nil {
		t.Fatal(e)
	}
	token, e := m.ReserveSQLTabToken(903, 45)
	if e != nil {
		t.Fatal(e)
	}
	defer m.ReleaseSQLTabToken(token)
	script := "CREATE TABLE backup_parent (`id` int PRIMARY KEY, `amount` decimal(40,20)); CREATE TABLE backup_child (`id` int PRIMARY KEY, `parent` int, `body` longtext, `bytes` longblob, `nullable` text NULL, `flags` BIT(8), `geom` POINT SRID 4326, `generated` int GENERATED ALWAYS AS (`id`+1) STORED, CONSTRAINT `backup_fk` FOREIGN KEY (`parent`) REFERENCES `backup_parent` (`id`)); INSERT INTO backup_parent VALUES(1,9007199254740993.12345678901234567890); INSERT INTO backup_child (`id`,`parent`,`body`,`bytes`,`nullable`,`flags`,`geom`) VALUES(1,1,'NULL',X'',NULL,b'1',ST_GeomFromText('POINT(1 2)',4326)),(2,1,'',X'00ff','NULL',b'0',NULL),(3,1,REPEAT('x',1100000),X'01','',b'11111111',ST_GeomFromText('POINT(3 4)',4326)); CREATE TRIGGER backup_trigger BEFORE INSERT ON backup_child FOR EACH ROW SET NEW.parent=1;"
	if _, e = m.ExecuteSQLTabTokenFile(ctx, token, "nyadb_core_it", strings.NewReader(script)); e != nil {
		t.Fatal(e)
	}
	geometryBefore, e := m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "SELECT HEX(flags),HEX(geom) FROM backup_child WHERE id=1")
	if e != nil {
		t.Fatal(e)
	}
	generation := m.SessionGeneration(903)
	path := filepath.Join(t.TempDir(), "backup.nyabackup")
	e = queries.WriteStream(path, func(w io.Writer) error {
		_, e := m.BackupMySQL(ctx, 903, BackupRequest{Database: "nyadb_core_it", Generation: generation, Data: true, Tables: []string{"backup_parent", "backup_child"}}, w)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	reader, e := backups.Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	if _, e = m.PlanRestore(ctx, 903, generation, "nyadb_core_it", "stop", reader); e == nil {
		t.Fatal("conflict policy did not stop")
	}
	if _, e = m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "CREATE TABLE backup_outside(id int PRIMARY KEY, parent int, FOREIGN KEY(parent) REFERENCES backup_parent(id))"); e != nil {
		t.Fatal(e)
	}
	if _, e = m.PlanRestore(ctx, 903, generation, "nyadb_core_it", "replace", reader); e == nil {
		t.Fatal("external incoming foreign key was ignored")
	}
	if _, e = m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "DROP TABLE backup_outside; DROP TRIGGER backup_trigger; CREATE TABLE backup_other(id int); CREATE TRIGGER backup_trigger BEFORE INSERT ON backup_other FOR EACH ROW SET NEW.id=1;"); e != nil {
		t.Fatal(e)
	}
	if _, e = m.PlanRestore(ctx, 903, generation, "nyadb_core_it", "replace", reader); e == nil {
		t.Fatal("trigger name conflict on another target was ignored")
	}
	if _, e = m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "DROP TRIGGER backup_trigger; DROP TABLE backup_other;"); e != nil {
		t.Fatal(e)
	}
	if _, e = m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "UPDATE backup_child SET body='changed' WHERE id=1"); e != nil {
		t.Fatal(e)
	}
	plan, e := m.PlanRestore(ctx, 903, generation, "nyadb_core_it", "replace", reader)
	if e != nil {
		t.Fatal(e)
	}
	outcome := m.RestoreMySQL(ctx, plan)
	if outcome.Error != nil || !outcome.Verified {
		t.Fatalf("restore: %+v", outcome)
	}
	result, e := m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "SELECT id,LENGTH(body),HEX(bytes),nullable,`generated` FROM backup_child ORDER BY id; SELECT amount FROM backup_parent;")
	if e != nil || len(result.Sets) != 2 || result.Rows[0][1] != "4" || result.Rows[1][1] != "0" || result.Rows[2][1] != "1100000" || result.Rows[1][2] != "00FF" || result.Sets[0].Rows[0][3].Kind != "null" || result.Sets[0].Rows[1][3].Text != "NULL" || result.Sets[1].Rows[0][0].Text != "9007199254740993.12345678901234567890" {
		t.Fatalf("data fidelity: %+v %v", result, e)
	}
	geometryAfter, e := m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "SELECT HEX(flags),HEX(geom) FROM backup_child WHERE id=1")
	if e != nil || fmt.Sprint(geometryBefore.Rows) != fmt.Sprint(geometryAfter.Rows) {
		t.Fatal("BIT/GIS bytes changed", geometryBefore.Rows, geometryAfter.Rows, e)
	}
	var dump strings.Builder
	if e = SQLDump(ctx, reader, &dump, true); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(dump.String(), "DELIMITER") || !strings.Contains(dump.String(), "9007199254740993.12345678901234567890") {
		t.Fatal("SQL dump lost definitions/values")
	}
	if _, e = m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "DROP TABLE backup_child; DROP TABLE backup_parent;"); e != nil {
		t.Fatal(e)
	}
	scriptResult, e := m.ExecuteSQLTabTokenFile(ctx, token, "nyadb_core_it", strings.NewReader(dump.String()))
	if e != nil {
		t.Fatalf("SQL dump replay failed: %+v %v", scriptResult.Steps, e)
	}
	afterDump, e := m.ExecuteSQLTabToken(ctx, token, "nyadb_core_it", "SELECT LENGTH(body),HEX(flags),HEX(geom) FROM backup_child WHERE id=3")
	if e != nil || afterDump.Rows[0][0] != "1100000" {
		t.Fatalf("SQL dump changed large row: %+v %v", afterDump, e)
	}

}
