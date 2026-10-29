package sqlworkspace

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type testLease struct {
	prepare              func(context.Context) error
	execute              func(context.Context, string, string) (mysqlsvc.MySQLSQLResult, error)
	replace              func() (Lease, error)
	prepares, executions atomic.Int32
	releases             int
}

func (l *testLease) Preconnect(ctx context.Context) error {
	l.prepares.Add(1)
	if l.prepare != nil {
		return l.prepare(ctx)
	}
	return nil
}
func (l *testLease) Execute(ctx context.Context, database, sql string) (mysqlsvc.MySQLSQLResult, error) {
	l.executions.Add(1)
	if l.execute != nil {
		return l.execute(ctx, database, sql)
	}
	return mysqlsvc.MySQLSQLResult{AffectedRows: 1}, nil
}
func (l *testLease) Replace() (Lease, error) {
	if l.replace != nil {
		return l.replace()
	}
	return nil, mysqlsvc.ErrMySQLSQLSessionLost
}
func (l *testLease) Release() { l.releases++ }

type testRuntime struct {
	reserve      func(int64, uint64) (Lease, error)
	reservations int
}

func (r *testRuntime) IsConnected(id int64) bool { return id > 0 }
func (r *testRuntime) Reserve(id int64, doc uint64) (Lease, error) {
	r.reservations++
	return r.reserve(id, doc)
}
func awaitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("SQL worker did not signal completion")
	}
}
func consumeUntil(t *testing.T, s *State, complete func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !complete() && time.Now().Before(deadline) {
		s.Consume()
		time.Sleep(time.Millisecond)
	}
	s.Consume()
	if !complete() {
		t.Fatal("SQL result did not complete")
	}
}

func TestSQLPrepareIsAsyncAndFailureCannotOverwriteCompletedResult(t *testing.T) {
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}, 2)
	lease := &testLease{prepare: func(ctx context.Context) error { close(started); <-release; return errors.New("late dial error") }}
	runtime := &testRuntime{reserve: func(id int64, doc uint64) (Lease, error) {
		if id != 9 || doc != 42 {
			t.Errorf("wrong reservation identity: %d/%d", id, doc)
		}
		return lease, nil
	}}
	s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: runtime.IsConnected})
	s.Invalidate = func() { finished <- struct{}{} }
	d := s.Open(42, "SELECT @session", 9, "db_test", false)
	t.Cleanup(s.CloseAll)
	s.Prepare(d)
	awaitSignal(t, started)
	s.Prepare(d)
	if runtime.reservations != 1 || lease.executions.Load() != 0 {
		t.Fatal("prepare reserved twice or ran SQL")
	}
	s.Run(s.Invalidate, d)
	awaitSignal(t, finished)
	s.Consume()
	if d.Result() == nil || d.Result().AffectedRows != 1 {
		t.Fatal("explicit execution did not finish while preparation was parked")
	}
	close(release)
	awaitSignal(t, finished)
	s.Consume()
	if d.Error() != "" || d.Result() == nil || d.Editor.Buffer.String() != "SELECT @session" {
		t.Fatal("late preparation overwrote completed query")
	}
}

func TestSQLClosedPreparationCannotReachReplacementDocument(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "close", true: "disconnect"}[disconnect], func(t *testing.T) {
			started, canceled, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
			oldLease := &testLease{prepare: func(ctx context.Context) error { close(started); <-ctx.Done(); close(canceled); <-release; return nil }}
			newLease := &testLease{}
			current := oldLease
			runtime := &testRuntime{reserve: func(int64, uint64) (Lease, error) { return current, nil }}
			s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: runtime.IsConnected})
			s.Invalidate = func() { done <- struct{}{} }
			d := s.Open(8, "SELECT original", 1, "original_db", false)
			s.Prepare(d)
			awaitSignal(t, started)
			if disconnect {
				s.DisconnectProfile(1)
			} else {
				s.Close(d)
			}
			awaitSignal(t, canceled)
			if oldLease.releases != 1 || d.Loading() {
				t.Fatal("old preparation was not revoked")
			}
			current = newLease
			var replacement *Document
			if disconnect {
				s.Recover(d)
				replacement = d
			} else {
				replacement = s.Open(8, "SELECT replacement", 1, "new_db", false)
				s.Prepare(replacement)
			}
			awaitSignal(t, done)
			s.Consume()
			close(release)
			awaitSignal(t, done)
			s.Consume()
			if replacement.NeedsRecovery() || replacement.Error() != "" || replacement.lease != newLease || newLease.executions.Load() != 0 {
				t.Fatal("late old preparation contaminated replacement or replayed SQL")
			}
			s.CloseAll()
			s.CloseAll()
			if oldLease.releases != 1 || newLease.releases != 1 || len(s.documents) != 0 {
				t.Fatal("cleanup released a lease twice or kept documents")
			}
		})
	}
}

func TestSQLRecoveryReplacesOwningLeaseAndNeverReplays(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "replace", true: "reserve-stale"}[stale], func(t *testing.T) {
			fresh := &testLease{}
			old := &testLease{execute: func(context.Context, string, string) (mysqlsvc.MySQLSQLResult, error) {
				return mysqlsvc.MySQLSQLResult{}, mysqlsvc.ErrMySQLSQLSessionLost
			}}
			replacements := 0
			old.replace = func() (Lease, error) {
				replacements++
				if stale {
					return nil, mysqlsvc.ErrMySQLSQLSessionLost
				}
				return fresh, nil
			}
			runtime := &testRuntime{reserve: func(int64, uint64) (Lease, error) {
				if old.executions.Load() == 0 {
					return old, nil
				}
				return fresh, nil
			}}
			var activeID int64
			s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: runtime.IsConnected, Recovered: func(id int64) { activeID = id }})
			d := s.Open(3, "UPDATE tx_probe SET note='do_not_replay'", 5, "original_db", false)
			s.Prepare(d)
			consumeUntil(t, s, func() bool { return d.prepareResults == nil })
			s.Run(nil, d)
			consumeUntil(t, s, func() bool { return !d.Loading() })
			if !d.NeedsRecovery() {
				t.Fatal("lost session was not marked for explicit recovery")
			}
			s.Prepare(d)
			s.Run(nil, d)
			if old.executions.Load() != 1 || runtime.reservations != 1 {
				t.Fatal("lost session automatically retried")
			}
			s.Recover(d)
			consumeUntil(t, s, func() bool { return d.prepareResults == nil })
			wantReservations := 1
			if stale {
				wantReservations = 2
			}
			if replacements != 1 || runtime.reservations != wantReservations || old.releases != 1 || activeID != 5 || d.ProfileID() != 5 || d.Database() != "original_db" || d.NeedsRecovery() {
				t.Fatal("recovery lost document identity or owning lease")
			}
			if d.Editor.Buffer.String() != "UPDATE tx_probe SET note='do_not_replay'" || fresh.executions.Load() != 0 || d.Result() != nil {
				t.Fatal("recovery replayed SQL or discarded editor")
			}
			s.Run(nil, d)
			consumeUntil(t, s, func() bool { return !d.Loading() })
			if fresh.executions.Load() != 1 || d.Result() == nil {
				t.Fatal("explicit run after recovery did not use the new lease")
			}
			s.CloseAll()
			if fresh.releases != 1 || old.releases != 1 {
				t.Fatal("close released the wrong lease")
			}
		})
	}
}

func TestSQLDisconnectDiscardsLateExecutionAndRetainsSiblingStatus(t *testing.T) {
	started, canceled, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	old := &testLease{execute: func(ctx context.Context, db, sql string) (mysqlsvc.MySQLSQLResult, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return mysqlsvc.MySQLSQLResult{AffectedRows: 99}, nil
	}}
	siblingLease := &testLease{}
	runtime := &testRuntime{reserve: func(id int64, _ uint64) (Lease, error) {
		if id == 1 {
			return old, nil
		}
		return siblingLease, nil
	}}
	var active *Document
	status := ""
	s := New(Dependencies{Runtime: func() Runtime { return runtime }, Connected: runtime.IsConnected, Active: func(d *Document) bool { return d == active }, Status: func(message string) { status = message }})
	a := s.Open(1, "UPDATE original", 1, "a_db", false)
	b := s.Open(2, "SELECT sibling", 2, "b_db", false)
	s.Prepare(a)
	s.Prepare(b)
	consumeUntil(t, s, func() bool { return a.prepareResults == nil && b.prepareResults == nil })
	active = a
	s.Run(func() { done <- struct{}{} }, a)
	awaitSignal(t, started)
	active = b
	s.Run(nil, b)
	consumeUntil(t, s, func() bool { return !b.Loading() })
	siblingStatus := status
	s.DisconnectProfile(1)
	awaitSignal(t, canceled)
	close(release)
	awaitSignal(t, done)
	s.Consume()
	if a.Loading() || a.Result() != nil || a.ProfileID() != 0 || !a.NeedsRecovery() || a.Editor.Buffer.String() != "UPDATE original" {
		t.Fatal("disconnect retained late result or lost unsaved document")
	}
	if old.releases != 1 || siblingLease.releases != 0 || b.Result() == nil || status != siblingStatus {
		t.Fatal("disconnect or late result affected sibling")
	}
	s.CloseAll()
}

func TestSQLInactiveCompletionAndCancellationKeepActiveStatus(t *testing.T) {
	var active *Document
	status := ""
	s := New(Dependencies{Connected: func(int64) bool { return true }, Active: func(d *Document) bool { return d == active }, Status: func(message string) { status = message }})
	a := s.Open(1, "SELECT background", 1, "a_db", false)
	active = s.Open(2, "SELECT active", 2, "b_db", false)
	for _, failure := range []bool{false, true} {
		s.Executor = func(context.Context, int64, string, string) (mysqlsvc.MySQLSQLResult, error) {
			if failure {
				return mysqlsvc.MySQLSQLResult{}, errors.New("background error")
			}
			return mysqlsvc.MySQLSQLResult{AffectedRows: 2}, nil
		}
		s.Run(nil, a)
		status = "active document"
		consumeUntil(t, s, func() bool { return !a.Loading() })
		if status != "active document" {
			t.Fatal("inactive completion changed active status")
		}
	}
	canceled := make(chan struct{})
	s.Executor = func(ctx context.Context, _ int64, _, _ string) (mysqlsvc.MySQLSQLResult, error) {
		<-ctx.Done()
		close(canceled)
		return mysqlsvc.MySQLSQLResult{}, ctx.Err()
	}
	s.Run(nil, a)
	status = "active document"
	s.RequestCancel(a)
	awaitSignal(t, canceled)
	consumeUntil(t, s, func() bool { return !a.Loading() })
	if status != "active document" || !strings.Contains(a.Error(), "服务器端更改可能已生效") {
		t.Fatal("inactive cancellation contaminated status or promised rollback")
	}
	s.CloseAll()
}

func TestSQLRecoveryFailureRetainsOldLeaseAndEditor(t *testing.T) {
 old:=&testLease{replace:func()(Lease,error){return nil,mysqlsvc.ErrMySQLSQLSessionLimit}}
 runtime:=&testRuntime{reserve:func(int64,uint64)(Lease,error){return nil,mysqlsvc.ErrMySQLSQLSessionLimit}}
 s:=New(Dependencies{Runtime:func()Runtime{return runtime},Connected:runtime.IsConnected})
 d:=s.Open(7,"SELECT original",4,"original_db",false)
 d.lease=old
 d.recoveryNeeded,d.recoveryProfile,d.recoverySchema=true,4,"original_db"
 s.Recover(d)
 if !d.NeedsRecovery()||d.lease!=old||old.releases!=0||runtime.reservations!=0||d.Editor.Buffer.String()!="SELECT original"||!strings.Contains(d.Error(),"新会话创建失败"){t.Fatal("failed recovery revoked old context or discarded SQL")}
 s.CloseAll()
 if old.releases!=1{t.Fatal("failed recovery lost ownership of old lease")}
}

func TestSQLDocumentKeepsLeaseOwnerWhenHostRuntimeChanges(t *testing.T) {
 oldLease,newLease:=&testLease{},&testLease{}
 oldRuntime:=&testRuntime{reserve:func(int64,uint64)(Lease,error){return oldLease,nil}}
 newRuntime:=&testRuntime{reserve:func(int64,uint64)(Lease,error){return newLease,nil}}
 var current Runtime=oldRuntime
 s:=New(Dependencies{Runtime:func()Runtime{return current},Connected:func(int64)bool{return true}})
 d:=s.Open(1,"SELECT @original_session",1,"original_db",false)
 s.Prepare(d);consumeUntil(t,s,func()bool{return d.prepareResults==nil})
 current=newRuntime
 s.Run(nil,d);consumeUntil(t,s,func()bool{return !d.Loading()})
 s.CloseAll()
 if oldLease.executions.Load()!=1||oldLease.releases!=1||newLease.executions.Load()!=0||newLease.releases!=0||newRuntime.reservations!=0{t.Fatal("host runtime change used or released a different lease")}
}
