package shell

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
)

// Run only in a separate process inside tools/verify_entry_acceptance.py's
// disposable fixture. No runtime/reader/executor is mocked in these tests.
func TestEntryAcceptanceRealMySQLShell(t *testing.T) {
	if os.Getenv("NYADB_ENTRY_IT") != "1" {
		t.Skip("requires explicitly owned isolated entry fixture")
	}
	root := os.Getenv("NYADB_ENTRY_ROOT")
	marker, e := os.ReadFile(filepath.Join(root, ".owned"))
	if e != nil || len(bytes.TrimSpace(marker)) != 12 || os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "unix:path="+root+"/runtime/bus" {
		t.Fatal("private fixture required")
	}
	port, e := strconv.Atoi(os.Getenv("NYADB_ENTRY_PORT"))
	if e != nil || port < 1024 || port > 65535 {
		t.Fatal("invalid disposable port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg := mysqldriver.NewConfig()
	cfg.User = "entry_admin"
	cfg.Passwd = os.Getenv("NYADB_ENTRY_PASSWORD")
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("127.0.0.1:%d", port)
	cfg.Timeout = 5 * time.Second
	connector, e := mysqldriver.NewConnector(cfg)
	if e != nil {
		t.Fatal("fixture connector failed")
	}
	verify := sql.OpenDB(connector)
	defer verify.Close()
	if e = verify.PingContext(ctx); e != nil {
		t.Fatal("owned fixture unavailable")
	}
	db, e := localdb.Open(filepath.Join(t.TempDir(), "profiles.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	svc := connections.NewService(localdb.NewProfileStore(db))
	s := NewWithProfiles(svc)
	defer s.CloseMySQLSessions()
	vault := credentials.NewOSVault()
	s.SetCredentialVault(vault)
	historyStore := queries.NewStore(localdb.NewSQLRepository(db), credentials.WorkspaceKey{})
	s.SetSQLPersistence(historyStore, sqlworkspace.Files{}, nil)
	var profiles []connections.Profile
	for _, user := range []string{"entry_admin", "entry_read"} {
		p, e := svc.Create(ctx, connections.Profile{Name: user, Kind: connections.MySQL, Host: "127.0.0.1", Port: uint16(port), Username: user, MySQLAdvanced: connections.DefaultMySQLAdvanced()})
		if e != nil {
			t.Fatal(e)
		}
		password := os.Getenv("NYADB_ENTRY_PASSWORD")
		if user == "entry_read" {
			password = os.Getenv("NYADB_ENTRY_READ_PASSWORD")
		}
		if e = vault.Set(p.ID, "", password); e != nil {
			t.Fatal("real vault write failed")
		}
		profiles = append(profiles, p)
	}
	s.loadProfiles()
	tt := ui.NewTester(s.View, 1280, 800)
	wait := func(t *testing.T, message string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			tt.Frame()
			if ready() {
				return
			}
			time.Sleep(15 * time.Millisecond)
		}
		t.Fatalf("%s: timeout", message)
	}
	click := func(t *testing.T, label string) {
		t.Helper()
		if e := tt.Click(label); e != nil {
			t.Fatal(e)
		}
	}
	choose := func(t *testing.T, label string) {
		t.Helper()
		if e := tt.ChooseMenuItem(label); e != nil {
			t.Fatal(e)
		}
	}
	connect := func(t *testing.T, p connections.Profile) {
		t.Helper()
		s.selectProfileByName(p.Name)
		tt.Frame()
		click(t, "连接所选 MySQL")
		wait(t, "real connect", func() bool { return s.sessions.Current().Status != "连接中" })
		if s.sessions.Current().Status != "已连接" {
			t.Fatal("real connect failed")
		}
		s.navigation.Tree.Open.Add(navigation.ConnectionKey(p.ID))
		s.selectNavigationNode(ui.Services{}, navigation.DatabaseNode(p.ID, "entry_source").Key())
		tt.Frame()
		if e := tt.RightClick("数据库：entry_source"); e != nil {
			t.Fatal(e)
		}
		choose(t, "打开数据库")
		wait(t, "metadata", func() bool {
			return s.sessions.Current().Pending == "" && len(s.sessions.Current().Cache["entry_source"]) > 0
		})
	}
	connect(t, profiles[0])
	t.Run("program-entry-lists", func(t *testing.T) {
		for _, x := range []struct{ category, name string }{{"函数", "entry_function"}, {"存储过程", "entry_procedure"}, {"事件", "entry_event"}} {
			if x.category == "函数" {
				click(t, "函数")
			} else {
				click(t, "其它")
				choose(t, x.category)
			}
			if s.objectWorkspace.Category(profiles[0].ID) != x.category || !tt.HasText(x.name) {
				t.Fatalf("real %s list missing", x.category)
			}
		}
	})
	t.Run("account-create-grant-revoke-drop", func(t *testing.T) {
		click(t, "用户")
		wait(t, "account read", func() bool { return tt.Click("现有账户") == nil })
		if !tt.HasText("entry_admin@%") {
			t.Fatal("real account list missing")
		}
		click(t, "entry_admin@%")
		user := "entry_ui_account"
		_, e := verify.ExecContext(ctx, "DROP USER IF EXISTS 'entry_ui_account'@'%'")
		if e != nil {
			t.Fatal(e)
		}
		defer verify.ExecContext(context.Background(), "DROP USER IF EXISTS 'entry_ui_account'@'%'")
		setTestInput(t, tt, "账户名", user)
		for _, action := range []string{"create_user", "grant", "revoke", "drop"} {
			click(t, "账户操作")
			click(t, action)
			if action == "create_user" {
				setTestInput(t, tt, "账户密码", os.Getenv("NYADB_ENTRY_READ_PASSWORD"))
			}
			if action == "grant" || action == "revoke" {
				setTestInput(t, tt, "授权数据库（空为全部）", "entry_source")
				setTestInput(t, tt, "授权表（空为全部）", "samples")
			}
			click(t, "确认执行账户操作")
			wait(t, "account receipt", func() bool { return tt.HasText("权限操作完成；可重新读取授权核验") })
			var n int
			if action == "create_user" || action == "drop" {
				e = verify.QueryRowContext(ctx, "SELECT COUNT(*) FROM mysql.user WHERE User=? AND Host='%'", user).Scan(&n)
				if e != nil || n != map[string]int{"create_user": 1, "drop": 0}[action] {
					t.Fatal("account mutation not verified")
				}
			} else {
				e = verify.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLE_PRIVILEGES WHERE GRANTEE=? AND TABLE_SCHEMA='entry_source' AND TABLE_NAME='samples' AND PRIVILEGE_TYPE='SELECT'", "'"+user+"'@'%'").Scan(&n)
				if e != nil || n != map[string]int{"grant": 1, "revoke": 0}[action] {
					t.Fatal("grant mutation not verified")
				}
			}
			if action == "grant" {
				click(t, "读取账户与授权")
				wait(t, "grant read", func() bool { return tt.Click("账户名") == nil })
			}
		}
		click(t, "关闭")
		tt.Advance(time.Second)
	})
	dir := filepath.Join(root, "evidence")
	backup := filepath.Join(dir, "shell.nyabackup")
	output := backup
	s.SetBackupFiles(objectworkspace.BackupFiles{Save: func(string) (string, error) { return output, nil }, Open: func() (string, error) { return backup, nil }, Write: queries.WriteStream})
	tt.Frame()
	startBackup := func(t *testing.T, label string) {
		t.Helper()
		click(t, "备份")

		choose(t, label)
		click(t, "选择文件并开始备份")
	}
	closeBackup := func(t *testing.T) {
		t.Helper()
		wait(t, "backup acknowledgement", func() bool { return !tt.HasText("取消并等待回执") })
		click(t, "关闭")
		tt.Advance(time.Second)
	}
	t.Run("backup-restore-exact-data", func(t *testing.T) {

		for _, schema := range []string{"entry_target", "entry_dump"} {
			for _, stmt := range []string{"DROP TABLE IF EXISTS " + schema + ".samples", "DROP FUNCTION IF EXISTS " + schema + ".entry_function", "DROP PROCEDURE IF EXISTS " + schema + ".entry_procedure", "DROP EVENT IF EXISTS " + schema + ".entry_event"} {
				if _, err := verify.ExecContext(ctx, stmt); err != nil {
					t.Fatal(err)
				}
			}
		}
		startBackup(t, "备份数据库…")
		wait(t, "backup complete", func() bool { return tt.HasText("备份已完成：" + output) })
		closeBackup(t)
		for _, schema := range []string{"entry_target", "entry_dump"} {
			if _, e = verify.ExecContext(ctx, "DROP TABLE IF EXISTS "+schema+".samples"); e != nil {
				t.Fatal(e)
			}
		}
		click(t, "备份")
		choose(t, "恢复备份…")
		setTestInput(t, tt, "目标数据库", "entry_target")
		click(t, "读取备份并生成恢复预览")
		wait(t, "restore preview", func() bool { return tt.HasText("确认按预览恢复") })
		var n int
		if e = verify.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='entry_target'").Scan(&n); e != nil || n != 0 {
			t.Fatal("preview executed SQL")
		}
		click(t, "确认按预览恢复")
		wait(t, "restore result", func() bool { return tt.HasText("结果：executed") })
		closeBackup(t)
		entryVerifyRows(t, ctx, verify, "entry_target")
	})
	t.Run("sql-dump-structure-and-data", func(t *testing.T) {
		for _, x := range []struct {
			label, file string
			data        bool
		}{{"导出结构 SQL…", "structure.sql", false}, {"导出结构及数据 SQL…", "data.sql", true}} {
			output = filepath.Join(dir, x.file)
			startBackup(t, x.label)
			wait(t, "dump completed", func() bool { return tt.HasText("备份已完成：" + output) })
			closeBackup(t)
			raw, e := os.ReadFile(output)
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(string(raw), "CREATE TABLE") || strings.Contains(string(raw), "INSERT INTO") != x.data {
				t.Fatal("dump mode mismatch")
			}
			if x.data {
				sess, e := s.runtime.Session(profiles[0].ID)
				if e != nil {
					t.Fatal(e)
				}
				provider, e := contract.Require[contract.QueryProvider](sess)
				if e != nil {
					t.Fatal(e)
				}
				lease, e := provider.ReserveQuery(991)
				if e != nil {
					t.Fatal(e)
				}
				defer lease.Release()
				// Exports deliberately qualify the source schema. Rebind only this fixture copy.
				if _, e = lease.(contract.QueryFileLease).ExecuteFile(ctx, "entry_dump", strings.NewReader(strings.ReplaceAll(string(raw), "`entry_source`", "`entry_dump`"))); e != nil {
					t.Fatal(e)
				}
				entryVerifyRows(t, ctx, verify, "entry_dump")
			}
		}
	})
	t.Run("real-lock-cancellation-preserves-output", func(t *testing.T) {
		lock, e := verify.Conn(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer lock.Close()
		if _, e = lock.ExecContext(ctx, "LOCK TABLES entry_source.samples WRITE"); e != nil {
			t.Fatal(e)
		}
		defer lock.ExecContext(context.Background(), "UNLOCK TABLES")
		output = filepath.Join(dir, "cancel.nyabackup")
		original := []byte("prior destination")
		if e = os.WriteFile(output, original, 0600); e != nil {
			t.Fatal(e)
		}
		startBackup(t, "备份数据库…")
		wait(t, "actual server lock wait", func() bool {
			var n int
			_ = verify.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE USER='entry_admin' AND STATE LIKE '%lock%'").Scan(&n)
			return n > 0
		})
		click(t, "取消并等待回执")
		wait(t, "cancel acknowledgement", func() bool { return !tt.HasText("取消并等待回执") })
		got, e := os.ReadFile(output)
		if e != nil || !bytes.Equal(got, original) {
			t.Fatal("cancel replaced existing output")
		}
		click(t, "关闭")
		tt.Advance(time.Second)
		if _, e = lock.ExecContext(ctx, "UNLOCK TABLES"); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("stale-menu-preview-and-reconnect", func(t *testing.T) {
		click(t, "备份")
		old := s.sessions.Current().Generation
		s.disconnectMySQLProfile(profiles[0].ID)
		tt.Frame()
		choose(t, "备份数据库…")
		if tt.HasText("备份与 SQL 转储") {
			t.Fatal("stale menu opened modal")
		}
		connect(t, profiles[0])
		if s.sessions.Current().Generation == old {
			t.Fatal("real reconnect reused generation")
		}
		click(t, "备份")
		choose(t, "恢复备份…")
		setTestInput(t, tt, "目标数据库", "entry_target")
		click(t, "替换已有对象")
		click(t, "读取备份并生成恢复预览")
		wait(t, "preview", func() bool { return tt.HasText("确认按预览恢复") })
		s.selectProfileByName(profiles[1].Name)
		tt.Frame()
		if tt.HasText("确认按预览恢复") {
			t.Fatal("preview leaked to new profile")
		}
		s.selectProfileByName(profiles[0].Name)
		s.disconnectMySQLProfile(profiles[0].ID)
		tt.Frame()
		if s.runtime.IsConnected(profiles[0].ID) {
			t.Fatal("real disconnect failed")
		}
		if _, e = verify.ExecContext(ctx, "UPDATE entry_target.samples SET optional='stale-preview-sentinel' WHERE id=1"); e != nil {
			t.Fatal(e)
		}
		_ = tt.Click("确认按预览恢复")
		tt.Frame()
		var sentinel string
		if e = verify.QueryRowContext(ctx, "SELECT optional FROM entry_target.samples WHERE id=1").Scan(&sentinel); e != nil || sentinel != "stale-preview-sentinel" {
			t.Fatal("stale preview executed restore")
		}

		s.objectWorkspace.CloseAll()
	})
	t.Run("read-only-account-error", func(t *testing.T) {
		connect(t, profiles[1])
		if _, err := verify.ExecContext(ctx, "DROP USER IF EXISTS 'entry_read_denied'@'%'"); err != nil {
			t.Fatal(err)
		}
		defer verify.ExecContext(context.Background(), "DROP USER IF EXISTS 'entry_read_denied'@'%'")
		click(t, "用户")
		wait(t, "readonly account read settled", func() bool {
			for _, label := range tt.Texts() {
				if strings.Contains(label, "1142") {
					return true
				}
			}
			return false
		})
		setTestInput(t, tt, "账户名", "entry_read_denied")
		click(t, "账户操作")
		click(t, "create_user")
		setTestInput(t, tt, "账户密码", os.Getenv("NYADB_ENTRY_READ_PASSWORD"))
		click(t, "确认执行账户操作")
		wait(t, "server denied mutation", func() bool {
			for _, label := range tt.Texts() {
				if strings.Contains(label, "account step 1 rejected by server (code 1227)") {
					return true
				}
			}
			return false
		})
		var n int
		if e = verify.QueryRowContext(ctx, "SELECT COUNT(*) FROM mysql.user WHERE User='entry_read_denied'").Scan(&n); e != nil || n != 0 {
			t.Fatal("read-only user created account")
		}
		click(t, "关闭")
		tt.Advance(time.Second)
	})
	t.Run("parked-backup-receipt-keeps-original-scope", func(t *testing.T) {
		connect(t, profiles[0])
		lock, err := verify.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if _, err = lock.ExecContext(ctx, "LOCK TABLES entry_source.samples WRITE"); err != nil {
			t.Fatal(err)
		}
		defer lock.ExecContext(context.Background(), "UNLOCK TABLES")
		output = filepath.Join(dir, "parked.nyabackup")
		original := []byte("prior parked destination")
		if err = os.WriteFile(output, original, 0600); err != nil {
			t.Fatal(err)
		}
		startBackup(t, "备份数据库…")
		wait(t, "actual parked lock wait", func() bool {
			var n int
			_ = verify.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE USER='entry_admin' AND STATE LIKE '%lock%'").Scan(&n)
			return n > 0
		})
		s.selectProfileByName(profiles[1].Name)
		tt.Frame()
		if tt.HasText("备份与 SQL 转储") {
			t.Fatal("old backup appeared on new profile")
		}
		s.disconnectMySQLProfile(profiles[0].ID)
		tt.Frame()
		// MySQL may keep its metadata-lock waiter until the fixture unlocks,
		// even after the driver acknowledges cancellation and closes its socket.
		// The formal atomic writer removes its temporary file only after work returns.
		wait(t, "revoked backup acknowledgement", func() bool {
			files, err := filepath.Glob(filepath.Join(dir, ".nyadb-export-*"))
			return err == nil && len(files) == 0
		})

		if !s.runtime.IsConnected(profiles[1].ID) || tt.HasText("备份已完成："+output) {
			t.Fatal("late receipt changed new profile")
		}
		got, err := os.ReadFile(output)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatal("revocation replaced output")
		}
		if _, err = lock.ExecContext(ctx, "UNLOCK TABLES"); err != nil {
			t.Fatal(err)
		}
		s.objectWorkspace.CloseAll()
		tt.Frame()
	})

	t.Run("encrypted-global-operation-history", func(t *testing.T) {
		wait(t, "global log publication", func() bool { return !s.sqlWorkspace.HistoryPending() })
		h := s.sqlWorkspace.HistorySnapshot()
		sources, states := map[string]int{}, map[string]int{}
		for _, entry := range h.Entries {
			if entry.ProfileID != profiles[0].ID && entry.ProfileID != profiles[1].ID || entry.Generation == 0 || !entry.Summary {
				t.Fatal("global management history lost original scope or summary safety")
			}
			if strings.Contains(entry.SQL+entry.Error, os.Getenv("NYADB_ENTRY_PASSWORD")) || strings.Contains(entry.SQL+entry.Error, os.Getenv("NYADB_ENTRY_READ_PASSWORD")) {
				t.Fatal("credential entered the global log")
			}
			sources[entry.Source]++
			states[entry.State]++
			if entry.Source == "restore" && entry.Database != "entry_target" {
				t.Fatal("global restore rebound the original target")
			}
		}
		if sources["accounts"] < 7 || sources["backup"] < 5 || sources["restore_preview"] < 2 || sources["restore"] != 1 || states["error"] == 0 || states["success"] == 0 || states["cancelled"]+states["session_lost"] == 0 {
			t.Fatalf("real global log missing operation/outcome categories: %v %v", sources, states)
		}
		restored, err := queries.NewStore(localdb.NewSQLRepository(db), credentials.WorkspaceKey{}).LoadHistory(ctx, time.Now())
		if err != nil || len(restored.Entries) != len(h.Entries) {
			t.Fatal("global MySQL history failed encrypted restart")
		}
		records, err := localdb.NewSQLRepository(db).List(ctx, "history", 0)
		if err != nil || len(records) != 1 || bytes.Contains(records[0].Payload, []byte("READ ACCOUNTS")) {
			t.Fatal("management history was not encrypted")
		}
		evidence, err := json.MarshalIndent(struct {
			Entries             int
			Sources, States     map[string]int
			Encrypted, Restored bool
		}{len(h.Entries), sources, states, true, true}, "", "  ")
		if err != nil || os.WriteFile(filepath.Join(root, "evidence/mysql-global-history.json"), evidence, 0600) != nil {
			t.Fatal("global log evidence failed")
		}
	})

}
func entryVerifyRows(t *testing.T, ctx context.Context, db *sql.DB, schema string) {
	t.Helper()
	rows, e := db.QueryContext(ctx, "SELECT id,CAST(amount AS CHAR),optional,HEX(payload) FROM `"+schema+"`.samples ORDER BY id")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	const shapeSQL = `SELECT COALESCE(GROUP_CONCAT(CONCAT_WS('|',COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COALESCE(COLUMN_DEFAULT,'<NULL>'),COLUMN_KEY,EXTRA) ORDER BY ORDINAL_POSITION SEPARATOR '\n'),'') FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME='samples'`
	var sourceShape, targetShape string
	if e = db.QueryRowContext(ctx, shapeSQL, "entry_source").Scan(&sourceShape); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRowContext(ctx, shapeSQL, schema).Scan(&targetShape); e != nil || sourceShape == "" || sourceShape != targetShape {
		t.Fatal("restored column structure mismatch")
	}

	wantAmount := []string{"12345678901234567890.12345678901234567890", "-0.00000000000000000001"}
	wantBytes := []string{"0001FF", ""}
	n := 0
	for rows.Next() {
		var id int
		var amount, hex string
		var optional sql.NullString
		if e = rows.Scan(&id, &amount, &optional, &hex); e != nil {
			t.Fatal(e)
		}
		if n >= 2 || id != n+1 || amount != wantAmount[n] || hex != wantBytes[n] || optional.Valid != (n == 1) || (n == 1 && optional.String != "fixture") {
			t.Fatal("restored typed data mismatch")
		}
		n++
	}
	if e = rows.Err(); e != nil || n != 2 {
		t.Fatal("restored row count mismatch")
	}
}
