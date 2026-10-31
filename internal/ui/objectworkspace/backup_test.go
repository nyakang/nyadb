package objectworkspace

import (
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/backups"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/services/queries"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type backupTestRuntime struct {
	generation uint64
	run        func(context.Context, io.Writer) error
	restore    func(mysqlsvc.RestorePlan) mysqlsvc.DDLOutcome
}

func (r *backupTestRuntime) SessionGeneration(int64) uint64 { return r.generation }
func (r *backupTestRuntime) BackupMySQL(ctx context.Context, _ int64, _ mysqlsvc.BackupRequest, w io.Writer) (backups.Manifest, error) {
	return backups.Manifest{}, r.run(ctx, w)
}
func (r *backupTestRuntime) DumpMySQL(ctx context.Context, _ int64, _ mysqlsvc.BackupRequest, w io.Writer) error {
	return r.run(ctx, w)
}
func (r *backupTestRuntime) PlanRestore(context.Context, int64, uint64, string, string, *backups.Reader) (mysqlsvc.RestorePlan, error) {
	return mysqlsvc.RestorePlan{}, nil
}
func (r *backupTestRuntime) RestoreMySQL(_ context.Context, p mysqlsvc.RestorePlan) mysqlsvc.DDLOutcome {
	return r.restore(p)
}
func TestBackupCancellationWaitsForReceiptAndPreservesDestination(t *testing.T) {
	session := Session{ProfileID: 8, Generation: 4, Connected: true}
	s := New(Dependencies{Session: func() Session { return session }, SessionFor: func(int64) Session { return session }})
	defer s.CloseAll()
	path := filepath.Join(t.TempDir(), "existing.zip")
	_ = os.WriteFile(path, []byte("original"), 0600)
	started, ack := make(chan struct{}), make(chan struct{})
	r := &backupTestRuntime{generation: 9, run: func(ctx context.Context, w io.Writer) error {
		_, _ = io.WriteString(w, "partial")
		close(started)
		<-ctx.Done()
		<-ack
		return ctx.Err()
	}}
	s.SetBackupServices(func() BackupRuntime { return r }, BackupFiles{Save: func(string) (string, error) { return path, nil }, Write: queries.WriteStream})
	services := ui.Services{}
	s.BeginBackup(services, 8, "original", "", "backup")
	op := s.backup
	s.startBackup(services, op)
	<-started
	op.cancel()
	s.consumeBackup(services)
	if !op.busy {
		t.Fatal("cancel settled before writer receipt")
	}
	close(ack)
	deadline := time.Now().Add(time.Second)
	for op.busy && time.Now().Before(deadline) {
		s.consumeBackup(services)
		time.Sleep(time.Millisecond)
	}
	b, _ := os.ReadFile(path)
	if op.busy || string(b) != "original" {
		t.Fatal("cancelled backup published partial file")
	}
}
func TestRestoreCompletionCarriesOriginalUIGenerationAndNeverReplays(t *testing.T) {
	session := Session{ProfileID: 8, Generation: 4, Connected: true}
	events := make(chan ObjectEvent, 1)
	calls := 0
	s := New(Dependencies{Session: func() Session { return session }, SessionFor: func(int64) Session { return Session{ProfileID: 8, Generation: 4, Connected: true} }, ObjectChanged: func(_ ui.Services, e ObjectEvent) { events <- e }})
	defer s.CloseAll()
	r := &backupTestRuntime{generation: 9, restore: func(p mysqlsvc.RestorePlan) mysqlsvc.DDLOutcome {
		calls++
		if p.Database != "original" || p.Generation != 9 {
			t.Error("lost original target")
		}
		return mysqlsvc.DDLOutcome{State: "uncertain"}
	}}
	s.SetBackupServices(func() BackupRuntime { return r }, BackupFiles{})
	services := ui.Services{}
	s.BeginBackup(services, 8, "original", "", "restore")
	op := s.backup
	op.plan = &mysqlsvc.RestorePlan{ProfileID: 8, Generation: 9, Database: "original"}
	s.executeRestore(services, op)
	session.ProfileID = 99
	deadline := time.Now().Add(time.Second)
	for op.busy && time.Now().Before(deadline) {
		s.consumeBackup(services)
		time.Sleep(time.Millisecond)
	}
	event := <-events
	if event.ProfileID != 8 || event.Generation != 4 || calls != 1 || op.plan != nil {
		t.Fatal("wrong completion identity or replayable restore")
	}
	s.executeRestore(services, op)
	if calls != 1 {
		t.Fatal("uncertain operation replayed")
	}
}
