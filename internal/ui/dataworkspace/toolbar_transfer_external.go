package dataworkspace

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/datasources"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) SetDataSources(open func(context.Context, datasources.Config) (datasources.Source, error), capabilities func(context.Context, datasources.Config) []datasources.Capability) {
	s.SourceOpen = open
	s.SourceCapabilities = capabilities
}

func (s *State) dataExternalSourceControls(c *ui.Context, t *Document) {
	tr := &t.transfer
	tr.SourceConfig.Kind = datasources.Kind(tr.Format)
	if tr.Format != "ODBC" {
		mysqlDataWizardInput(c, &tr.SourceConfig.Path, locales.Text("源数据库文件路径")).FillWidth()
	}
	if tr.Format == "ODBC" || tr.Format == "Access" {
		mysqlDataWizardInput(c, &tr.SourceConfig.ConnectionString, locales.Text("临时 ODBC 连接字符串")).Password().FillWidth()
		mysqlDataWizardInput(c, &tr.SourceConfig.Driver, locales.Text("本机 ODBC 驱动名称"))
	}
	mysqlDataWizardInput(c, &tr.SourceConfig.ToolPath, locales.Text("MDBTools 程序目录（可选）"))
	mysqlDataWizardInput(c, &tr.SourceConfig.LibraryPath, locales.Text("本机组件库路径（可选）"))
	if ui.Button(c, locales.Text("检测组件并读取源表")).Disabled(tr.Busy || s.SourceOpen == nil).Clicked() {
		s.inspectExternalSource(c.Services(), t)
	}
	for _, cap := range tr.Capabilities {
		ui.Text(c, fmt.Sprintf("%s · %t · %s", cap.Kind, cap.Available, cap.Detail))
	}
	if len(tr.SourceTables) > 0 {
		choices := []string{}
		for _, table := range tr.SourceTables {
			choices = append(choices, strings.Join([]string{table.Catalog, table.Schema, table.Name}, "."))
		}
		tr.SourceTable = max(0, min(tr.SourceTable, len(choices)-1))
		choice := choices[tr.SourceTable]
		mysqlDataRuleSelect(c, &choice, choices, func(x string) string { return x }).Width(400)
		tr.SourceTable = slices.Index(choices, choice)
		if ui.Button(c, locales.Text("读取并校验源记录")).Disabled(tr.Busy).Clicked() {
			s.readExternalSource(c.Services(), t)
		}
	}
}

func (s *State) inspectExternalSource(services ui.Services, t *Document) {
	if t.transfer.Busy || s.SourceOpen == nil {
		return
	}
	config, open, capabilities := t.transfer.SourceConfig, s.SourceOpen, s.SourceCapabilities
	ctx, out, request, gen := s.beginDataTransfer(t)
	profile := t.profileID
	go func() {
		var caps []datasources.Capability
		if capabilities != nil {
			caps = capabilities(ctx, config)
		}
		source, err := open(ctx, config)
		var tables []datasources.Table
		if err == nil {
			tables, err = source.Tables(ctx)
			source.Close()
		}
		if tables == nil {
			tables = []datasources.Table{}
		}
		out <- mysqlDataTransferResult{SourceTables: tables, Capabilities: caps, Request: request, Generation: gen, Profile: profile, Err: err}
		services.Invalidate()
	}()
}

type mysqlDataExternalRows struct {
	source datasources.Source
	rows   datasources.RowReader
}

func (r *mysqlDataExternalRows) Fields() []string {
	fields := []string{}
	for _, col := range r.rows.Columns() {
		fields = append(fields, col.Name)
	}
	return fields
}
func (r *mysqlDataExternalRows) Close() error { err := r.rows.Close(); r.source.Close(); return err }
func (r *mysqlDataExternalRows) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	row, err := r.rows.Next(ctx)
	if err != nil {
		return nil, err
	}
	values := make([]mysqlsvc.MySQLDataValue, len(row))
	for i, v := range row {
		values[i] = mysqlsvc.MySQLDataValue{Text: v.Text, Bytes: slices.Clone(v.Bytes), Null: v.Null, Binary: v.Binary}
	}
	return values, nil
}

func (s *State) readExternalSource(services ui.Services, t *Document) {
	tr := &t.transfer
	if tr.Busy || s.SourceOpen == nil || tr.SourceTable < 0 || tr.SourceTable >= len(tr.SourceTables) {
		return
	}
	config, table, open, owner := tr.SourceConfig, tr.SourceTables[tr.SourceTable], s.SourceOpen, tr.Assets
	options := tr.Options
	sourceKey := mysqlDataCurrentSourceKey(tr)
	ctx, out, request, gen := s.beginDataTransfer(t)
	profile := t.profileID
	go func() {
		source, err := open(ctx, config)
		var spool *mysqlDataImportSpool
		var input *mysqlDataImportInput
		var columns []mysqlsvc.MySQLDataImportColumn
		if err == nil {
			rows, e := source.Rows(ctx, table)
			err = e
			if err == nil {
				spool, input, columns, err = mysqlDataSpoolRows(ctx, &mysqlDataRangeRowReader{reader: &mysqlDataExternalRows{source: source, rows: rows}, first: options.FirstRow, last: options.LastRow})
				if err == nil {
					err = owner.add(spool)
				}
			} else {
				source.Close()
			}
		}
		out <- mysqlDataTransferResult{RawSpool: spool, ParsedSourceKey: sourceKey, Input: input, Inferred: columns, Path: table.Name, Request: request, Generation: gen, Profile: profile, Err: err}
		services.Invalidate()
	}()
}

func (s *State) loadImportTarget(services ui.Services, t *Document) {
	if t.transfer.Busy {
		return
	}
	loader := s.runtime().TableMetadata
	if loader == nil {
		return
	}
	ctx, out, request, gen := s.beginDataTransfer(t)
	profile, db, table := t.profileID, t.database, mysqlDataImportDestination(t)
	go func() {
		snapshot, err := loader(ctx, profile, db, table)
		var columns []mysqlsvc.MySQLStructureColumn
		if err == nil && snapshot != nil {
			columns = slices.Clone(snapshot.Columns)
		}
		out <- mysqlDataTransferResult{TargetColumns: columns, Request: request, Generation: gen, Profile: profile, Err: err}
		services.Invalidate()
	}()
}
