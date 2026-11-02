package dataworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDataActivityReadCloseAndScopeChangesKeepCapturedRequest(t *testing.T) {
	session := Session{ProfileID: 7, Generation: 31, Connected: true}
	started, release := make(chan struct{}), make(chan struct{})
	results := make(chan contract.ActivityResult, 1)
	var request contract.ActivityRequest
	s := New(Dependencies{Session: func() Session { return session }, RecordActivity: func(v contract.ActivityRequest) func(contract.ActivityResult) {
		request = v
		return func(out contract.ActivityResult) { results <- out }
	}})
	d := s.Open(42, 7, "original", "entries")
	d.filterColumn, d.filterValue = "secret", "private-value"
	s.AdvancedLoader = func(ctx context.Context, profile int64, database, table string, options contract.DataOptions) (*contract.DataPage, error) {
		if profile != 7 || database != "original" || options.FilterValue != "private-value" {
			t.Error("read lost original request")
		}
		close(started)
		<-release
		return nil, errors.Join(ctx.Err(), errors.New("password=private-value"))
	}
	s.Load(ui.Services{}, d, false)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	s.Close(d)
	session = Session{ProfileID: 8, Generation: 32, Connected: true}
	select {
	case <-results:
		t.Fatal("close logged before actual read receipt")
	default:
	}
	close(release)
	select {
	case out := <-results:
		if request.ProfileID != 7 || request.Generation != 31 || request.Database != "original" || request.Object != "entries" || !request.Summary || request.Source != "data_read" || out.State != "cancelled" || strings.Contains(request.SQL+out.Error, "private-value") {
			t.Fatalf("unsafe/rebound global read: %+v %+v", request, out)
		}
	case <-time.After(time.Second):
		t.Fatal("read close discarded global receipt")
	}
}

func TestDataActivityPendingAndUncertainWritesRemainDistinct(t *testing.T) {
	saved := &contract.DataEditResult{Outcomes: make([]contract.DataEditOutcome, 2)}
	if got := dataActivityOutcome(nil, saved, 2, true); got.State != "pending" {
		t.Fatal("uncommitted transaction logged as committed")
	}
	saved.CommitUncertain = true
	if got := dataActivityOutcome(nil, saved, 2, false); got.State != "uncertain" {
		t.Fatal("uncertain commit logged as successful")
	}
	saved.CommitUncertain = false
	saved.Outcomes = saved.Outcomes[:1]
	if got := dataActivityOutcome(nil, saved, 2, false); got.State != "partial" {
		t.Fatal("incomplete receipt logged as successful")
	}
}
