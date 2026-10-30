package dataworkspace

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/datasources"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var mysqlDataImportNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

type mysqlDataExporter func(context.Context, int64, string, string, mysqlsvc.MySQLDataOptions, []string, func([]mysqlsvc.MySQLDataValue) error) error
type mysqlDataNewTableImporter func(context.Context, int64, string, string, []mysqlsvc.MySQLDataImportColumn, []mysqlsvc.MySQLDataEdit) (*mysqlsvc.MySQLDataNewTableImportResult, error)
type mysqlDataImporter func(context.Context, int64, string, string, mysqlsvc.MySQLDataImportRequest) (*mysqlsvc.MySQLDataImportResult, error)

type mysqlDataTransferState struct {
	Batch                                        []mysqlDataBatchReceipt
	Sources                                      []mysqlDataWizardSource
	ActiveSource                                 int
	SourceConfig                                 datasources.Config
	SourceTables                                 []datasources.Table
	SourceTable                                  int
	Capabilities                                 []datasources.Capability
	TargetTable                                  string
	TargetColumns                                []mysqlsvc.MySQLStructureColumn
	Journal                                      *mysqlDataImportJournal
	JournalCommitted, JournalUncertain           bool
	Wizard                                       bool
	Step                                         int
	Options                                      mysqlDataSourceOptions
	PresetName, PresetChoice                     string
	ExtendedInsert, IgnoreForeignKeys            bool
	StatementKB                                  int
	ExportPath                                   string
	RawSpool, MappedSpool                        *mysqlDataImportSpool
	TotalRows, ChangedValues                     int64
	PreparationKey                               string
	ParsedSourceKey                              string
	Assets                                       *mysqlDataSpoolOwner
	Progress                                     <-chan mysqlsvc.MySQLDataImportProgress
	Processed                                    int64
	LeaveOpen                                    bool
	LeaveNext                                    func(ui.Services)
	SaveDialog                                   func(mygo.SaveDialogOptions) (string, error)
	OpenDialog                                   func(mygo.OpenDialogOptions) ([]string, error)
	PickerActive                                 bool
	PickerOutsidePress                           bool
	PickerDone                                   <-chan struct{}
	Open, Busy, Canceling, Submitted, Uncertain  bool
	EmptyAsNull                                  bool
	ContinueOnError                              bool
	Mode, DestructiveConfirmation                string
	MatchColumns                                 []string
	RowResults                                   []mysqlsvc.MySQLDataImportRowResult
	ResultGrid                                   ui.ListState
	ReviewInput                                  *mysqlDataImportInput
	ReviewKey                                    string
	ReviewRows                                   int
	ReviewErr                                    error
	Target, NewName, ResultTable                 string
	NewColumns                                   []mysqlsvc.MySQLDataImportColumn
	Created, Completed                           bool
	Kind, Format, Scope, Path, Err, Confirmation string
	Fields                                       []string
	Included                                     map[string]bool
	Mapping                                      []string
	Input                                        *mysqlDataImportInput
	Cancel                                       context.CancelFunc
	Results                                      <-chan mysqlDataTransferResult
	Request, Generation                          uint64
}
type mysqlDataTransferResult struct {
	Batch                                []mysqlDataBatchReceipt
	SourceTables                         []datasources.Table
	Capabilities                         []datasources.Capability
	TargetColumns                        []mysqlsvc.MySQLStructureColumn
	Journal                              *mysqlDataImportJournal
	RawSpool, MappedSpool                *mysqlDataImportSpool
	Streamed                             *mysqlsvc.MySQLDataImportStreamResult
	Inferred                             []mysqlsvc.MySQLDataImportColumn
	PreparationKey                       string
	ParsedSourceKey                      string
	TotalRows, ChangedValues             int64
	PickedPath                           bool
	History                              []mysqlDataHistoryEntry
	Request, Generation                  uint64
	Profile                              int64
	Path                                 string
	Rows                                 int
	Input                                *mysqlDataImportInput
	Saved                                *mysqlsvc.MySQLDataEditResult
	Imported                             *mysqlsvc.MySQLDataImportResult
	Err                                  error
	Write                                bool
	NewTable, Created, CreationUncertain bool
	Destination                          string
	CreationHistory                      mysqlDataHistoryEntry
}
type mysqlDataImportInput struct {
	Fields []string
	Rows   [][]mysqlsvc.MySQLDataValue
}

func (s *State) openDataImport(t *Document) { s.openDataTransfer(t, "import") }
func (s *State) openDataExport(t *Document) { s.openDataTransfer(t, "export") }
func (s *State) openDataTransfer(t *Document, kind string) {
	if t == nil || t.closed || t.page == nil || t.loading || t.edit.Busy || t.edit.Uncertain != nil || t.transfer.Busy || t.transfer.Uncertain || dataTransactionActive(t) || dataTransactionBusy(t) {
		return
	}
	if mysqlDataHasDraft(t) {
		t.edit.Err = locales.Text("请先保存或取消更改，再导入或导出")
		return
	}
	s.closeDataTransfer(t)
	request := t.transfer.Request + 1
	tr := mysqlDataTransferState{Wizard: true, Assets: &mysqlDataSpoolOwner{}, Options: defaultMySQLDataSourceOptions(), ContinueOnError: true, ExtendedInsert: true, StatementKB: 1024, Open: true, Kind: kind, Format: "CSV", Scope: "page", Target: "existing", NewName: t.table + "_import", Fields: append([]string(nil), t.page.Columns...), Included: map[string]bool{}, Request: request, Generation: s.session().Generation}
	for _, field := range tr.Fields {
		tr.Included[field] = !t.hiddenColumns[field]
	}
	if kind == "import" {
		tr.Sources = []mysqlDataWizardSource{{Name: "来源 1"}}
	}
	t.transfer = tr
}
func (s *State) closeDataTransfer(t *Document) {
	if t == nil {
		return
	}
	if t.transfer.Cancel != nil {
		t.transfer.Cancel()
	}
	if t.transfer.RawSpool != nil {
		t.transfer.RawSpool.Close()
	}
	if t.transfer.MappedSpool != nil {
		t.transfer.MappedSpool.Close()
	}
	if t.transfer.Journal != nil {
		t.transfer.Journal.Close()
	}
	if t.transfer.Assets != nil {
		t.transfer.Assets.Close()
	}
	request := t.transfer.Request + 1
	t.transfer = mysqlDataTransferState{Request: request}
}
func (s *State) stopDataTransfer(t *Document) {
	if t.transfer.Busy {
		if t.transfer.Cancel != nil {
			t.transfer.Cancel()
		}
		t.transfer.Canceling = true
	} else {
		s.closeDataTransfer(t)
	}
}

func (s *State) transferModals(c *ui.Context, t *Document) {
	if t == nil || t.closed {
		return
	}
	tr := &t.transfer
	if tr.LeaveOpen {
		choice := ui.AlertDialog(c, &tr.LeaveOpen, locales.Text("导入结果不确定"), locales.Text("外部核验导入结果后，可关闭此表。关闭不会再次执行导入，也无法撤回可能已提交的数据。"), locales.Text("已核验，关闭表"), locales.Text("取消"))
		if choice == 0 {
			s.resolveDataTransferLeave(c.Services(), t, true)
		}
		if choice == 1 {
			s.resolveDataTransferLeave(c.Services(), t, false)
		}
		return
	}
	if !tr.Open && tr.PickerActive {
		tr.Open = true
	}
	if !tr.Open {
		if tr.Busy {
			s.stopDataTransfer(t)
		}
		return
	}
	available := s.session().Connected && s.session().ProfileID == t.profileID && s.session().Generation == tr.Generation && !dataTransactionActive(t) && !dataTransactionBusy(t)
	colors := theme.Colors(c)
	title := locales.Text("导出向导")
	if tr.Kind == "import" {
		title = locales.Text("导入向导")
	}
	if tr.Wizard {
		s.dataTransferWizard(c, t, available, title)
		return
	}
	modal := ui.Modal(c, &tr.Open, func() {
		width, height := c.Size()
		ui.Scroll(c).Width(min(float32(620), width-40)).MaxHeight(height - 40).Children(func() {
			ui.Column(c).Width(min(float32(620), width-40)).Padding(18).Gap(10).Background(colors.Header).Children(func() {
				ui.Text(c, title).Bold().FontSize(theme.Font(c, 15))
				ui.Text(c, t.database+"."+t.table).TextColor(colors.Muted).SingleLine()
				if !available {
					ui.Text(c, locales.Text("原 MySQL 会话已失效，请关闭向导后重新打开")).TextColor(colors.Error)
				}
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, locales.Text("文件格式"))
					choices := mysqlDataTransferFormatChoices(tr.Kind)
					mysqlDataRuleSelect(c, &tr.Format, choices, func(x string) string { return x }).Disabled(tr.Busy || tr.Input != nil).Width(130)
				})
				if tr.Format == "XML" {
					ui.Text(c, locales.Text("XML 使用 RECORDS/RECORD；空元素作为空字符串，xsi:nil 表示 NULL，仅显式 encoding=base64/hex 解码二进制。")).FontSize(theme.Font(c, 11)).TextColor(colors.Muted)
				}
				if tr.Format == "XLSX" {
					ui.Text(c, locales.Text("XLSX 使用首个工作表，首行为字段名；数字文本保留精度，公式单元格不支持导入。")).FontSize(theme.Font(c, 11)).TextColor(colors.Muted)
				}
				if tr.Kind == "export" {
					ui.Row(c).Gap(8).Children(func() {
						ui.Text(c, locales.Text("导出范围"))
						mysqlDataRuleSelect(c, &tr.Scope, []string{"page", "selection", "all"}, func(x string) string {
							switch x {
							case "page":
								return locales.Text("当前页")
							case "selection":
								return locales.Text("当前选区")
							default:
								return locales.Text("完整筛选结果")
							}
						}).Disabled(tr.Busy).Width(200)
					})
					ui.Text(c, locales.Text("选择导出字段")).Bold()
					ui.Scroll(c).MaxHeight(150).Children(func() {
						ui.Column(c).Gap(3).Children(func() {
							for _, field := range tr.Fields {
								checked := tr.Included[field]
								ui.Checkbox(c, &checked, field).Disabled(tr.Busy)
								tr.Included[field] = checked
							}
						})
					})
					ui.Text(c, locales.Text("完整值导出：每字段最多 1 MiB、每行最多 16 MiB，超限将失败并保留原文件。CSV/TSV 使用 \\N 表示 NULL，二进制使用 0x 十六进制。")).FontSize(theme.Font(c, 11)).TextColor(colors.Muted)
				} else {
					ui.Text(c, locales.Text("最多 10000 行、文件最多 16 MiB。CSV/TSV 首行为字段名，\\N 表示 NULL，二进制使用 0x 十六进制。")).FontSize(theme.Font(c, 11)).TextColor(colors.Muted)
					s.dataImportTargetControls(c, t)
					s.dataImportModeControls(c, t)
					ui.Checkbox(c, &tr.EmptyAsNull, locales.Text("使用 NULL 取代空字符串")).Disabled(tr.Busy || tr.Submitted)
					if tr.EmptyAsNull {
						ui.Text(c, locales.Text("只转换长度为零的文本值；空二进制和空格保留原值。")).FontSize(theme.Font(c, 11)).TextColor(colors.Muted)
					}
					ui.Row(c).Gap(8).Children(func() {
						ui.Text(c, tr.Path).SingleLine().Grow(1).MinWidth(0)
						if ui.Button(c, locales.Text("选择文件")).Disabled(tr.Busy || tr.Submitted).Clicked() {
							s.chooseDataImportFile(c.Services(), t)
						}
					})
					if tr.Input != nil {
						s.dataImportReview(c, t)
					}
					s.dataImportResultRows(c, t)
				}
				if tr.Err != "" {
					color := colors.Error
					if tr.Completed {
						color = colors.Muted
					}
					ui.Text(c, tr.Err).TextColor(color).FontSize(theme.Font(c, 12))
				}
				if tr.Busy {
					label := locales.Text("正在处理数据文件…")
					if tr.Canceling {
						label = locales.Text("正在等待取消确认…")
					}
					ui.Text(c, label).TextColor(colors.Muted)
				}
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					label := locales.Text("取消")
					if tr.Submitted && !tr.Busy {
						label = locales.Text("关闭")
					}
					if ui.Button(c, label).Disabled(tr.Canceling).Clicked() {
						s.stopDataTransfer(t)
					}
					if tr.Kind == "export" {
						if ui.PrimaryButton(c, locales.Text("导出")).Disabled(tr.Busy || !available).Clicked() {
							s.startDataExport(c.Services(), t)
						}
					} else {
						valid := available && tr.Input != nil && mysqlDataImportConfirmed(t) && !tr.Busy && !tr.Submitted
						if valid {
							_, err := mysqlDataImportReviewValidation(t)
							valid = err == nil && (s.importer() != nil || (!tr.ContinueOnError && mysqlDataImportMode(t) == "append" && (tr.Target != "new" || s.newTableImporter() != nil)))
						}
						if tr.Created && !tr.Uncertain && !tr.Busy && s.deps.OpenImportedTable != nil && ui.Button(c, locales.Text("打开目标表")).Clicked() {
							tr.Open = false
							s.deps.OpenImportedTable(c.Services(), TableImportEvent{ProfileID: t.profileID, Generation: tr.Generation, Database: t.database, Table: tr.ResultTable, Created: true, Completed: tr.Completed})
						}
						if ui.PrimaryButton(c, locales.Text("确认导入")).Disabled(!valid).Clicked() {
							s.startDataImport(c.Services(), t)
						}
					}
				})
			})
		})
	})
	s.finishDataTransferModal(t, modal.PressedOutside())
}

func (s *State) beginDataPicker(t *Document) chan struct{} {
	done := make(chan struct{})
	t.transfer.PickerActive, t.transfer.PickerDone = true, done
	t.transfer.PickerOutsidePress = false
	return done
}

// Native picker events may be delivered to the owning window at coordinates
// outside its modal. Consume the picker acknowledgement after that frame's
// dismissal guard, rather than clearing it before the modal handles events.
// All mutable presentation state stays on the UI thread.
func (s *State) finishDataTransferModal(t *Document, pressedOutside bool) {
	tr := &t.transfer
	if tr.PickerActive && tr.PickerDone != nil {
		tr.PickerOutsidePress = tr.PickerOutsidePress || pressedOutside
		returned := false
		select {
		case <-tr.PickerDone:
			returned = true
		default:
		}
		if !tr.Open {
			// With no pending external press, Escape after the picker
			// returned cancels the real reader/writer which is now busy.
			if returned && tr.Busy && !tr.PickerOutsidePress {
				tr.PickerActive = false
			} else {
				tr.Open = true
			}
			tr.PickerOutsidePress = false
		}
		// Keep the guard across the external press/release pair, including
		// extra MyGo build passes between those two events.
		if returned && !tr.PickerOutsidePress {
			tr.PickerActive, tr.PickerDone = false, nil
		}
	}
	if !tr.Open && tr.Busy {
		tr.Open = true
		s.stopDataTransfer(t)
	}
}

func (s *State) chooseDataImportFile(services ui.Services, t *Document) {
	if t.transfer.Busy || t.transfer.Submitted {
		return
	}
	ctx, out, request, gen := s.beginDataTransfer(t)
	pickerDone := s.beginDataPicker(t)
	format, profile := t.transfer.Format, t.profileID
	openDialog := t.transfer.OpenDialog
	options, wizard, assets := t.transfer.Options, t.transfer.Wizard, t.transfer.Assets
	if openDialog == nil {
		openDialog = mygo.Dialog.Open
	}
	go func() {
		paths, err := openDialog(mygo.OpenDialogOptions{Title: locales.Text("选择导入数据文件"), Filters: []mygo.FileFilter{{Name: format, Extensions: []string{strings.ToLower(format)}}}})
		close(pickerDone)
		services.Invalidate()
		var input *mysqlDataImportInput
		var spool *mysqlDataImportSpool
		var inferred []mysqlsvc.MySQLDataImportColumn
		path := ""
		if err == nil && len(paths) > 0 && ctx.Err() == nil {
			path = paths[0]
			var f *os.File
			f, err = os.Open(path)
			if err == nil {
				if wizard {
					spool, input, inferred, err = mysqlDataReadSpool(ctx, f, format, options, assets)
				} else {
					input, err = mysqlDataParseImport(f, format)
				}
				f.Close()
			}
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		out <- mysqlDataTransferResult{RawSpool: spool, ParsedSourceKey: mysqlDataSourceKey(format, options), Inferred: inferred, Request: request, Generation: gen, Profile: profile, Path: path, Input: input, Err: err}
		services.Invalidate()
	}()
}
func (s *State) beginDataTransfer(t *Document) (context.Context, chan mysqlDataTransferResult, uint64, uint64) {
	t.transfer.Request++
	t.transfer.Busy = true
	t.transfer.Canceling = false
	t.transfer.PickerActive, t.transfer.PickerDone = false, nil
	t.transfer.PickerOutsidePress = false
	t.transfer.Err = ""
	ctx, cancel := context.WithCancel(context.Background())
	t.transfer.Cancel = cancel
	out := make(chan mysqlDataTransferResult, 1)
	t.transfer.Results = out
	return ctx, out, t.transfer.Request, s.session().Generation
}
func (s *State) startDataImport(services ui.Services, t *Document) {
	tr := &t.transfer
	if t.closed || dataTransactionActive(t) || dataTransactionBusy(t) || tr.Busy || tr.Submitted || tr.Input == nil || !mysqlDataImportConfirmed(t) || t.edit.Busy || t.edit.Uncertain != nil || mysqlDataHasDraft(t) || !s.session().Connected || s.session().ProfileID != t.profileID || s.session().Generation != tr.Generation {
		return
	}
	columns, edits, err := mysqlDataImportRequest(t)
	if err != nil {
		tr.Err = err.Error()
		return
	}
	saver := s.saverFor(t)
	newImporter := s.newTableImporter()
	importer := s.importer()
	newTable := tr.Target == "new"
	if importer == nil && (tr.ContinueOnError || mysqlDataImportMode(t) != "append" || !newTable && saver == nil || newTable && newImporter == nil || len(edits) > mysqlsvc.MySQLDataMaxPageSize) {
		tr.Err = locales.Text("数据编辑服务不可用")
		return
	}
	tr.Submitted = true
	ctx, out, request, gen := s.beginDataTransfer(t)
	profile, db, table := t.profileID, t.database, mysqlDataImportDestination(t)
	importRequest := mysqlDataImportServiceRequest(t, columns, edits)
	var history []mysqlDataHistoryEntry
	if importer == nil {
		history = mysqlDataWriteHistoryRequest(db, table, edits, false)
	}
	creation := mysqlDataHistoryEntry{}
	if newTable {
		creation = mysqlDataImportCreationHistory(db, table, columns)
	}
	go func() {
		started := time.Now()
		var result *mysqlsvc.MySQLDataEditResult
		var created, creationUncertain bool
		var err error
		var importedResult *mysqlsvc.MySQLDataImportResult
		if importer != nil {
			importedResult, err = importer(ctx, profile, db, table, importRequest)
			if importedResult != nil {
				result, created, creationUncertain = importedResult.Saved, importedResult.Created, importedResult.CreationUncertain
			}
		} else if newTable {
			imported, importErr := newImporter(ctx, profile, db, table, columns, edits)
			err = importErr
			if imported != nil {
				result, created, creationUncertain = imported.Saved, imported.Created, imported.CreationUncertain
			}
		} else {
			result, err = saver(ctx, profile, db, table, edits)
		}
		for i := range history {
			history[i].At, history[i].Duration = time.Now(), time.Since(started)
		}
		creation.At, creation.Duration = time.Now(), time.Since(started)
		out <- mysqlDataTransferResult{Imported: importedResult, CreationHistory: creation, NewTable: newTable, Created: created, CreationUncertain: creationUncertain, Destination: table, History: history, Request: request, Generation: gen, Profile: profile, Rows: len(edits), Saved: result, Err: err, Write: true}
		services.Invalidate()
	}()
}
func (s *State) startDataExport(services ui.Services, t *Document) {
	tr := &t.transfer
	if t.closed || dataTransactionActive(t) || dataTransactionBusy(t) || tr.Busy || t.loading || mysqlDataHasDraft(t) || t.edit.Busy || t.edit.Uncertain != nil || !s.session().Connected || s.session().ProfileID != t.profileID || s.session().Generation != tr.Generation {
		return
	}
	var fields []string
	for _, field := range tr.Fields {
		if tr.Included[field] {
			fields = append(fields, field)
		}
	}
	if tr.Format == "SQL" {
		for _, col := range t.page.ColumnMetadata {
			if slices.Contains(fields, col.Name) && mysqlsvc.MySQLDataColumnGenerated(col) {
				tr.Err = locales.Text("SQL 导出不能包含生成字段，请取消勾选该字段")
				return
			}
		}
	}
	if len(fields) == 0 {
		tr.Err = locales.Text("请选择至少一个字段")
		return
	}
	var previewRows [][]mysqlsvc.MySQLDataValue
	var refs []*mysqlsvc.MySQLRowRef
	var snapshots []*mysqlsvc.MySQLRowSnapshot
	scope := tr.Scope
	if scope != "all" {
		var err error
		previewRows, refs, snapshots, err = mysqlDataExportSnapshot(t, fields, scope == "selection")
		if err != nil {
			tr.Err = err.Error()
			return
		}
		if len(previewRows) == 0 {
			tr.Err = locales.Text("没有可导出的行")
			return
		}
	}
	exporter := s.Exporter
	if exporter == nil {
		exporter = s.runtime().Exporter
	}
	if scope == "all" && exporter == nil {
		tr.Err = locales.Text("完整数据导出服务不可用")
		return
	}
	reader := s.rowReaderFor(t)
	for _, ref := range refs {
		if ref != nil && reader == nil {
			tr.Err = locales.Text("完整字段读取不可用")
			return
		}
	}
	options := mysqlsvc.MySQLDataOptions{FilterColumn: t.filterColumn, FilterValue: t.filterValue, FilterMode: t.filterMode, SortColumn: t.sortColumn, SortDescending: t.sortDescending, Filters: append([]mysqlsvc.MySQLDataFilterRule(nil), t.filterRules...), Sorts: append([]mysqlsvc.MySQLDataSortRule(nil), t.sortRules...)}
	formatOptions := tr.Options
	metadata := slices.Clone(t.page.ColumnMetadata)
	ctx, out, request, gen := s.beginDataTransfer(t)
	format, profile, db, table := tr.Format, t.profileID, t.database, t.table
	if tr.Wizard {
		tr.Submitted = true
	}
	saveDialog := tr.SaveDialog
	if saveDialog == nil {
		saveDialog = mygo.Dialog.Save
	}
	var pickerDone chan struct{}
	if tr.ExportPath != "" {
		destination := tr.ExportPath
		saveDialog = func(mygo.SaveDialogOptions) (string, error) { return destination, nil }
	} else {
		pickerDone = s.beginDataPicker(t)
	}
	go func() {
		path, err := saveDialog(mygo.SaveDialogOptions{Title: locales.Text("导出数据文件"), DefaultPath: table + "." + strings.ToLower(format), Filters: []mygo.FileFilter{{Name: format, Extensions: []string{strings.ToLower(format)}}}})
		if pickerDone != nil {
			close(pickerDone)
			services.Invalidate()
		}
		count := 0
		if err == nil && path != "" {
			err = mysqlDataAtomicFile(ctx, path, func(w io.Writer) error {
				encoder, e := newMySQLDataConfiguredEncoder(ctx, w, format, db, table, fields, metadata, formatOptions)
				if e != nil {
					return e
				}
				defer encoder.Abort()
				emit := func(values []mysqlsvc.MySQLDataValue) error {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if e := encoder.Row(values); e != nil {
						return e
					}
					count++
					return nil
				}
				if scope == "all" {
					e = exporter(ctx, profile, db, table, options, fields, emit)
				} else {
					for i, values := range previewRows {
						if refs[i] != nil {
							var row *mysqlsvc.MySQLDataRow
							row, e = reader(ctx, profile, db, table, *refs[i])
							if e != nil {
								break
							}
							if row == nil || snapshots[i] == nil || !reflect.DeepEqual(row.Snapshot, snapshots[i]) {
								e = mysqlsvc.ErrMySQLDataConflict
								break
							}
							values = make([]mysqlsvc.MySQLDataValue, len(fields))
							for j, field := range fields {
								value, ok := row.Values[field]
								if !ok {
									e = mysqlsvc.ErrMySQLDataPage
									break
								}
								values[j] = value
							}
							if e != nil {
								break
							}
						}
						if e = emit(values); e != nil {
							break
						}
					}
				}
				if e != nil {
					return e
				}
				return encoder.Close()
			})
		}
		out <- mysqlDataTransferResult{Request: request, Generation: gen, Profile: profile, Rows: count, Path: path, Err: err}
		services.Invalidate()
	}()
}

func (s *State) consumeDataTransfers(services ui.Services) {
	for _, t := range s.documents {
		tr := &t.transfer
		if t.profileID == s.session().ProfileID && tr.Progress != nil {
			select {
			case p := <-tr.Progress:
				tr.Processed = p.Processed
			default:
			}
		}
		if t.closed || tr.Results == nil || t.profileID != s.session().ProfileID {
			continue
		}
		select {
		case result := <-tr.Results:
			if result.Request != tr.Request || result.Profile != t.profileID {
				continue
			}
			if tr.Cancel != nil {
				tr.Cancel()
			}
			tr.Cancel = nil
			tr.Results = nil
			tr.Busy = false
			tr.Canceling = false
			stale := result.Generation != s.session().Generation || !s.session().Connected
			if result.Capabilities != nil {
				tr.Capabilities = result.Capabilities
			}
			if result.Write {
				if result.Streamed != nil {
					s.consumeStreamedDataImport(services, t, result, stale)
					continue
				}
				if result.Imported != nil {
					s.consumeDataImportResult(services, t, result, stale)
					continue
				}
				if result.NewTable {
					t.transfer.Created, t.transfer.ResultTable = result.Created, result.Destination
					entry := result.CreationHistory
					entry.Failed = !result.Created || result.CreationUncertain
					entry.Message = locales.Text("目标表已创建")
					if entry.Failed {
						entry.Message = mysqlDataHistoryError(result.Err, stale, false, result.CreationUncertain)
					}
					s.appendDataHistory(t, entry)
				}
				if !result.NewTable || result.Created {
					s.recordDataWriteHistory(t, result.History, result.Saved, result.Err, stale)
				}
				complete := result.Saved != nil && !result.Saved.CommitUncertain && len(result.Saved.Outcomes) == result.Rows
				if stale || result.CreationUncertain || result.Saved != nil && result.Saved.CommitUncertain || result.Err == nil && (!complete || result.NewTable && !result.Created) {
					tr.Uncertain = true
					tr.Err = locales.Text("导入结果不确定，禁止重放；请核验数据库")
					if !result.NewTable {
						t.edit.Uncertain = cloneEditResult(result.Saved)
						if t.edit.Uncertain == nil {
							t.edit.Uncertain = &mysqlsvc.MySQLDataEditResult{}
						}
						t.edit.Uncertain.CommitUncertain = true
					}
				} else if result.Err != nil {
					tr.Err = locales.Text("导入失败；本次向导不会重放：") + result.Err.Error()
					if result.NewTable && result.Created {
						tr.Err = fmt.Sprintf(locales.Text("目标表 %s 已创建，数据导入失败；表保留。本次向导不会重放："), result.Destination) + result.Err.Error()
					}
				} else {
					tr.Err = ""
					if result.NewTable {
						tr.Completed = true
						tr.Err = fmt.Sprintf(locales.Text("已创建 %s 并导入 %d 行"), result.Destination, result.Rows)
						s.setStatus(tr.Err)
					} else {
						tr.Open = false
						s.setStatus(fmt.Sprintf(locales.Text("已导入 %d 行"), result.Rows))
						s.Load(services, t, true)
					}
				}
				if result.NewTable && (result.Created || result.CreationUncertain) && s.deps.TableImported != nil {
					s.deps.TableImported(services, TableImportEvent{ProfileID: result.Profile, Generation: result.Generation, Database: t.database, Table: result.Destination, Created: result.Created, Uncertain: tr.Uncertain, Completed: tr.Completed})
				}
			} else if stale {
				if result.RawSpool != nil {
					result.RawSpool.Close()
				}
				if result.MappedSpool != nil {
					result.MappedSpool.Close()
				}
				tr.Err = locales.Text("原 MySQL 会话已失效，请关闭向导后重新打开")
			} else if result.Err != nil {
				if !errors.Is(result.Err, context.Canceled) {
					tr.Err = result.Err.Error()
				}
			} else if result.SourceTables != nil {
				tr.SourceTables = result.SourceTables
				tr.Capabilities = result.Capabilities
			} else if result.TargetColumns != nil {
				tr.TargetColumns = result.TargetColumns
				mysqlDataResetTargetMapping(t)
			} else if result.MappedSpool != nil {
				if tr.MappedSpool != nil {
					tr.MappedSpool.Close()
				}
				tr.MappedSpool = result.MappedSpool
				tr.PreparationKey = result.PreparationKey
				tr.ChangedValues = result.ChangedValues
				tr.TotalRows = result.TotalRows
			} else if result.PickedPath {
				tr.ExportPath = result.Path
			} else if result.Input != nil {
				if tr.RawSpool != nil {
					tr.RawSpool.Close()
				}
				if tr.MappedSpool != nil {
					tr.MappedSpool.Close()
					tr.MappedSpool = nil
				}
				tr.RawSpool = result.RawSpool
				tr.ParsedSourceKey = result.ParsedSourceKey
				tr.PreparationKey = ""
				if result.RawSpool != nil {
					tr.TotalRows = result.RawSpool.rows
				}
				tr.Input = result.Input
				tr.NewColumns = mysqlDataInferImportColumns(result.Input)
				if result.Inferred != nil {
					tr.NewColumns = result.Inferred
				}
				tr.Path = result.Path
				mysqlDataResetTargetMapping(t)
			} else if result.Path != "" {
				if tr.Wizard {
					tr.Completed = true
					tr.Step = len(mysqlDataWizardSteps(tr.Kind)) - 1
				} else {
					tr.Open = false
				}
				s.setStatus(fmt.Sprintf(locales.Text("已导出 %d 行至 %s"), result.Rows, result.Path))
			}
		default:
		}
	}
}

func mysqlDataExportSnapshot(t *Document, fields []string, selected bool) ([][]mysqlsvc.MySQLDataValue, []*mysqlsvc.MySQLRowRef, []*mysqlsvc.MySQLRowSnapshot, error) {
	var rows [][]mysqlsvc.MySQLDataValue
	var refs []*mysqlsvc.MySQLRowRef
	var snapshots []*mysqlsvc.MySQLRowSnapshot
	for i, line := range t.page.Rows {
		if selected && !t.selection.Has(i) && (t.selection.Len() != 0 || i != t.selectedRow) {
			continue
		}
		values := make([]mysqlsvc.MySQLDataValue, len(fields))
		needsFull := false
		for j, field := range fields {
			source := slices.Index(t.page.Columns, field)
			if source < 0 || source >= len(line) {
				return nil, nil, nil, mysqlsvc.ErrMySQLDataPage
			}
			cell := line[source]
			values[j] = mysqlsvc.MySQLDataValue{Text: cell.Text, Null: cell.Null}
			if cell.Null {
				values[j].Text = ""
			}
			needsFull = needsFull || cell.Truncated
			if source < len(t.page.ColumnMetadata) && mysqlDataBinaryField(t.page.ColumnMetadata[source]) && !cell.Null && !cell.Truncated {
				value, err := hex.DecodeString(strings.TrimPrefix(cell.Text, "0x"))
				if err != nil {
					return nil, nil, nil, err
				}
				values[j] = mysqlsvc.MySQLDataValue{Binary: true, Bytes: value}
			}
		}
		var ref *mysqlsvc.MySQLRowRef
		var snapshot *mysqlsvc.MySQLRowSnapshot
		if needsFull {
			if i >= len(t.page.RowRefs) || t.page.RowRefs[i] == nil || i >= len(t.page.RowSnapshots) || t.page.RowSnapshots[i] == nil {
				return nil, nil, nil, errors.New(locales.Text("截断字段缺少唯一行标识，无法完整导出"))
			}
			cloned := cloneRowRef(*t.page.RowRefs[i])
			ref = &cloned
			snapshot = cloneSnapshot(t.page.RowSnapshots[i])
		}
		rows = append(rows, values)
		refs = append(refs, ref)
		snapshots = append(snapshots, snapshot)
	}
	return rows, refs, snapshots, nil
}

// Parse the entire bounded input before any write. Headers are mandatory and
// JSON numbers retain their exact spelling, including large integer values.
func mysqlDataParseImport(r io.Reader, format string) (*mysqlDataImportInput, error) {
	data, err := io.ReadAll(io.LimitReader(r, mysqlsvc.MaxMySQLDataImportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > mysqlsvc.MaxMySQLDataImportBytes {
		return nil, mysqlsvc.ErrMySQLDataEditLimit
	}
	binaryFormat := format == "XLSX" || format == "XLS" || format == "DBF" || format == "Parquet"
	if !binaryFormat && !utf8.Valid(data) {
		return nil, errors.New(locales.Text("导入文件必须使用 UTF-8 编码"))
	}
	if !binaryFormat {
		data = []byte(strings.TrimPrefix(string(data), "\ufeff"))
	}
	input := &mysqlDataImportInput{}
	switch format {
	case "TXT":
		input, err = mysqlDataParseTXTImport(data)
	case "XLS":
		input, err = mysqlDataParseXLSImport(data)
	case "DBF":
		input, err = mysqlDataParseDBFImport(data)
	case "Parquet":
		input, err = mysqlDataParseParquetImport(data)
	case "CSV", "TSV":
		reader := csv.NewReader(strings.NewReader(string(data)))
		if format == "TSV" {
			reader.Comma = '\t'
		}
		input.Fields, err = reader.Read()
		if err != nil {
			return nil, err
		}
		reader.FieldsPerRecord = len(input.Fields)
		for {
			line, e := reader.Read()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, e
			}
			values := make([]mysqlsvc.MySQLDataValue, len(line))
			for i, value := range line {
				values[i] = mysqlDataDecodeDelimited(value)
			}
			input.Rows = append(input.Rows, values)
			if len(input.Rows) > mysqlsvc.MaxMySQLDataImportRows {
				return nil, mysqlsvc.ErrMySQLDataEditLimit
			}
		}
	case "XML":
		input, err = mysqlDataParseXMLImport(data)
		if err != nil {
			return nil, err
		}
	case "XLSX":
		input, err = mysqlDataParseXLSXImport(data)
		if err != nil {
			return nil, err
		}
	case "HTML":
		input, err = mysqlDataParseHTMLImport(data)
		if err != nil {
			return nil, err
		}
	case "JSON":
		if err = mysqlDataValidateJSONImportKeys(data); err != nil {
			return nil, err
		}
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.UseNumber()
		var records []map[string]any
		if err = dec.Decode(&records); err != nil {
			return nil, err
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return nil, errors.New(locales.Text("JSON 文件必须只包含一个对象数组"))
		}
		if len(records) > mysqlsvc.MaxMySQLDataImportRows {
			return nil, mysqlsvc.ErrMySQLDataEditLimit
		}
		if len(records) > 0 {
			for field := range records[0] {
				input.Fields = append(input.Fields, field)
			}
			slices.Sort(input.Fields)
		}
		for _, record := range records {
			if len(record) != len(input.Fields) {
				return nil, errors.New(locales.Text("JSON 每行必须包含相同字段"))
			}
			values := make([]mysqlsvc.MySQLDataValue, len(input.Fields))
			for i, field := range input.Fields {
				value, ok := record[field]
				if !ok {
					return nil, errors.New(locales.Text("JSON 每行必须包含相同字段"))
				}
				switch v := value.(type) {
				case nil:
					values[i].Null = true
				case string:
					values[i].Text = v
				case json.Number:
					values[i].Text = v.String()
				case bool:
					if v {
						values[i].Text = "1"
					} else {
						values[i].Text = "0"
					}
				default:
					return nil, errors.New(locales.Text("JSON 导入值必须为字符串、数字、布尔值或 null"))
				}
			}
			input.Rows = append(input.Rows, values)
		}
	default:
		return nil, errors.New("unsupported import format")
	}
	if err != nil {
		return nil, err
	}
	if len(input.Fields) == 0 || len(input.Fields) > 128 || len(input.Rows) == 0 {
		return nil, errors.New(locales.Text("导入文件没有字段或数据行"))
	}
	seen := map[string]bool{}
	for _, field := range input.Fields {
		if field == "" || seen[field] {
			return nil, errors.New(locales.Text("导入字段名不能为空或重复"))
		}
		seen[field] = true
	}
	total := 0
	for _, row := range input.Rows {
		if len(row) != len(input.Fields) {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		for _, value := range row {
			if len(value.Text)+len(value.Bytes) > mysqlsvc.MySQLDataEditMaxBytes {
				return nil, mysqlsvc.ErrMySQLDataEditLimit
			}
			total += len(value.Text) + len(value.Bytes)
			if total > mysqlsvc.MaxMySQLDataImportBytes {
				return nil, mysqlsvc.ErrMySQLDataEditLimit
			}
		}
	}
	return input, nil
}
func mysqlDataDecodeDelimited(text string) mysqlsvc.MySQLDataValue {
	if text == `\N` {
		return mysqlsvc.MySQLDataValue{Null: true}
	}
	if strings.HasPrefix(text, `\\`) {
		text = text[1:]
	}
	return mysqlsvc.MySQLDataValue{Text: text}
}
func mysqlDataImportEdits(input *mysqlDataImportInput, mapping []string, columns []mysqlsvc.MySQLStructureColumn) ([]mysqlsvc.MySQLDataEdit, error) {
	return mysqlDataImportEditsWithOptions(input, mapping, columns, false)
}

// Conversion is an explicit import choice. Keep the parsed source immutable so
// disabling the choice restores the original preview and a worker owns its
// already validated values. Empty byte strings and whitespace are not text "".
func mysqlDataImportValue(value mysqlsvc.MySQLDataValue, emptyAsNull bool) mysqlsvc.MySQLDataValue {
	if emptyAsNull && !value.Null && !value.Binary && value.Text == "" {
		return mysqlsvc.MySQLDataValue{Null: true}
	}
	return value
}

func mysqlDataImportEditsWithOptions(input *mysqlDataImportInput, mapping []string, columns []mysqlsvc.MySQLStructureColumn, emptyAsNull bool) ([]mysqlsvc.MySQLDataEdit, error) {
	if input == nil || len(mapping) != len(input.Fields) || len(input.Rows) == 0 || len(input.Rows) > mysqlsvc.MySQLDataMaxPageSize {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	allowed := map[string]mysqlsvc.MySQLStructureColumn{}
	for _, col := range columns {
		allowed[col.Name] = col
	}
	seen := map[string]bool{}
	for _, target := range mapping {
		if target == "" {
			continue
		}
		col, ok := allowed[target]
		if !ok || seen[target] || mysqlsvc.MySQLDataColumnGenerated(col) {
			return nil, errors.New(locales.Text("字段映射包含重复、未知或生成字段"))
		}
		if mysqlDataSpatialField(col) {
			return nil, errors.New(locales.Text("空间字段不支持文件导入"))
		}
		seen[target] = true
	}
	if len(seen) == 0 {
		return nil, errors.New(locales.Text("请映射至少一个字段"))
	}
	for _, col := range columns {
		if !seen[col.Name] && !mysqlsvc.MySQLDataColumnGenerated(col) && !col.Nullable && col.Default == nil && !strings.Contains(strings.ToLower(col.Extra), "auto_increment") {
			return nil, fmt.Errorf(locales.Text("必填字段未映射：%s"), col.Name)
		}
	}
	edits := make([]mysqlsvc.MySQLDataEdit, len(input.Rows))
	total := 0
	for i, row := range input.Rows {
		if len(row) != len(mapping) {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		values := map[string]mysqlsvc.MySQLDataValue{}
		for j, target := range mapping {
			if target == "" {
				continue
			}
			col := allowed[target]
			value := mysqlDataImportValue(row[j], emptyAsNull)
			if value.Null && !col.Nullable {
				return nil, fmt.Errorf(locales.Text("第 %d 行字段 %s 不允许 NULL"), i+1, target)
			}
			if value.Binary && !mysqlDataBinaryField(col) {
				return nil, errors.New(locales.Text("XML 二进制值只能映射到二进制字段"))
			}
			if value.Binary {
				value.Bytes = append([]byte(nil), value.Bytes...)
			}
			if !value.Null && !value.Binary && mysqlDataBinaryField(col) {
				if !strings.HasPrefix(value.Text, "0x") {
					return nil, fmt.Errorf(locales.Text("第 %d 行字段 %s 必须为 0x 十六进制"), i+1, target)
				}
				bytes, e := hex.DecodeString(value.Text[2:])
				if e != nil {
					return nil, e
				}
				value = mysqlsvc.MySQLDataValue{Binary: true, Bytes: bytes}
			}
			if !value.Null && !value.Binary {
				kind := strings.ToLower(col.DataType)
				if slices.Contains([]string{"tinyint", "smallint", "mediumint", "int", "integer", "bigint"}, kind) {
					if _, ok := new(big.Int).SetString(value.Text, 10); !ok {
						return nil, fmt.Errorf(locales.Text("第 %d 行字段 %s 数值无效"), i+1, target)
					}
				}
				if kind == "decimal" || kind == "numeric" || kind == "float" || kind == "double" {
					if !mysqlDataImportNumber.MatchString(value.Text) {
						return nil, fmt.Errorf(locales.Text("第 %d 行字段 %s 数值无效"), i+1, target)
					}
				}
				if kind == "json" && !json.Valid([]byte(value.Text)) {
					return nil, fmt.Errorf(locales.Text("第 %d 行字段 %s JSON 无效"), i+1, target)
				}
				if col.CharacterMaxLength != nil && int64(utf8.RuneCountInString(value.Text)) > *col.CharacterMaxLength {
					return nil, fmt.Errorf(locales.Text("第 %d 行字段 %s 超过字段长度"), i+1, target)
				}
			}
			total += len(value.Text) + len(value.Bytes)
			if total > mysqlsvc.MySQLDataEditMaxBytes {
				return nil, mysqlsvc.ErrMySQLDataEditLimit
			}
			values[target] = value
		}
		edits[i] = mysqlsvc.MySQLDataEdit{Kind: "insert", Values: values}
	}
	return edits, nil
}

// Atomic publication keeps existing targets intact on cancellation or failure.
func mysqlDataAtomicFile(ctx context.Context, path string, write func(io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".nyadb-export-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if err = write(f); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

type mysqlDataTransferEncoder struct {
	w                 io.Writer
	csv               *csv.Writer
	xml               *mysqlDataXMLEncoder
	additional        mysqlDataAdditionalEncoder
	format, db, table string
	fields            []string
	rows              int
}

func newMySQLDataTransferEncoder(w io.Writer, format, db, table string, fields []string) (*mysqlDataTransferEncoder, error) {
	e := &mysqlDataTransferEncoder{w: w, format: format, db: db, table: table, fields: append([]string(nil), fields...)}
	switch format {
	case "CSV", "TSV":
		e.csv = csv.NewWriter(w)
		if format == "TSV" {
			e.csv.Comma = '\t'
		}
		if err := e.csv.Write(fields); err != nil {
			return nil, err
		}
	case "JSON":
		if _, err := io.WriteString(w, "[\n"); err != nil {
			return nil, err
		}
	case "XML":
		var err error
		e.xml, err = newMySQLDataXMLEncoder(w, fields)
		if err != nil {
			return nil, err
		}
	case "SQL":
	case "XLSX", "HTML", "XLS", "DBF", "Parquet", "TXT":
		var err error
		if format == "XLSX" {
			e.additional, err = newMySQLDataXLSXEncoder(w, fields)
		} else if format == "HTML" {
			e.additional, err = newMySQLDataHTMLEncoder(w, fields)
		} else if format == "XLS" {
			e.additional, err = newMySQLDataXLSEncoder(w, fields)
		} else if format == "DBF" {
			e.additional, err = newMySQLDataDBFEncoder(w, fields)
		} else if format == "Parquet" {
			e.additional, err = newMySQLDataParquetEncoder(w, fields)
		} else {
			e.additional, err = newMySQLDataTXTEncoder(w, fields)
		}
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unsupported export format")
	}
	return e, nil
}
func (e *mysqlDataTransferEncoder) Row(values []mysqlsvc.MySQLDataValue) error {
	if len(values) != len(e.fields) {
		return mysqlsvc.ErrMySQLDataPage
	}
	text := make([]string, len(values))
	total := 0
	for i, value := range values {
		total += len(value.Text) + len(value.Bytes)
		if len(value.Text)+len(value.Bytes) > mysqlsvc.MySQLDataExportFieldMaxBytes || total > mysqlsvc.MySQLDataExportRowMaxBytes {
			return mysqlsvc.ErrMySQLDataEditLimit
		}
		text[i] = value.Text
		if value.Binary {
			text[i] = "0x" + hex.EncodeToString(value.Bytes)
		}
		if value.Null {
			text[i] = `\N`
		} else if strings.HasPrefix(text[i], `\`) {
			text[i] = `\` + text[i]
		}
	}
	switch e.format {
	case "XLSX", "HTML", "XLS", "DBF", "Parquet", "TXT":
		if err := e.additional.Row(values); err != nil {
			return err
		}
	case "XML":
		if err := e.xml.Row(values); err != nil {
			return err
		}
	case "CSV", "TSV":
		if err := e.csv.Write(text); err != nil {
			return err
		}
	case "JSON":
		object := map[string]any{}
		for i, field := range e.fields {
			if values[i].Null {
				object[field] = nil
			} else {
				value := values[i].Text
				if values[i].Binary {
					value = "0x" + hex.EncodeToString(values[i].Bytes)
				}
				object[field] = value
			}
		}
		data, err := json.Marshal(object)
		if err != nil {
			return err
		}
		prefix := "  "
		if e.rows > 0 {
			prefix = ",\n  "
		}
		if _, err = io.WriteString(e.w, prefix+string(data)); err != nil {
			return err
		}
	case "SQL":
		var names, literals []string
		for i, field := range e.fields {
			names = append(names, mysqlDataSQLIdentifier(field))
			value := values[i].Text
			if values[i].Binary {
				value = string(values[i].Bytes)
			}
			literals = append(literals, mysqlDataSQLLiteral(value, values[i].Null, values[i].Binary))
		}
		if _, err := io.WriteString(e.w, "INSERT INTO "+mysqlDataSQLIdentifier(e.db)+"."+mysqlDataSQLIdentifier(e.table)+" ("+strings.Join(names, ", ")+") VALUES ("+strings.Join(literals, ", ")+");\n"); err != nil {
			return err
		}
	}
	e.rows++
	return nil
}
func (e *mysqlDataTransferEncoder) Close() error {
	if e.additional != nil {
		return e.additional.Close()
	}
	if e.xml != nil {
		return e.xml.Close()
	}
	if e.csv != nil {
		e.csv.Flush()
		return e.csv.Error()
	}
	if e.format == "JSON" {
		_, err := io.WriteString(e.w, "\n]\n")
		return err
	}
	return nil
}

// encoding/json maps normally overwrite duplicate object keys. Reject them
// before conversion so an import never silently loses a supplied field value.
func mysqlDataValidateJSONImportKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('[') {
		return errors.New(locales.Text("JSON 文件必须只包含一个对象数组"))
	}
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return err
		}
		if token != json.Delim('{') {
			return errors.New(locales.Text("JSON 文件必须只包含一个对象数组"))
		}
		seen := map[string]bool{}
		for dec.More() {
			token, err = dec.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New(locales.Text("导入字段名不能为空或重复"))
			}
			seen[key] = true
			var value json.RawMessage
			if err = dec.Decode(&value); err != nil {
				return err
			}
		}
		if _, err = dec.Token(); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

// The user explicitly acknowledges external verification before abandoning an
// uncertain import. This releases retained input without replaying that batch.
func (s *State) resolveDataTransferLeave(services ui.Services, t *Document, close bool) {
	if t == nil || t.closed || t.transfer.Busy {
		return
	}
	next := t.transfer.LeaveNext
	t.transfer.LeaveOpen, t.transfer.LeaveNext = false, nil
	if !close {
		return
	}
	s.closeDataTransfer(t)
	t.transfer.Submitted = true
	t.edit.Uncertain, t.edit.UncertainKeys, t.edit.Drafts, t.edit.Editor = nil, nil, nil, nil
	if next != nil {
		next(services)
	}
}
