package objectworkspace

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestTableAdministrationMenusRejectChangedScope(t *testing.T) {
	for _, action := range []string{"accounts", "backup"} {
		for _, change := range []string{"none", "generation", "database"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				s, b, session := browserFixture()
				defer s.CloseAll()
				calls := 0
				s.deps.AccountLoader = func(context.Context, int64) ([]mysqlsvc.MySQLAccount, error) { return nil, nil }
				s.deps.AccountExecutor = func(context.Context, int64, uint64, []mysqlsvc.AccountChange) mysqlsvc.DDLOutcome {
					return mysqlsvc.DDLOutcome{}
				}
				s.deps.Backup = func(ui.Services, int64, string, string, string) { calls++ }
				s.SetBackupServices(func() BackupRuntime { return &backupTestRuntime{generation: 31} }, BackupFiles{})
				tt, _ := browserTester(s, 900)
				if err := tt.RightClick("orders"); err != nil {
					t.Fatal(err)
				}
				switch change {
				case "generation":
					session.Generation++
				case "database":
					b.Database = "other_database"
				}
				tt.Frame()
				path := []string{"设置权限"}
				if action == "backup" {
					path = []string{"转储 SQL 文件", "仅结构…"}
				}
				if err := tt.ChooseMenuItem(path...); err != nil {
					t.Fatal(err)
				}
				acted := s.accounts != nil || calls > 0
				if acted != (change == "none") {
					t.Fatalf("scope=%s dispatched=%v", change, acted)
				}
			})
		}
	}
}
