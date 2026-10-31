package dataworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func completionWizard(t *testing.T, kind string) (*State, *Document, *Session) {
	t.Helper()
	s, d, session := transferTestDocument()
	t.Cleanup(func() { s.Close(d) })
	s.openDataTransfer(d, kind)
	return s, d, session
}

func completionReadRows(t *testing.T, s *State, d *Document, value string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rows.csv")
	if err := os.WriteFile(path, []byte("id\n"+value+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s.readDataImportPath(ui.Services{}, d, path)
	waitDataTransfer(t, s, d)
	if d.transfer.Input == nil || d.transfer.TotalRows != 1 {
		t.Fatal("source was not completely read", d.transfer.Err)
	}
}

func completionPrepare(t *testing.T, s *State, d *Document) {
	t.Helper()
	s.prepareDataImport(ui.Services{}, d)
	waitDataTransfer(t, s, d)
	if d.transfer.MappedSpool == nil {
		t.Fatal("source was not prepared", d.transfer.Err)
	}
	d.transfer.Confirmation = mysqlDataImportDestination(d)
}

func completionRecordRows(ctx context.Context, r mysqlsvc.MySQLDataImportStreamRequest) (mysqlsvc.MySQLDataImportProgress, error) {
	reader, err := r.Open(ctx)
	if err != nil {
		return mysqlsvc.MySQLDataImportProgress{}, err
	}
	defer reader.Close()
	p := mysqlsvc.MySQLDataImportProgress{}
	for {
		_, err := reader.Next(ctx)
		if errors.Is(err, io.EOF) {
			return p, nil
		}
		if err != nil {
			return p, err
		}
		if err := r.Record(mysqlsvc.MySQLDataImportRowResult{Row: int(p.Processed + 1), Kind: "insert", Status: "prepared"}); err != nil {
			return p, err
		}
		p.Processed++
		p.Prepared++
	}
}

func TestDataTransferCompletionWizardFooterRemainsVisibleWhileBodyScrolls(t *testing.T) {
	for _, size := range [][2]int{{1280, 800}, {900, 650}} {
		for _, kind := range []string{"import", "export"} {
			t.Run(fmt.Sprintf("%s-%dx%d", kind, size[0], size[1]), func(t *testing.T) {
				s, d, _ := completionWizard(t, kind)
				tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.transferModals(c, d) }, size[0], size[1])
				var footerY float32
				for step := range mysqlDataWizardSteps(kind) {
					d.transfer.Step = step
					d.transfer.Submitted = step == len(mysqlDataWizardSteps(kind))-1
					d.transfer.Err = strings.Repeat("overflowing body content\n", 120)
					tt.Frame()
					label := "取消"
					if d.transfer.Submitted {
						label = "关闭"
					}
					r, ok := tt.Find(label)
					if !ok || r.W <= 0 || r.H <= 0 || r.X < 20 || r.Y < 20 || r.X+r.W > float32(size[0]-20) || r.Y+r.H > float32(size[1]-20) {
						t.Fatalf("step %d footer outside client: %+v (%v)", step, r, ok)
					}
					if step == 0 {
						footerY = r.Y
					} else if r.Y != footerY {
						t.Fatalf("step %d body moved fixed footer: %g != %g", step, r.Y, footerY)
					}
					tt.Scroll(float32(size[0])/2, float32(size[1])/2, 0, 900)
					after, _ := tt.Find(label)
					if after != r {
						t.Fatalf("scroll moved footer: %+v != %+v", after, r)
					}
				}
			})
		}
	}
}

func TestDataTransferCompletionImportVisitsAllStepsAndKeyboardRequiresReview(t *testing.T) {
	s, d, _ := completionWizard(t, "import")
	path := filepath.Join(t.TempDir(), "rows.csv")
	if err := os.WriteFile(path, []byte("id\n9007199254740993\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d.transfer.OpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{path}, nil }
	calls := 0
	s.StreamImporter = func(ctx context.Context, id int64, db, table string, r mysqlsvc.MySQLDataImportStreamRequest) (*mysqlsvc.MySQLDataImportStreamResult, error) {
		calls++
		if id != 12 || db != "db" || table != "t" {
			return nil, errors.New("original destination lost")
		}
		p, err := completionRecordRows(ctx, r)
		return &mysqlsvc.MySQLDataImportStreamResult{Committed: err == nil, Progress: p}, err
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.consumeDataTransfers(c.Services()); s.transferModals(c, d) }, 900, 650)
	if err := tt.Click("下一步"); err == nil && d.transfer.Step != 0 {
		t.Fatal("unread source advanced")
	}
	if err := tt.Click("选择文件"); err != nil {
		t.Fatal(err)
	}
	waitDataTransfer(t, s, d)
	tt.Frame()
	for step := 1; step <= 4; step++ {
		if err := tt.Click("下一步"); err != nil {
			t.Fatal(err)
		}
		if d.transfer.Step != step || !tt.HasText("导入向导 · "+mysqlDataWizardSteps("import")[step]) {
			t.Fatal("missing actual step transition", step, tt.Texts())
		}
	}
	// Tab and Enter reach the disabled submit button without bypassing the
	// asynchronous complete-source review and typed destination guard.
	for i := 0; i < 40 && !tt.Focused("确认导入"); i++ {
		tt.Key(0, ui.KeyTab)
	}
	tt.Key(0, ui.KeyEnter)
	if d.transfer.Submitted || calls != 0 {
		t.Fatal("keyboard bypassed complete review")
	}
	if err := tt.Click("完整校验全部源记录"); err != nil {
		t.Fatal(err)
	}
	waitDataTransfer(t, s, d)
	tt.Frame()
	if err := tt.Click("输入表名称以确认导入"); err != nil {
		t.Fatal(err)
	}
	tt.Type("t")
	for i := 0; i < 40 && !tt.Focused("确认导入"); i++ {
		tt.Key(0, ui.KeyTab)
	}
	if !tt.Focused("确认导入") {
		t.Fatal("submit not reachable by Tab")
	}
	tt.Key(0, ui.KeyEnter)
	waitDataTransfer(t, s, d)
	tt.Frame()
	if calls != 1 || d.transfer.Step != 5 || !d.transfer.Completed || d.transfer.Uncertain || !tt.HasText("导入向导 · 执行结果") {
		t.Fatal("reviewed keyboard submission did not settle once", calls, d.transfer)
	}
}

func TestDataTransferCompletionExportVisitsAllStepsAndWritesChosenFields(t *testing.T) {
	s, d, _ := completionWizard(t, "export")
	d.page.Columns = []string{"id", "note"}
	d.page.ColumnMetadata = transferColumns()[:2]
	d.page.Rows = [][]mysqlsvc.MySQLDataCell{{{Text: "1"}, {Text: "中文"}}}
	s.openDataExport(d)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.consumeDataTransfers(c.Services()); s.transferModals(c, d) }, 900, 650)
	for step := 1; step <= 3; step++ {
		if err := tt.Click("下一步"); err != nil {
			t.Fatal(err)
		}
		if d.transfer.Step != step || !tt.HasText("导出向导 · "+mysqlDataWizardSteps("export")[step]) {
			t.Fatal("missing export step", step, tt.Texts())
		}
		if step == 1 {
			if err := tt.Click("note"); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := filepath.Join(t.TempDir(), "chosen.csv")
	if err := tt.Click("目标文件路径"); err != nil {
		t.Fatal(err)
	}
	tt.Type(path)
	if err := tt.Click("导出"); err != nil {
		t.Fatal(err)
	}
	waitDataTransfer(t, s, d)
	tt.Frame()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "id\n1\n" || !d.transfer.Completed || !tt.HasText("导出向导 · 执行结果") {
		t.Fatal("export did not preserve chosen fields", string(data), err, d.transfer.Err)
	}
	if !tt.HasText("关闭") || tt.HasText("上一步") || !d.transfer.Submitted {
		t.Fatal("terminal export remained replayable", tt.Texts())
	}
}

func TestDataTransferCompletionSavePickerIgnoresOutsideClickUntilReturn(t *testing.T) {
	for _, sameFrame := range []bool{false, true} {
		t.Run(fmt.Sprintf("returned-before-click-%t", sameFrame), func(t *testing.T) {
			s, d, _ := completionWizard(t, "export")
			d.page.Rows = [][]mysqlsvc.MySQLDataCell{{{Text: "1"}}}
			d.transfer.Step = 3
			started, release := make(chan struct{}), make(chan struct{})
			path := filepath.Join(t.TempDir(), "chosen.csv")
			d.transfer.SaveDialog = func(mygo.SaveDialogOptions) (string, error) {
				close(started)
				<-release
				return path, nil
			}
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.consumeDataTransfers(c.Services()); s.transferModals(c, d) }, 900, 650)
			if err := tt.Click("选择目标文件"); err != nil {
				t.Fatal(err)
			}
			<-started
			if sameFrame {
				close(release)
				deadline := time.Now().Add(time.Second)
				for len(d.transfer.Results) == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
			}
			// Native chooser clicks can land beyond the underlying overlay.
			// Neither a pending chooser nor its return frame cancels the owner.
			tt.Press(5, 5)
			tt.Frame()
			tt.Release(5, 5)
			if !d.transfer.Open || d.transfer.Canceling || d.transfer.Submitted {
				if !sameFrame {
					close(release)
				}
				t.Fatal("native picker dismissed or cancelled the wizard", sameFrame)
			}
			if !sameFrame {
				close(release)
			}
			waitDataTransfer(t, s, d)
			tt.Frame()
			if d.transfer.ExportPath != path || !d.transfer.Open || d.transfer.Canceling {
				t.Fatal("picker return lost chosen path", d.transfer.ExportPath, d.transfer.Err)
			}
			if err := tt.Click("导出"); err != nil {
				t.Fatal(err)
			}
			waitDataTransfer(t, s, d)
			tt.Frame()
			if data, err := os.ReadFile(path); err != nil || string(data) != "id\n1\n" || !d.transfer.Completed {
				t.Fatal("picker click cancelled subsequent explicit export", string(data), err)
			}
		})
	}
}

func TestDataTransferCompletionPickerAcknowledgementDoesNotHideExportCancellation(t *testing.T) {
	s, d, _ := completionWizard(t, "export")
	d.transfer.Scope, d.transfer.Step = "all", 4
	pickerStarted, pickerRelease := make(chan struct{}), make(chan struct{})
	executorStarted, executorCancelled, executorRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	path := filepath.Join(t.TempDir(), "cancelled.csv")
	d.transfer.SaveDialog = func(mygo.SaveDialogOptions) (string, error) {
		close(pickerStarted)
		<-pickerRelease
		return path, nil
	}
	s.Exporter = func(ctx context.Context, _ int64, _, _ string, _ mysqlsvc.MySQLDataOptions, _ []string, _ func([]mysqlsvc.MySQLDataValue) error) error {
		close(executorStarted)
		<-ctx.Done()
		close(executorCancelled)
		<-executorRelease
		return ctx.Err()
	}
	s.startDataExport(ui.Services{}, d)
	<-pickerStarted
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.consumeDataTransfers(c.Services()); s.transferModals(c, d) }, 900, 650)
	tt.ClickAt(5, 5)
	if d.transfer.Canceling || !d.transfer.PickerActive {
		close(pickerRelease)
		close(executorRelease)
		t.Fatal("native picker was mistaken for cancellable export")
	}
	close(pickerRelease)
	<-executorStarted
	tt.Frame()
	if d.transfer.PickerActive || !d.transfer.Busy {
		close(executorRelease)
		t.Fatal("picker did not relinquish its dismissal guard")
	}
	tt.Key(0, ui.KeyEscape)
	select {
	case <-executorCancelled:
	case <-time.After(time.Second):
		close(executorRelease)
		t.Fatal("returned picker hid real export cancellation")
	}
	if !d.transfer.Busy || !d.transfer.Canceling || !d.transfer.Open {
		close(executorRelease)
		t.Fatal("export discarded executor before acknowledgement")
	}
	close(executorRelease)
	waitDataTransfer(t, s, d)
	if d.transfer.Completed || d.transfer.Busy || d.transfer.Canceling || d.transfer.PickerActive {
		t.Fatal("cancelled export did not settle safely")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled export published a file", err)
	}
}

func TestDataTransferCompletionReturningPickerAllowsFirstExecutorEscape(t *testing.T) {
	s, d, _ := completionWizard(t, "export")
	d.transfer.Scope, d.transfer.Step = "all", 4
	pickerStarted, pickerRelease := make(chan struct{}), make(chan struct{})
	executorStarted, executorCancelled, executorRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d.transfer.SaveDialog = func(mygo.SaveDialogOptions) (string, error) {
		close(pickerStarted)
		<-pickerRelease
		return filepath.Join(t.TempDir(), "cancelled.csv"), nil
	}
	s.Exporter = func(ctx context.Context, _ int64, _, _ string, _ mysqlsvc.MySQLDataOptions, _ []string, _ func([]mysqlsvc.MySQLDataValue) error) error {
		close(executorStarted)
		<-ctx.Done()
		close(executorCancelled)
		<-executorRelease
		return ctx.Err()
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.consumeDataTransfers(c.Services()); s.transferModals(c, d) }, 900, 650)
	s.startDataExport(ui.Services{}, d)
	<-pickerStarted
	close(pickerRelease)
	<-executorStarted
	// No intervening frame consumes PickerDone. The first Escape already
	// belongs to the executor, rather than to the completed native chooser.
	tt.Key(0, ui.KeyEscape)
	select {
	case <-executorCancelled:
	case <-time.After(time.Second):
		close(executorRelease)
		t.Fatal("first Escape was swallowed by returned native picker")
	}
	if !d.transfer.Canceling || !d.transfer.Busy || d.transfer.PickerActive {
		close(executorRelease)
		t.Fatal("executor cancellation did not retain acknowledgement guard")
	}
	close(executorRelease)
	waitDataTransfer(t, s, d)
}

func TestDataTransferCompletionPresetsRestoreCurrentFieldsAndRemainVersionOne(t *testing.T) {
	s, d, _ := completionWizard(t, "export")
	d.transfer.Fields = []string{"id", "note"}
	d.transfer.Included = map[string]bool{"id": true, "note": false}
	d.transfer.PresetName = "subset"
	saved := ""
	s.SetDataLayoutPreferences(func(string) (string, error) { return saved, nil }, func(_ string, value string) error { saved = value; return nil })
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.dataTransferPresetControls(c, d) }, 800, 500)
	if err := tt.Click("保存向导配置"); err != nil {
		t.Fatal(err)
	}
	var presets mysqlDataTransferPresets
	if err := json.Unmarshal([]byte(saved), &presets); err != nil || presets.Version != 1 || len(presets.Items) != 1 {
		t.Fatal("preset storage compatibility lost", saved, err)
	}
	d.transfer.Fields = []string{"note", "id", "added"}
	d.transfer.Included = map[string]bool{"note": true, "id": false, "added": true}
	d.transfer.Confirmation, d.transfer.PreparationKey = "reviewed", "prepared"
	tt.Frame()
	if err := tt.Click("加载向导配置"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.transfer.Included, map[string]bool{"note": false, "id": true, "added": false}) || d.transfer.Confirmation != "" || d.transfer.PreparationKey != "" {
		t.Fatal("load reused review or lost subset", d.transfer.Included)
	}
	// Legacy version-1 JSON intentionally omits the new optional field.
	presets.Items[0].Included = nil
	legacy, _ := json.Marshal(presets)
	saved = string(legacy)
	d.transfer.Included["note"] = true
	tt.Frame()
	if err := tt.Click("加载向导配置"); err != nil || !d.transfer.Included["note"] {
		t.Fatal("legacy preset changed current selection", err)
	}
	if err := tt.Click("删除向导配置"); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(saved), &presets); err != nil || len(presets.Items) != 0 {
		t.Fatal("delete did not persist", saved, err)
	}
}

func TestDataTransferCompletionLaterTargetRollbackPreservesFirstCommit(t *testing.T) {
	s, d, _ := completionWizard(t, "import")
	completionReadRows(t, s, d, "1")
	completionPrepare(t, s, d)
	s.addWizardSource(d)
	completionReadRows(t, s, d, "2")
	d.transfer.TargetTable = "second"
	d.transfer.TargetColumns = transferColumns()[:1]
	completionPrepare(t, s, d)
	calls := []string{}
	events := []TableImportEvent{}
	s.deps.TableImported = func(_ ui.Services, e TableImportEvent) { events = append(events, e) }
	s.StreamImporter = func(ctx context.Context, _ int64, _, table string, r mysqlsvc.MySQLDataImportStreamRequest) (*mysqlsvc.MySQLDataImportStreamResult, error) {
		calls = append(calls, table)
		p, err := completionRecordRows(ctx, r)
		if table == "second" {
			return &mysqlsvc.MySQLDataImportStreamResult{Progress: p}, errors.New("known rollback")
		}
		return &mysqlsvc.MySQLDataImportStreamResult{Committed: err == nil, Progress: p}, err
	}
	s.startPreparedDataImport(ui.Services{}, d)
	waitDataTransfer(t, s, d)
	if !reflect.DeepEqual(calls, []string{"t", "second"}) || d.transfer.Completed || d.transfer.Uncertain || !d.transfer.Submitted || len(events) != 1 || events[0].Table != "t" || !events[0].Completed {
		t.Fatal("later rollback changed first target receipt", calls, events, d.transfer)
	}
	for index, expected := range []string{"committed", "rolled_back"} {
		committed, uncertain := mysqlDataBatchRowSettlement(d.transfer.Batch, int64(index), false)
		row, err := d.transfer.Journal.Row(int64(index), committed, uncertain)
		if err != nil || row.Status != expected || row.Row != index+1 {
			t.Fatal("per-target journal settlement lost", row, err)
		}
	}
	s.startPreparedDataImport(ui.Services{}, d)
	if len(calls) != 2 {
		t.Fatal("failed multi-target wizard replayed")
	}
}

func TestDataTransferCompletionParkedCommitRetainsOriginalGeneration(t *testing.T) {
	s, d, session := completionWizard(t, "import")
	completionReadRows(t, s, d, "1")
	completionPrepare(t, s, d)
	started, release := make(chan struct{}), make(chan struct{})
	events := []TableImportEvent{}
	s.deps.TableImported = func(_ ui.Services, e TableImportEvent) { events = append(events, e) }
	s.StreamImporter = func(ctx context.Context, _ int64, _, _ string, r mysqlsvc.MySQLDataImportStreamRequest) (*mysqlsvc.MySQLDataImportStreamResult, error) {
		close(started)
		<-release
		p, err := completionRecordRows(ctx, r)
		return &mysqlsvc.MySQLDataImportStreamResult{Committed: err == nil, Progress: p}, err
	}
	s.startPreparedDataImport(ui.Services{}, d)
	<-started
	session.ProfileID = 99
	close(release)
	deadline := time.Now().Add(time.Second)
	for len(d.transfer.Results) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s.consumeDataTransfers(ui.Services{})
	if !d.transfer.Busy || len(events) != 0 {
		t.Fatal("parked completion affected active profile")
	}
	session.ProfileID, session.Generation = 12, 3
	waitDataTransfer(t, s, d)
	if !d.transfer.Uncertain || d.transfer.Completed || len(events) != 1 || events[0].ProfileID != 12 || events[0].Generation != 2 || !events[0].Uncertain {
		t.Fatal("stale receipt was applied to new session", events, d.transfer)
	}
}

func TestDataTransferCompletionUncertainLaterCommitNotifiesOriginalTarget(t *testing.T) {
	s, d, _ := completionWizard(t, "import")
	completionReadRows(t, s, d, "1")
	completionPrepare(t, s, d)
	s.addWizardSource(d)
	completionReadRows(t, s, d, "2")
	d.transfer.TargetTable = "second"
	d.transfer.TargetColumns = transferColumns()[:1]
	completionPrepare(t, s, d)
	calls := 0
	events := []TableImportEvent{}
	s.deps.TableImported = func(_ ui.Services, e TableImportEvent) { events = append(events, e) }
	s.StreamImporter = func(ctx context.Context, _ int64, _, table string, r mysqlsvc.MySQLDataImportStreamRequest) (*mysqlsvc.MySQLDataImportStreamResult, error) {
		calls++
		p, err := completionRecordRows(ctx, r)
		if err != nil {
			return nil, err
		}
		if table == "second" {
			return &mysqlsvc.MySQLDataImportStreamResult{CommitUncertain: true, Progress: p}, errors.New("commit reply lost")
		}
		return &mysqlsvc.MySQLDataImportStreamResult{Committed: true, Progress: p}, nil
	}
	s.startPreparedDataImport(ui.Services{}, d)
	waitDataTransfer(t, s, d)
	if calls != 2 || !d.transfer.Uncertain || d.transfer.Completed || len(events) != 2 || events[0].Table != "t" || events[0].Uncertain || !events[0].Completed || events[1].Table != "second" || !events[1].Uncertain || events[1].Completed || events[1].ProfileID != 12 || events[1].Generation != 2 {
		t.Fatal("uncertain target lost qualified invalidation", calls, events, d.transfer)
	}
	for index, expected := range []string{"committed", "uncertain"} {
		committed, uncertain := mysqlDataBatchRowSettlement(d.transfer.Batch, int64(index), false)
		row, err := d.transfer.Journal.Row(int64(index), committed, uncertain)
		if err != nil || row.Status != expected {
			t.Fatal("uncertain target changed another target", row, err)
		}
	}
	s.startPreparedDataImport(ui.Services{}, d)
	if calls != 2 {
		t.Fatal("uncertain multi-target commit replayed")
	}
}

func TestDataTransferCompletionJournalCreationFailureNeverStartsWrite(t *testing.T) {
	s, d, _ := completionWizard(t, "import")
	completionReadRows(t, s, d, "1")
	completionPrepare(t, s, d)
	// An invalid temporary directory exercises an actual filesystem failure
	// without a quota dependency or a production-only fault injection hook.
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", path)
	t.Setenv("TMP", path)
	t.Setenv("TEMP", path)
	called := false
	s.StreamImporter = func(context.Context, int64, string, string, mysqlsvc.MySQLDataImportStreamRequest) (*mysqlsvc.MySQLDataImportStreamResult, error) {
		called = true
		return &mysqlsvc.MySQLDataImportStreamResult{Committed: true}, nil
	}
	s.startPreparedDataImport(ui.Services{}, d)
	waitDataTransfer(t, s, d)
	if called || d.transfer.Completed || d.transfer.Uncertain || !d.transfer.Submitted || d.transfer.Err == "" {
		t.Fatal("unavailable result journal allowed writing or claimed completion", called, d.transfer)
	}
	s.startPreparedDataImport(ui.Services{}, d)
	if called {
		t.Fatal("failed journal setup was implicitly retried")
	}
}

func TestDataTransferCompletionBackChangedSourceSettingsRequireReread(t *testing.T) {
	s, d, _ := completionWizard(t, "import")
	completionReadRows(t, s, d, "1")
	completionPrepare(t, s, d)
	d.transfer.Step = 4
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.transferModals(c, d) }, 900, 650)
	for step := 3; step >= 1; step-- {
		if err := tt.Click("上一步"); err != nil || d.transfer.Step != step {
			t.Fatal("back did not revisit original source settings", step, err)
		}
	}
	for _, label := range []string{"文本编码:", "字段分隔符（使用 \\t 表示制表符）:", "日期顺序:", "二进制编码:"} {
		if !tt.HasText(label) {
			t.Fatalf("source setting has no visible label: %s", label)
		}
	}
	if err := tt.Click("字段分隔符（使用 \\t 表示制表符）"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(";")
	if d.transfer.Options.Delimiter != ";" {
		t.Fatal("source setting was not edited")
	}
	for step := 2; step <= 4; step++ {
		if err := tt.Click("下一步"); err != nil || d.transfer.Step != step {
			t.Fatal("next did not preserve revisited settings", step, err)
		}
	}
	if err := tt.Click("确认导入"); err == nil && d.transfer.Submitted {
		t.Fatal("edited source parsing reused prepared rows")
	}
	if err := tt.Click("完整校验全部源记录"); err != nil {
		t.Fatal(err)
	}
	if d.transfer.Busy || !strings.Contains(d.transfer.Err, "重新读取") {
		t.Fatal("old source spool prepared with new parsing settings", d.transfer.Err)
	}
}

func TestDataTransferCompletionEscapeWaitsForExecutorAcknowledgement(t *testing.T) {
	s, d, _ := completionWizard(t, "import")
	completionReadRows(t, s, d, "1")
	completionPrepare(t, s, d)
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.StreamImporter = func(ctx context.Context, _ int64, _, _ string, _ mysqlsvc.MySQLDataImportStreamRequest) (*mysqlsvc.MySQLDataImportStreamResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return &mysqlsvc.MySQLDataImportStreamResult{}, context.Canceled
	}
	s.startPreparedDataImport(ui.Services{}, d)
	<-started
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.consumeDataTransfers(c.Services()); s.transferModals(c, d) }, 900, 650)
	tt.Key(0, ui.KeyEscape)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Escape did not cancel owning executor")
	}
	if !d.transfer.Busy || !d.transfer.Canceling || !d.transfer.Open {
		t.Fatal("Escape dismissed pending driver acknowledgement")
	}
	close(release)
	waitDataTransfer(t, s, d)
	if d.transfer.Busy || d.transfer.Canceling || d.transfer.Completed || d.transfer.Uncertain || !d.transfer.Submitted {
		t.Fatal("known cancellation did not settle safely", d.transfer)
	}
}
