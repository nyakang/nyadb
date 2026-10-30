package dataworkspace

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestDataImportModesRequireKeysAndSecondReplaceConfirmation(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	d.transfer.Mode = "update"
	if _, _, err := mysqlDataImportRequest(d); err == nil {
		t.Fatal("update allowed no matching identity")
	}
	d.transfer.MatchColumns = []string{"id"}
	if _, _, err := mysqlDataImportRequest(d); err != nil {
		t.Fatal("mapped primary key rejected", err)
	}
	d.transfer.Mode = "replace"
	called := false
	s.Importer = func(context.Context, int64, string, string, mysqlsvc.MySQLDataImportRequest) (*mysqlsvc.MySQLDataImportResult, error) {
		called = true
		return nil, nil
	}
	s.startDataImport(ui.Services{}, d)
	if called || d.transfer.Busy || mysqlDataImportConfirmed(d) {
		t.Fatal("replace started without separate clear confirmation")
	}
	d.transfer.DestructiveConfirmation = "t"
	if !mysqlDataImportConfirmed(d) {
		t.Fatal("matching confirmations rejected")
	}
}

func TestDataImportModeWorkerOwnsSettingsAndBoundedBatch(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	d.transfer.Mode = "upsert"
	d.transfer.MatchColumns = []string{"id"}
	d.transfer.ContinueOnError = true
	for range 201 {
		d.transfer.Input.Rows = append(d.transfer.Input.Rows, []mysqlsvc.MySQLDataValue{{Text: "2"}})
	}
	started, release := make(chan struct{}), make(chan struct{})
	received := make(chan mysqlsvc.MySQLDataImportRequest, 1)
	s.Importer = func(_ context.Context, id int64, db, table string, request mysqlsvc.MySQLDataImportRequest) (*mysqlsvc.MySQLDataImportResult, error) {
		if id != 12 || db != "db" || table != "t" {
			return nil, errors.New("wrong destination")
		}
		close(started)
		<-release
		received <- request
		return &mysqlsvc.MySQLDataImportResult{}, errors.New("stopped before write")
	}
	s.startDataImport(ui.Services{}, d)
	<-started
	d.transfer.Mode, d.transfer.MatchColumns[0], d.transfer.ContinueOnError = "delete", "wrong", false
	d.transfer.Input.Rows[0][0].Text = "later"
	close(release)
	request := <-received
	if request.Mode != "upsert" || !request.ContinueOnError || !reflect.DeepEqual(request.MatchColumns, []string{"id"}) || len(request.Edits) != 202 || request.Edits[0].Values["id"].Text != "1" {
		t.Fatal("worker inherited changed UI or rejected larger import", request)
	}
	waitDataTransfer(t, s, d)
}

func TestDataImportModePartialCompletionUsesActualRowsAndSafeHistory(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	d.transfer.Mode, d.transfer.ContinueOnError = "append", true
	d.transfer.Input.Rows = append(d.transfer.Input.Rows, []mysqlsvc.MySQLDataValue{{Text: "2"}}, []mysqlsvc.MySQLDataValue{{Text: "3"}})
	s.Importer = func(context.Context, int64, string, string, mysqlsvc.MySQLDataImportRequest) (*mysqlsvc.MySQLDataImportResult, error) {
		return &mysqlsvc.MySQLDataImportResult{Committed: true, Rows: []mysqlsvc.MySQLDataImportRowResult{
			{Row: 1, Kind: "insert", Status: "committed"},
			{Row: 2, Kind: "skip", Status: "skipped"},
			{Row: 3, Kind: "error", Status: "failed", Error: "MySQL error 1062"},
		}}, nil
	}
	s.startDataImport(ui.Services{}, d)
	waitDataTransfer(t, s, d)
	if !d.transfer.Completed || d.transfer.Uncertain || !d.transfer.Open || d.transfer.Err != "已提交 1 行 · 跳过 1 行 · 失败 1 行" {
		t.Fatal("partial result was reported as full or uncertain", d.transfer)
	}
	if d.tools.historyStats.Insert != 2 || d.tools.historyStats.Errors != 1 {
		t.Fatal("actual action and failed summary absent", d.tools.historyStats)
	}
	for _, entry := range d.tools.history {
		if strings.Contains(entry.Message, "1062") || entry.Rows > 1 {
			t.Fatal("history used diagnostics or requested row counts", entry)
		}
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.transferModals(c, d) }, 1000, 1000)
	if !tt.HasText("逐行导入结果") || !tt.HasText("第 3 行 · 处理 · 失败 · MySQL error 1062") {
		t.Fatal("row outcome details not visible", tt.Texts())
	}
}

func TestDataImportModeWaitsForCancellationAndParkedResults(t *testing.T) {
	s, d, session := transferTestDocument()
	defer s.Close(d)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	s.Importer = func(ctx context.Context, _ int64, _, _ string, _ mysqlsvc.MySQLDataImportRequest) (*mysqlsvc.MySQLDataImportResult, error) {
		calls++
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return &mysqlsvc.MySQLDataImportResult{Rows: []mysqlsvc.MySQLDataImportRowResult{{Row: 1, Kind: "insert", Status: "rolled_back"}}}, context.Canceled
	}
	s.startDataImport(ui.Services{}, d)
	<-started
	s.stopDataTransfer(d)
	<-canceled
	s.consumeDataTransfers(ui.Services{})
	if !d.transfer.Busy || !d.transfer.Canceling {
		t.Fatal("settled before executor acknowledgment")
	}
	session.ProfileID = 13
	close(release)
	deadline := time.Now().Add(time.Second)
	for len(d.transfer.Results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s.consumeDataTransfers(ui.Services{})
	if !d.transfer.Busy {
		t.Fatal("parked import consumed under another profile")
	}
	session.ProfileID = 12
	waitDataTransfer(t, s, d)
	if d.transfer.Uncertain || !d.transfer.Submitted || d.transfer.Completed || d.transfer.RowResults[0].Status != "rolled_back" {
		t.Fatal("known canceled rollback became replayable or complete", d.transfer)
	}
	s.startDataImport(ui.Services{}, d)
	if calls != 1 {
		t.Fatal("canceled submission replayed")
	}
}

func TestDataImportModeStaleOrIncompleteResultsRequireVerification(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "incomplete", true: "stale"}[stale], func(t *testing.T) {
			s, d, session := transferTestDocument()
			defer s.Close(d)
			out := make(chan mysqlDataTransferResult, 1)
			d.transfer.Busy, d.transfer.Submitted, d.transfer.Results = true, true, out
			imported := &mysqlsvc.MySQLDataImportResult{Committed: true}
			if stale {
				imported.Rows = []mysqlsvc.MySQLDataImportRowResult{{Row: 1, Kind: "insert", Status: "committed"}}
				session.Generation++
			}
			out <- mysqlDataTransferResult{Imported: imported, Request: d.transfer.Request, Generation: 2, Profile: 12, Write: true, Destination: "t", Rows: 1}
			s.consumeDataTransfers(ui.Services{})
			if d.transfer.Completed || !d.transfer.Uncertain || d.edit.Uncertain == nil {
				t.Fatal("unproven result treated as complete", d.transfer)
			}
			if stale && d.transfer.RowResults[0].Status != "uncertain" {
				t.Fatal("stale generation claimed verified committed row")
			}
		})
	}
}

func TestDataImportModeContradictoryCommitReplyNeverClaimsSuccess(t *testing.T) {
	s, d, _ := transferTestDocument()
	defer s.Close(d)
	out := make(chan mysqlDataTransferResult, 1)
	d.transfer.Busy, d.transfer.Submitted, d.transfer.Results = true, true, out
	out <- mysqlDataTransferResult{Imported: &mysqlsvc.MySQLDataImportResult{Committed: true}, Request: d.transfer.Request, Generation: 2, Profile: 12, Write: true, Destination: "t", Rows: 1, Err: errors.New("reply incomplete")}
	s.consumeDataTransfers(ui.Services{})
	if !d.transfer.Uncertain || d.transfer.Completed || d.edit.Uncertain == nil || d.tools.historyStats.Errors != 1 {
		t.Fatal("contradictory commit reply escaped verification", d.transfer)
	}
}
