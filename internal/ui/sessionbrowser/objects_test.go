package sessionbrowser

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestSessionBrowserObjectRetriesStayExplicit(t *testing.T) {
	s, done, notify := connectedState(t)
	var calls atomic.Int32
	s.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("permission denied")
		}
		return []mysqlsvc.MySQLDBObject{{Name: "orders", Kind: "表"}}, nil
	}
	s.LoadObjects("app", false, notify)
	awaitJob(t, done)
	s.Consume()
	if s.Current().Errors["app"] == "" || !s.DatabaseOpened(1, "app") {
		t.Fatal("failed open did not keep explicit error/opened state")
	}
	request := s.Current().ObjectRequest()
	s.LoadObjects("app", false, notify)
	s.LoadExpanded(func(string) bool { return true }, notify)
	if calls.Load() != 1 || s.Current().ObjectRequest() != request {
		t.Fatal("frame-driven navigation retried a failed read")
	}
	s.LoadObjects("app", true, notify)
	awaitJob(t, done)
	s.Consume()
	if calls.Load() != 2 || s.Current().Errors["app"] != "" || s.Current().Cache["app"][0].Name != "orders" {
		t.Fatal("explicit retry failed to replace error with metadata")
	}
	s.LoadObjects("app", false, notify)
	if calls.Load() != 2 {
		t.Fatal("cached metadata was read again")
	}
}

func TestSessionBrowserRemovedDatabaseCannotPublishLateObjects(t *testing.T) {
	s, done, notify := connectedState(t)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	started := make(chan context.Context, 1)
	var calls atomic.Int32
	s.ObjectLister = func(ctx context.Context, _ int64, _ string) ([]mysqlsvc.MySQLDBObject, error) {
		calls.Add(1)
		started <- ctx
		<-gate // Model a driver returning success despite cancellation.
		return []mysqlsvc.MySQLDBObject{{Name: "stale_orders", Kind: "表"}}, nil
	}
	s.LoadObjects("app", false, notify)
	var ctx context.Context
	select {
	case ctx = <-started:
	case <-time.After(time.Second):
		t.Fatal("metadata read did not start")
	}
	oldResults := s.Current().objectResults
	s.SelectDatabase("other")
	if s.Current().Pending != "app" || ctx.Err() != nil {
		t.Fatal("single selection canceled its opened sibling")
	}
	s.Current().Databases = []string{"other"}
	s.ReconcileDatabases(s.Current().Databases)
	if ctx.Err() == nil {
		t.Fatal("removed schema retained an outstanding job")
	}
	release()
	awaitJob(t, done)
	// The canceled driver can still deliver to its original buffered channel.
	select {
	case <-oldResults:
	default:
		t.Fatal("canceled worker could not finish delivery")
	}
	s.Consume()
	if _, cached := s.Current().Cache["app"]; cached || s.Current().Pending != "" || s.DatabaseOpened(1, "app") || s.Current().Database != "other" || calls.Load() != 1 {
		t.Fatal("removed schema returned to cache or selected sibling was loaded implicitly")
	}
}

func TestSessionBrowserCancellationPreservesOpenedStateWithoutAutomaticRetry(t *testing.T) {
	s, done, notify := connectedState(t)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	s.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) { <-gate; return nil, nil }
	s.LoadObjects("app", false, notify)
	s.CancelObjects()
	request := s.Current().ObjectRequest()
	s.LoadExpanded(func(string) bool { return true }, notify)
	if s.Current().Errors["app"] != "加载已取消" || !s.DatabaseOpened(1, "app") || s.Current().ObjectRequest() != request {
		t.Fatal("canceled metadata was retried during rendering")
	}
	release()
	awaitJob(t, done)
	s.Consume()
	if _, loaded := s.Current().Cache["app"]; loaded {
		t.Fatal("canceled success entered object cache")
	}
}

func TestSessionBrowserWorkspaceOpeningIsIsolated(t *testing.T) {
	s, _, _ := connectedState(t)
	first := s.Current()
	first.Database = "app"
	s.MarkOpened(1, "app")
	s.Activate(2)
	second := s.Current()
	second.ProfileID, second.Status = 2, "已连接"
	second.Database, second.Databases = "app", []string{"app"}
	if s.DatabaseOpened(2, "app") || !s.DatabaseOpened(1, "app") {
		t.Fatal("same-name schemas shared opened state")
	}
	s.Activate(1)
	if s.Current() != first {
		t.Fatal("workspace identity changed")
	}
	s.SelectDatabase("other")
	if first.Database != "other" {
		t.Fatal("database context did not change")
	}
	s.DisconnectAll()
	if s.HasSessions() || second.Password != "" {
		t.Fatal("close-all retained workspace or password")
	}
}

func TestSessionBrowserRemovedSelectionKeepsKnownSiblingRead(t *testing.T) {
	s, done, notify := connectedState(t)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	started := make(chan context.Context, 1)
	s.ObjectLister = func(ctx context.Context, _ int64, _ string) ([]mysqlsvc.MySQLDBObject, error) {
		started <- ctx
		<-gate
		return []mysqlsvc.MySQLDBObject{{Name: "known", Kind: "表"}}, nil
	}
	s.SelectDatabase("app")
	s.LoadObjects("other", false, notify)
	var ctx context.Context
	select {
	case ctx = <-started:
	case <-time.After(time.Second):
		t.Fatal("sibling read did not start")
	}
	s.Current().Databases = []string{"other"}
	s.ReconcileDatabases(s.Current().Databases)
	if ctx.Err() != nil || s.Current().Pending != "other" || s.Current().Database != "" {
		t.Fatal("removed selection canceled a known sibling's read")
	}
	release()
	awaitJob(t, done)
	s.Consume()
	if s.Current().Cache["other"][0].Name != "known" || s.Current().Database != "" {
		t.Fatal("sibling read was lost or changed selection")
	}
}

func TestDatabaseInvalidationTargetsParkedSessionAndRevokesLateRead(t *testing.T) {
	s, done, notify := connectedState(t)
	original := s.Current()
	s.SelectDatabase("app")
	original.Cache["other"] = []mysqlsvc.MySQLDBObject{{Name: "sibling"}}
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	started := make(chan context.Context, 1)
	var calls atomic.Int32
	s.ObjectLister = func(ctx context.Context, _ int64, _ string) ([]mysqlsvc.MySQLDBObject, error) {
		calls.Add(1)
		started <- ctx
		<-gate // Late success must not restore invalidated metadata.
		return []mysqlsvc.MySQLDBObject{{Name: "stale"}}, nil
	}
	s.LoadObjects("app", true, notify)
	var ctx context.Context
	select {
	case ctx = <-started:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	oldResults := original.objectResults
	s.Activate(2)
	active := s.Current()
	active.ProfileID, active.Status, active.Generation = 2, "已连接", original.Generation+1
	active.Databases = []string{"app", "other"}
	active.Cache["app"] = []mysqlsvc.MySQLDBObject{{Name: "active"}}
	if !s.InvalidateDatabaseObjects(1, original.Generation, "app") {
		t.Fatal("parked original session was not invalidated")
	}
	if ctx.Err() == nil || original.Pending != "" || original.objectResults != nil ||
		original.Database != "app" || !s.DatabaseOpened(1, "app") || original.Cache["other"][0].Name != "sibling" ||
		s.Current() != active || active.Cache["app"][0].Name != "active" {
		t.Fatal("invalidation retained its read or changed unrelated state")
	}
	release()
	awaitJob(t, done)
	if len(oldResults) != 1 {
		t.Fatal("late worker could not finish on its buffered channel")
	}
	s.Activate(1)
	s.Consume()
	if _, loaded := original.Cache["app"]; loaded || calls.Load() != 1 || original.Errors["app"] != "" {
		t.Fatal("invalidation accepted late data or implicitly restarted its read")
	}
}

func TestDatabaseInvalidationPreservesSiblingReadAndRejectsStaleIdentity(t *testing.T) {
	s, done, notify := connectedState(t)
	w := s.Current()
	s.SelectDatabase("app")
	w.Cache["app"] = []mysqlsvc.MySQLDBObject{{Name: "old"}}
	w.Errors["app"] = "old error"
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	s.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		<-gate
		return []mysqlsvc.MySQLDBObject{{Name: "sibling"}}, nil
	}
	s.LoadObjects("other", false, notify)
	request, results := w.objectRequest, w.objectResults
	for _, invalid := range []struct {
		profile    int64
		generation uint64
	}{
		{99, w.Generation}, {1, w.Generation + 1},
	} {
		if s.InvalidateDatabaseObjects(invalid.profile, invalid.generation, "app") ||
			s.InvalidateProfileObjects(invalid.profile, invalid.generation) {
			t.Fatal("invalid session identity accepted")
		}
		if w.Cache["app"][0].Name != "old" || w.Errors["app"] != "old error" || w.objectRequest != request || w.objectResults != results {
			t.Fatal("rejected invalidation changed cache or request")
		}
	}
	w.Status = "未连接"
	if s.InvalidateProfileObjects(1, w.Generation) || s.InvalidateDatabaseObjects(1, w.Generation, "app") {
		t.Fatal("disconnected session accepted invalidation")
	}
	w.Status = "已连接"
	if !s.InvalidateDatabaseObjects(1, w.Generation, "app") {
		t.Fatal("current session rejected")
	}
	if _, loaded := w.Cache["app"]; loaded || w.Errors["app"] != "" || w.Pending != "other" ||
		w.objectRequest != request || w.objectResults != results || w.Database != "app" {
		t.Fatal("database invalidation changed its sibling's pending read")
	}
	release()
	awaitJob(t, done)
	s.Consume()
	if w.Cache["other"][0].Name != "sibling" {
		t.Fatal("sibling completion lost")
	}
}

func TestProfileInvalidationClearsOnlyOriginalMetadata(t *testing.T) {
	s, done, notify := connectedState(t)
	w := s.Current()
	s.SelectDatabase("app")
	s.MarkOpened(1, "app")
	w.Cache["app"] = []mysqlsvc.MySQLDBObject{{Name: "old"}}
	w.Errors["app"] = "old error"
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	s.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		<-gate
		return []mysqlsvc.MySQLDBObject{{Name: "late"}}, nil
	}
	s.LoadObjects("other", false, notify)
	s.Activate(2)
	active := s.Current()
	active.ProfileID, active.Status, active.Generation = 2, "已连接", w.Generation+1
	active.Databases = []string{"app", "other"}
	active.Cache["app"] = []mysqlsvc.MySQLDBObject{{Name: "active"}}
	active.Errors["other"] = "active error"
	if !s.InvalidateProfileObjects(1, w.Generation) || w.Pending != "" || len(w.Cache) != 0 || len(w.Errors) != 0 ||
		w.Database != "app" || !s.DatabaseOpened(1, "app") || !s.DatabaseOpened(1, "other") ||
		active.Cache["app"][0].Name != "active" || active.Errors["other"] != "active error" || s.Current() != active {
		t.Fatal("profile invalidation changed selection/opened state or another workspace")
	}
	release()
	awaitJob(t, done)
	s.Activate(1)
	s.Consume()
	if len(w.Cache) != 0 || w.Pending != "" {
		t.Fatal("profile invalidation accepted a late completion")
	}
}
