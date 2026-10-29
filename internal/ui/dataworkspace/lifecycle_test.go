package dataworkspace

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestPageWorkerOwnsCursorAndParkedResult(t *testing.T) {
	session := Session{ProfileID: 12, Generation: 2, Connected: true}
	s := New(Dependencies{Session: func() Session { return session }})
	d := s.Open(1, 12, "db", "orders")
	d.after = &mysqlsvc.MySQLDataCursor{Columns: []string{"id"}, Values: [][]byte{[]byte("1")}, Descending: []bool{false}}
	started, release := make(chan struct{}), make(chan struct{})
	received := make(chan mysqlsvc.MySQLDataOptions, 1)
	s.AdvancedLoader = func(_ context.Context, _ int64, _, _ string, options mysqlsvc.MySQLDataOptions) (*mysqlsvc.MySQLDataPage, error) {
		close(started)
		<-release
		received <- options
		return &mysqlsvc.MySQLDataPage{Columns: []string{"id"}}, nil
	}
	s.Load(ui.Services{}, d, false)
	<-started
	d.after.Columns[0], d.after.Values[0][0], d.after.Descending[0] = "changed", '9', true
	close(release)
	options := <-received
	if options.After.Columns[0] != "id" || string(options.After.Values[0]) != "1" || options.After.Descending[0] {
		t.Fatal("worker shared a mutable page cursor")
	}
	// Result remains registered and buffered while another connection is active.
	session.ProfileID = 13
	s.Consume(ui.Services{})
	if !d.loading || d.page != nil {
		t.Fatal("parked page was applied to another profile")
	}
	session.ProfileID = 12
	wait := d.results
	r := <-wait
	out := make(chan mysqlDataResult, 1)
	out <- r
	d.results = out
	s.Consume(ui.Services{})
	if d.loading || d.page == nil {
		t.Fatal("parked result was lost")
	}
	s.Close(d)
	s.Close(d)
	if len(s.documents) != 0 || d.page != nil || d.results != nil {
		t.Fatal("close retained page state")
	}
	s.Load(ui.Services{}, d, true)
	if d.loading {
		t.Fatal("closed document accepted another request")
	}
}

func TestSaveWorkerOwnsDraftAndVerificationRequest(t *testing.T) {
	s, d, _ := dataTestWorkspace(t)
	fp := "fingerprint"
	ref := mysqlsvc.MySQLRowRef{Columns: []string{"id"}, Values: [][]byte{[]byte("1")}}
	original := &mysqlsvc.MySQLRowSnapshot{Ref: ref, Columns: []string{"id"}, Fingerprints: []*string{&fp}}
	values := map[string]mysqlsvc.MySQLDataValue{"payload": {Binary: true, Bytes: []byte("abc")}}
	d.edit.Drafts = map[string]*mysqlRowDraft{"row": {Row: 0, Kind: "update", Original: original, Values: values}}
	started, release := make(chan struct{}), make(chan struct{})
	received := make(chan []mysqlsvc.MySQLDataEdit, 1)
	s.Saver = func(_ context.Context, _ int64, _, _ string, edits []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataEditResult, error) {
		close(started)
		<-release
		received <- edits
		return nil, context.Canceled
	}
	s.saveMySQLData(ui.Services{}, d)
	<-started
	original.Ref.Values[0][0], original.Columns[0], fp = '9', "changed", "changed"
	value := values["payload"]
	value.Bytes[0] = 'z'
	values["extra"] = mysqlsvc.MySQLDataValue{Text: "new"}
	close(release)
	edits := <-received
	if string(edits[0].Original.Ref.Values[0]) != "1" || edits[0].Original.Columns[0] != "id" || *edits[0].Original.Fingerprints[0] != "fingerprint" || string(edits[0].Values["payload"].Bytes) != "abc" || len(edits[0].Values) != 1 {
		t.Fatal("save worker shared UI draft values")
	}
	s.cancelMySQLDataEdit(d)
	d.edit.Uncertain = &mysqlsvc.MySQLDataEditResult{Outcomes: []mysqlsvc.MySQLDataEditOutcome{{Saved: original}}}
	started, release = make(chan struct{}), make(chan struct{})
	verified := make(chan *mysqlsvc.MySQLDataEditResult, 1)
	s.Verify = func(_ context.Context, _ int64, _, _ string, result *mysqlsvc.MySQLDataEditResult) (*mysqlsvc.MySQLDataVerification, error) {
		close(started)
		<-release
		verified <- result
		return nil, context.Canceled
	}
	s.verifyMySQLDataSave(ui.Services{}, d)
	<-started
	original.Ref.Values[0][0] = '7'
	close(release)
	if string((<-verified).Outcomes[0].Saved.Ref.Values[0]) != "9" {
		t.Fatal("verification shared mutable row identities")
	}
	s.Close(d)
}

func TestEditGenerationMismatchSettlesWithoutReplayOrLeave(t *testing.T) {
	session := Session{ProfileID: 12, Generation: 4, Connected: true}
	s := New(Dependencies{Session: func() Session { return session }})
	d := s.Open(1, 12, "db", "orders")
	d.edit.Drafts = map[string]*mysqlRowDraft{"row": {Kind: "update", Values: map[string]mysqlsvc.MySQLDataValue{"name": {Text: "draft"}}}}
	d.edit.Busy, d.edit.Request = true, 1
	called := false
	d.edit.AfterSave = func(ui.Services) { called = true }
	out := make(chan mysqlEditResult, 1)
	d.edit.Results = out
	out <- mysqlEditResult{Generation: 3, Request: 1, Profile: 12, Keys: []string{"row"}, Saved: &mysqlsvc.MySQLDataEditResult{}}
	s.Consume(ui.Services{})
	if d.edit.Busy || d.edit.Results != nil || called || !d.HasDraft() || d.edit.Uncertain == nil || !s.GuardLeave(d, nil) {
		t.Fatal("invalidated write lost draft, stayed busy or executed leave")
	}
	reads := make(chan mysqlEditResult, 1)
	d.edit.Results = reads
	d.edit.Busy = true
	reads <- mysqlEditResult{Generation: 3, Request: 1, Profile: 12, Field: "name"}
	s.Consume(ui.Services{})
	if d.edit.Busy || d.edit.Err == "" || d.edit.Uncertain == nil {
		t.Fatal("invalidated field read failed to settle")
	}
	s.Close(d)
}

func TestInspectorCloseReleasesSelectionAndCaches(t *testing.T) {
	s := newInspector(InspectorDependencies{})
	p := s.Open(12, "db", "orders", func() Selection { return Selection{Row: 0} })
	p.info.info = &mysqlsvc.MySQLTableInfo{}
	p.chunkText = "cached"
	s.Close(p)
	s.Close(p)
	if p.selection != nil || p.Info() != nil || p.chunkText != "" || len(s.panels) != 0 {
		t.Fatal("closed inspector retained its document")
	}
}
