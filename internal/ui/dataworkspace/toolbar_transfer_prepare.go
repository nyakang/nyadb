package dataworkspace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

type mysqlDataStreamImporter func(context.Context, int64, string, string, mysqlsvc.MySQLDataImportStreamRequest) (*mysqlsvc.MySQLDataImportStreamResult, error)

func mysqlDataDecodeBase64Value(text string) (mysqlsvc.MySQLDataValue, error) {
	b, err := base64.StdEncoding.DecodeString(text)
	return mysqlsvc.MySQLDataValue{Binary: true, Bytes: b}, err
}
func mysqlDataConvertDate(text, kind string, o mysqlDataSourceOptions) (string, error) {
	if kind == "time" {
		if o.TimeSeparator != "" && o.TimeSeparator != ":" {
			text = strings.ReplaceAll(text, o.TimeSeparator, ":")
		}
		return text, nil
	}
	date, timePart, _ := strings.Cut(text, " ")
	if o.DateSeparator == "" {
		o.DateSeparator = "-"
	}
	parts := strings.Split(date, o.DateSeparator)
	if len(parts) != 3 {
		return "", errors.New("invalid date")
	}
	switch o.DateOrder {
	case "DMY":
		parts = []string{parts[2], parts[1], parts[0]}
	case "MDY":
		parts = []string{parts[2], parts[0], parts[1]}
	}
	text = strings.Join(parts, "-")
	if kind == "date" {
		if _, err := time.Parse("2006-01-02", text); err != nil {
			return "", err
		}
		return text, nil
	}
	if o.TimeSeparator != "" && o.TimeSeparator != ":" {
		timePart = strings.ReplaceAll(timePart, o.TimeSeparator, ":")
	}
	return text + " " + timePart, nil
}

func (s *State) readDataImportPath(services ui.Services, t *Document, path string) {
	if t.transfer.Busy || t.transfer.Submitted {
		return
	}
	ctx, out, request, gen := s.beginDataTransfer(t)
	profile, format, options, owner := t.profileID, t.transfer.Format, t.transfer.Options, t.transfer.Assets
	go func() {
		f, err := os.Open(path)
		var spool *mysqlDataImportSpool
		var input *mysqlDataImportInput
		var columns []mysqlsvc.MySQLDataImportColumn
		if err == nil {
			spool, input, columns, err = mysqlDataReadSpool(ctx, f, format, options, owner)
			f.Close()
		}
		out <- mysqlDataTransferResult{RawSpool: spool, ParsedSourceKey: mysqlDataSourceKey(format, options), Input: input, Inferred: columns, Path: path, Err: err, Profile: profile, Request: request, Generation: gen}
		services.Invalidate()
	}()
}

type mysqlDataMappingReader struct {
	raw             mysqlDataImportRowReader
	mapping, fields []string
	columns         []mysqlsvc.MySQLStructureColumn
	options         mysqlDataSourceOptions
	empty           bool
	request         mysqlsvc.MySQLDataImportRequest
	changed         int64
}

func (r *mysqlDataMappingReader) Fields() []string { return append([]string(nil), r.fields...) }
func (r *mysqlDataMappingReader) Close() error     { return r.raw.Close() }
func (r *mysqlDataMappingReader) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	row, err := r.raw.Next(ctx)
	if err != nil {
		return nil, err
	}
	values, changed, err := mysqlDataMappedRow(row, r.mapping, r.columns, r.options, r.empty)
	if err != nil {
		return nil, err
	}
	edit := mysqlsvc.MySQLDataEdit{Kind: "insert", Values: map[string]mysqlsvc.MySQLDataValue{}}
	for i, name := range r.fields {
		edit.Values[name] = values[i]
	}
	request := r.request
	request.Edits = []mysqlsvc.MySQLDataEdit{edit}
	if err = mysqlsvc.ValidateMySQLDataImportRequest(r.columns, request); err != nil {
		return nil, err
	}
	r.changed += changed
	return values, nil
}

func (s *State) prepareDataImport(services ui.Services, t *Document) {
	tr := &t.transfer
	if tr.Busy || tr.RawSpool == nil || tr.Submitted {
		return
	}
	if tr.ParsedSourceKey != mysqlDataCurrentSourceKey(tr) {
		tr.Err = locales.Text("源设置已改变，请重新读取来源后校验")
		return
	}
	mapping, columns, metadata, err := mysqlDataImportMapping(t)
	if err != nil {
		tr.Err = err.Error()
		return
	}
	fields := []string{}
	seen := map[string]bool{}
	for _, name := range mapping {
		if name == "" {
			continue
		}
		if seen[name] {
			tr.Err = locales.Text("字段映射包含重复、未知或生成字段")
			return
		}
		seen[name] = true
		fields = append(fields, name)
	}
	if len(fields) == 0 {
		tr.Err = locales.Text("请映射至少一个字段")
		return
	}
	requestValues := mysqlDataImportServiceRequest(t, columns, nil)
	options, empty, raw, owner, key := tr.Options, tr.EmptyAsNull, tr.RawSpool, tr.Assets, mysqlDataPreparationKey(t)
	mapping = slices.Clone(mapping)
	metadata = slices.Clone(metadata)
	ctx, out, request, gen := s.beginDataTransfer(t)
	profile := t.profileID
	go func() {
		reader, err := raw.open(ctx)
		var spool *mysqlDataImportSpool
		changed := int64(0)
		if err == nil {
			mapped := &mysqlDataMappingReader{raw: reader, mapping: mapping, fields: fields, columns: metadata, options: options, empty: empty, request: requestValues}
			spool, _, _, err = mysqlDataSpoolRows(ctx, mapped)
			changed = mapped.changed
			if err == nil {
				err = owner.add(spool)
			}
		}
		out <- mysqlDataTransferResult{MappedSpool: spool, PreparationKey: key, TotalRows: raw.rows, ChangedValues: changed, Request: request, Generation: gen, Profile: profile, Err: err}
		services.Invalidate()
	}()
}

type mysqlDataEditSpoolReader struct{ rows *mysqlDataSpoolReader }

func (r *mysqlDataEditSpoolReader) Close() error { return r.rows.Close() }
func (r *mysqlDataEditSpoolReader) Next(ctx context.Context) (mysqlsvc.MySQLDataEdit, error) {
	row, err := r.rows.Next(ctx)
	if err != nil {
		return mysqlsvc.MySQLDataEdit{}, err
	}
	edit := mysqlsvc.MySQLDataEdit{Kind: "insert", Values: map[string]mysqlsvc.MySQLDataValue{}}
	for i, name := range r.rows.fields {
		edit.Values[name] = row[i]
	}
	return edit, nil
}

func (s *State) startPreparedDataImport(services ui.Services, t *Document) {
	tr := &t.transfer
	importer := s.StreamImporter
	if importer == nil {
		importer = s.runtime().StreamImporter
	}
	if importer == nil {
		tr.Err = locales.Text("数据编辑服务不可用")
		return
	}
	if tr.Busy || tr.Submitted || tr.MappedSpool == nil || tr.PreparationKey != mysqlDataPreparationKey(t) || !mysqlDataImportConfirmed(t) || t.closed || !s.session().Connected || s.session().ProfileID != t.profileID || s.session().Generation != tr.Generation || dataTransactionActive(t) || dataTransactionBusy(t) || mysqlDataHasDraft(t) {
		return
	}
	plans, err := s.preparedImportSources(t)
	if err != nil {
		tr.Err = err.Error()
		return
	}
	s.executePreparedSources(services, t, importer, plans)
}

func (s *State) consumeStreamedDataImport(services ui.Services, t *Document, r mysqlDataTransferResult, stale bool) {
	tr, result := &t.transfer, r.Streamed
	s.publishImportBatch(services, t, r, stale)
	for _, receipt := range r.Batch {
		result := receipt.Result
		if result == nil {
			continue
		}
		committed := int64(0)
		if result.Committed {
			committed = result.Progress.Prepared
		}
		s.appendDataHistory(t, mysqlDataHistoryEntry{Kind: "IMPORT", Summary: true, Failed: !result.Committed || stale || result.CommitUncertain, Rows: int(committed), Message: fmt.Sprintf(locales.Text("目标 %s · 已提交 %d 行 · 跳过 %d 行 · 失败 %d 行"), receipt.Database+"."+receipt.Table, committed, result.Progress.Skipped, result.Progress.Failed)})
	}
	tr.Progress = nil
	tr.Journal = r.Journal
	tr.JournalCommitted = result.Committed
	tr.JournalUncertain = stale || result.CommitUncertain
	if len(r.Batch) > 0 {
		tr.JournalUncertain = stale
	}
	tr.Processed = result.Progress.Processed
	tr.Created = result.Created
	tr.ResultTable = r.Destination
	tr.RowResults = slices.Clone(result.Rows)
	tr.Uncertain = stale || result.CreationUncertain || result.CommitUncertain || r.Err == nil && (!result.Committed || result.Progress.Processed != r.TotalRows) || result.Committed && r.Err != nil
	if tr.Uncertain {
		tr.Err = locales.Text("导入结果不确定，禁止重放；请核验数据库")
		if !r.NewTable {
			t.edit.Uncertain = &mysqlsvc.MySQLDataEditResult{CommitUncertain: true}
		}
	} else if r.Err != nil {
		tr.Err = fmt.Sprintf(locales.Text("已提交 %d 行；后续导入失败，本次向导不会重放："), result.Progress.Prepared) + r.Err.Error()
	} else {
		tr.Completed = true
		tr.Err = fmt.Sprintf(locales.Text("已提交 %d 行 · 跳过 %d 行 · 失败 %d 行"), result.Progress.Prepared, result.Progress.Skipped, result.Progress.Failed)
		if len(r.Batch) == 0 && !r.NewTable && r.Destination == t.table {
			s.Load(services, t, true)
		}
	}
	if len(r.Batch) == 0 && (result.Created || result.CreationUncertain) && s.deps.TableImported != nil {
		s.deps.TableImported(services, TableImportEvent{ProfileID: r.Profile, Generation: r.Generation, Database: t.database, Table: r.Destination, Created: result.Created, Uncertain: tr.Uncertain, Completed: tr.Completed})
	}
	s.setStatus(tr.Err)
}

func (s *State) chooseDataExportPath(services ui.Services, t *Document) {
	if t.transfer.Busy {
		return
	}
	_, out, request, gen := s.beginDataTransfer(t)
	profile, format, table := t.profileID, t.transfer.Format, t.table
	dialog := t.transfer.SaveDialog
	if dialog == nil {
		dialog = mygo.Dialog.Save
	}
	go func() {
		path, err := dialog(mygo.SaveDialogOptions{Title: locales.Text("导出数据文件"), DefaultPath: table + "." + strings.ToLower(format)})
		out <- mysqlDataTransferResult{PickedPath: true, Path: path, Err: err, Request: request, Generation: gen, Profile: profile}
		services.Invalidate()
	}()
}

func (s *State) dataImportConversionPreview(c *ui.Context, t *Document) {
	mapping, _, metadata, err := mysqlDataImportMapping(t)
	if err != nil {
		return
	}
	for i, row := range t.transfer.Input.Rows[:min(5, len(t.transfer.Input.Rows))] {
		values, changed, e := mysqlDataMappedRow(row, mapping, metadata, t.transfer.Options, t.transfer.EmptyAsNull)
		if e != nil {
			ui.Text(c, e.Error())
			continue
		}
		if changed > 0 {
			ui.Text(c, fmt.Sprintf(locales.Text("第 %d 行 · 原值 %s → 转换值 %s"), i+1, mysqlDataPreviewValues(row), mysqlDataPreviewValues(values)))
		}
	}
}
func mysqlDataPreviewValues(row []mysqlsvc.MySQLDataValue) string {
	values := []string{}
	for _, v := range row {
		if v.Null {
			values = append(values, "NULL")
		} else if v.Binary {
			values = append(values, fmt.Sprintf("0x%x", v.Bytes[:min(16, len(v.Bytes))]))
		} else {
			runes := []rune(v.Text)
			values = append(values, string(runes[:min(80, len(runes))]))
		}
	}
	return strings.Join(values, " · ")
}

type mysqlDataTransferPreset struct {
	Name                                         string
	Format, Scope                                string
	Options                                      mysqlDataSourceOptions
	Empty, Continue, Extended, IgnoreForeignKeys bool
	StatementKB                                  int
	Mode                                         string
	Mapping, Match, Fields                       []string
	Columns                                      []mysqlsvc.MySQLDataImportColumn
}

type mysqlDataTransferPresets struct {
	Version int
	Items   []mysqlDataTransferPreset
}

func mysqlDataValidPreset(p mysqlDataTransferPreset, kind string) bool {
	if strings.TrimSpace(p.Name) != p.Name || p.Name == "" || len([]rune(p.Name)) > 64 || strings.ContainsAny(p.Name, "\x00\r\n") || !slices.Contains(mysqlDataTransferFormatChoices(kind), p.Format) || !slices.Contains([]string{"page", "selection", "all"}, p.Scope) {
		return false
	}
	if !slices.Contains([]string{"", "append", "update", "upsert", "insert_missing", "delete", "replace"}, p.Mode) || len(p.Mapping) > 128 || len(p.Match) > 128 || len(p.Columns) > 128 || len(p.Fields) > 128 || p.StatementKB < 0 || p.StatementKB > 16384 {
		return false
	}
	o := p.Options
	if _, err := mysqlDataTextEncoding(o.Encoding); err != nil {
		return false
	}
	if !slices.Contains([]string{"preserve", "navicat_xml"}, o.Compatibility) || !slices.Contains([]string{"hex", "base64"}, o.BinaryEncoding) || !slices.Contains([]string{"YMD", "DMY", "MDY"}, o.DateOrder) || o.HeaderRow < 0 || o.FirstRow < 1 || o.LastRow < 0 || o.Sheet < 0 {
		return false
	}
	if len(o.DateSeparator) > 8 || len(o.TimeSeparator) > 8 || len(o.DecimalSeparator) > 8 || len(o.XMLRoot) > 128 || len(o.XMLRow) > 128 {
		return false
	}
	if slices.Contains([]string{"CSV", "TSV", "TXT"}, p.Format) {
		if _, _, err := mysqlDataTextCharacters(o); err != nil {
			return false
		}
	}
	return true
}

func (s *State) readTransferPresets(key, kind string) mysqlDataTransferPresets {
	saved := mysqlDataTransferPresets{Version: 1}
	if s.dataLayoutPreferences.read == nil {
		return saved
	}
	value, err := s.dataLayoutPreferences.read(key)
	if err != nil || len(value) > 256<<10 || json.Unmarshal([]byte(value), &saved) != nil || saved.Version != 1 || len(saved.Items) > 20 {
		return mysqlDataTransferPresets{Version: 1}
	}
	for _, p := range saved.Items {
		if !mysqlDataValidPreset(p, kind) {
			return mysqlDataTransferPresets{Version: 1}
		}
	}
	return saved
}

func (s *State) dataTransferPresetControls(c *ui.Context, t *Document) {
	tr := &t.transfer
	ui.TextInput(c, &tr.PresetName).Label(locales.Text("向导配置名称"))
	key := strings.Replace(mysqlDataProfilesKey(t), "mysql_data_profiles:", "mysql_data_transfer:", 1) + ":" + tr.Kind
	p := mysqlDataTransferPreset{Name: strings.TrimSpace(tr.PresetName), Format: tr.Format, Scope: tr.Scope, Options: tr.Options, Empty: tr.EmptyAsNull, Continue: tr.ContinueOnError, Mode: tr.Mode, Mapping: slices.Clone(tr.Mapping), Match: slices.Clone(tr.MatchColumns), Columns: slices.Clone(tr.NewColumns), Fields: slices.Clone(tr.Fields), Extended: tr.ExtendedInsert, IgnoreForeignKeys: tr.IgnoreForeignKeys, StatementKB: tr.StatementKB}
	if ui.Button(c, locales.Text("保存向导配置")).Disabled(s.dataLayoutPreferences.write == nil || tr.Busy || !mysqlDataValidPreset(p, tr.Kind)).Clicked() {
		saved := s.readTransferPresets(key, tr.Kind)
		saved.Items = slices.DeleteFunc(saved.Items, func(x mysqlDataTransferPreset) bool { return x.Name == p.Name })
		if len(saved.Items) >= 20 {
			tr.Err = locales.Text("最多保存 20 个向导配置")
		} else {
			saved.Items = append(saved.Items, p)
			value, err := json.Marshal(saved)
			if err == nil && len(value) <= 256<<10 {
				err = s.dataLayoutPreferences.write(key, string(value))
			} else {
				err = errors.New("wizard preset exceeds storage limit")
			}
			if err != nil {
				tr.Err = locales.Text("保存表配置文件失败")
			}
		}
	}
	if ui.Button(c, locales.Text("加载向导配置")).Disabled(s.dataLayoutPreferences.read == nil || tr.Busy).Clicked() {
		for _, p := range s.readTransferPresets(key, tr.Kind).Items {
			if p.Name != tr.PresetName {
				continue
			}
			tr.Options, tr.Format, tr.Scope, tr.Mode = p.Options, p.Format, p.Scope, p.Mode
			tr.EmptyAsNull, tr.ContinueOnError, tr.ExtendedInsert, tr.IgnoreForeignKeys, tr.StatementKB = p.Empty, p.Continue, p.Extended, p.IgnoreForeignKeys, max(1, p.StatementKB)
			if len(p.Mapping) == len(tr.Mapping) {
				tr.Mapping = slices.Clone(p.Mapping)
				tr.MatchColumns = slices.Clone(p.Match)
			}
			if len(p.Columns) == len(tr.NewColumns) {
				tr.NewColumns = slices.Clone(p.Columns)
			}
			tr.PreparationKey, tr.Confirmation, tr.DestructiveConfirmation = "", "", ""
			break
		}
	}
	if ui.Button(c, locales.Text("删除向导配置")).Disabled(s.dataLayoutPreferences.write == nil || tr.Busy || tr.PresetName == "").Clicked() {
		saved := s.readTransferPresets(key, tr.Kind)
		saved.Items = slices.DeleteFunc(saved.Items, func(p mysqlDataTransferPreset) bool { return p.Name == tr.PresetName })
		value, err := json.Marshal(saved)
		if err == nil {
			err = s.dataLayoutPreferences.write(key, string(value))
		}
		if err != nil {
			tr.Err = locales.Text("保存表配置文件失败")
		}
	}
}
