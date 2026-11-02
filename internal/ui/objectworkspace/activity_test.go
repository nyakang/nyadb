package objectworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestAccountActivityCapturesOriginalScopeAndExcludesPasswordAndRawOutcome(t *testing.T) {
	session := Session{ProfileID: 7, Generation: 31, Connected: true}
	started, release := make(chan struct{}), make(chan struct{})
	results := make(chan contract.ActivityResult, 1)
	var request contract.ActivityRequest
	s := New(Dependencies{Session: func() Session { return session }, SessionFor: func(int64) Session { return session }, RecordActivity: func(v contract.ActivityRequest) func(contract.ActivityResult) {
		request = v
		return func(out contract.ActivityResult) { results <- out }
	}, AccountExecutor: func(_ context.Context, profile int64, generation uint64, changes []mysqlsvc.AccountChange) mysqlsvc.DDLOutcome {
		if profile != 7 || generation != 9 || len(changes) != 1 || changes[0].Password != "private-password" {
			t.Error("account worker changed captured request")
		}
		close(started)
		<-release
		return mysqlsvc.DDLOutcome{State: "partial", Error: errors.New("private-password echoed"), Steps: []mysqlsvc.DDLStep{{SQL: "private-password"}}}
	}})
	t.Cleanup(s.CloseAll)
	s.accounts = &accountPanel{profile: 7, generation: 9, sessionGeneration: 31, open: true, user: "fixture", host: "localhost", action: "create_user", password: "private-password", database: "original"}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Modals(c) }, 900, 700)
	tt.Frame()
	if err := tt.Click("确认执行账户操作"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("account operation did not start")
	}
	session = Session{ProfileID: 8, Generation: 32, Connected: true}
	s.CloseAll()
	close(release)
	var result contract.ActivityResult
	select {
	case result = <-results:
	case <-time.After(3 * time.Second):
		t.Fatal("closed account operation lost completion")
	}
	raw, _ := json.Marshal([]any{request, result})
	if request.ProfileID != 7 || request.Generation != 31 || request.Database != "original" || request.Source != "accounts" || !request.Summary || result.State != "partial" || strings.Contains(string(raw), "private-password") {
		t.Fatalf("unsafe or rebound global account log: %s", raw)
	}
}
func TestClosedBackupActivityWaitsForActualCancellationAcknowledgement(t *testing.T) {
	session := Session{ProfileID: 7, Generation: 31, Connected: true}
	started, release := make(chan struct{}), make(chan struct{})
	results := make(chan contract.ActivityResult, 1)
	var request contract.ActivityRequest
	s := New(Dependencies{Session: func() Session { return session }, SessionFor: func(int64) Session { return session }, RecordActivity: func(v contract.ActivityRequest) func(contract.ActivityResult) {
		request = v
		return func(out contract.ActivityResult) { results <- out }
	}})
	r := &backupTestRuntime{generation: 9, run: func(ctx context.Context, w io.Writer) error {
		close(started)
		<-ctx.Done()
		<-release
		return ctx.Err()
	}}
	s.SetBackupServices(func() BackupRuntime { return r }, BackupFiles{Save: func(string) (string, error) { return "fixture-path", nil }, Write: func(_ string, write func(io.Writer) error) error { return write(io.Discard) }})
	s.BeginBackup(ui.Services{}, 7, "original", "entries", "backup")
	s.startBackup(ui.Services{}, s.backup)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("backup did not start")
	}
	s.CloseAll()
	select {
	case <-results:
		t.Fatal("cancellation logged before acknowledgement")
	default:
	}
	close(release)
	select {
	case result := <-results:
		if result.State != "cancelled" || request.ProfileID != 7 || request.Generation != 31 || request.Database != "original" || request.Object != "entries" {
			t.Fatal("backup lost original operation scope")
		}
	case <-time.After(time.Second):
		t.Fatal("closed backup lost global receipt")
	}
}
