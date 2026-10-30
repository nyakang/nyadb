package dataworkspace

import (
	"context"
	"errors"
	"slices"

	"github.com/egoist/mygo/ui"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

type mysqlDataPreparedSource struct {
	spool            *mysqlDataImportSpool
	request          mysqlsvc.MySQLDataImportRequest
	database, table  string
	ignore, extended bool
	maxBytes         int
}
type mysqlDataBatchReceipt struct {
	Database, Table string
	From, To        int64
	NewTable        bool
	Result          *mysqlsvc.MySQLDataImportStreamResult
}

func (s *State) preparedImportSources(t *Document) ([]mysqlDataPreparedSource, error) {
	s.rememberWizardSource(t)
	states := []mysqlDataTransferState{}
	if len(t.transfer.Sources) == 0 {
		states = append(states, mysqlDataCopyTransfer(t.transfer))
	} else {
		for _, source := range t.transfer.Sources {
			if source.State == nil {
				return nil, errors.New(locales.Text("请完成每个来源的映射、校验和表名确认"))
			}
			states = append(states, mysqlDataCopyTransfer(*source.State))
		}
	}
	plans := []mysqlDataPreparedSource{}
	for _, state := range states {
		shadow := *t
		shadow.transfer = state
		if state.MappedSpool == nil || state.ParsedSourceKey != mysqlDataCurrentSourceKey(&state) || state.PreparationKey != mysqlDataPreparationKey(&shadow) || !mysqlDataImportConfirmed(&shadow) {
			return nil, errors.New(locales.Text("请完成每个来源的映射、校验和表名确认"))
		}
		_, columns, _, err := mysqlDataImportMapping(&shadow)
		if err != nil {
			return nil, err
		}
		plans = append(plans, mysqlDataPreparedSource{spool: state.MappedSpool, request: mysqlDataImportServiceRequest(&shadow, columns, nil), database: t.database, table: mysqlDataImportDestination(&shadow), ignore: state.IgnoreForeignKeys, extended: state.ExtendedInsert, maxBytes: state.StatementKB << 10})
	}
	return plans, nil
}

func (s *State) executePreparedSources(services ui.Services, t *Document, importer mysqlDataStreamImporter, plans []mysqlDataPreparedSource) {
	tr := &t.transfer
	ctx, out, request, gen := s.beginDataTransfer(t)
	tr.Submitted = true
	tr.Step = 5
	profile, owner := t.profileID, tr.Assets
	progress := make(chan mysqlsvc.MySQLDataImportProgress, 1)
	tr.Progress = progress
	total := int64(0)
	for _, plan := range plans {
		total += plan.spool.rows
	}
	go func() {
		journal, err := newMySQLDataImportJournal(owner, total)
		aggregate := &mysqlsvc.MySQLDataImportStreamResult{}
		receipts := []mysqlDataBatchReceipt{}
		offset := int64(0)
		if err == nil {
			for _, plan := range plans {
				if err = ctx.Err(); err != nil {
					break
				}
				base := aggregate.Progress
				sourceOffset := offset
				observed := mysqlsvc.MySQLDataImportProgress{}
				stream := mysqlsvc.MySQLDataImportStreamRequest{Request: plan.request, Rows: plan.spool.rows, IgnoreForeignKeys: plan.ignore, ExtendedInsert: plan.extended, StatementMaxBytes: plan.maxBytes, Open: func(ctx context.Context) (mysqlsvc.MySQLDataImportRowSource, error) {
					rows, err := plan.spool.open(ctx)
					if err != nil {
						return nil, err
					}
					return &mysqlDataEditSpoolReader{rows: rows}, nil
				}, Record: func(row mysqlsvc.MySQLDataImportRowResult) error {
					if int64(row.Row) != observed.Processed+1 || observed.Processed >= plan.spool.rows {
						return errors.New("import result journal has a duplicate or out-of-order row")
					}
					row.Row += int(sourceOffset)
					row.Database, row.Table = plan.database, plan.table
					if err := journal.Record(row); err != nil {
						return err
					}
					observed.Processed++
					switch row.Status {
					case "prepared", "committed":
						observed.Prepared++
					case "skipped":
						observed.Skipped++
					case "failed":
						observed.Failed++
					default:
						return errors.New("import result journal has an invalid row status")
					}
					return nil
				}, Progress: func(p mysqlsvc.MySQLDataImportProgress) {
					p.Processed += base.Processed
					p.Prepared += base.Prepared
					p.Skipped += base.Skipped
					p.Failed += base.Failed
					select {
					case progress <- p:
					default:
					}
				}}
				result, e := importer(ctx, profile, plan.database, plan.table, stream)
				if result == nil {
					result = &mysqlsvc.MySQLDataImportStreamResult{CommitUncertain: observed.Prepared > 0, Progress: observed}
				}
				if result.Committed && (e != nil || observed.Processed != plan.spool.rows || observed != result.Progress) {
					result.Committed, result.CommitUncertain = false, true
					e = errors.Join(e, errors.New("commit receipt does not match the complete import result journal"))
				}
				receipts = append(receipts, mysqlDataBatchReceipt{Database: plan.database, Table: plan.table, From: offset, To: offset + plan.spool.rows, NewTable: plan.request.NewTable, Result: result})
				aggregate.Progress.Processed += result.Progress.Processed
				aggregate.Progress.Skipped += result.Progress.Skipped
				aggregate.Progress.Failed += result.Progress.Failed
				if result.Committed {
					aggregate.Progress.Prepared += result.Progress.Prepared
				}
				aggregate.CommitUncertain = aggregate.CommitUncertain || result.CommitUncertain
				aggregate.CreationUncertain = aggregate.CreationUncertain || result.CreationUncertain
				aggregate.Created = aggregate.Created || result.Created
				for _, row := range result.Rows[:min(100-len(aggregate.Rows), len(result.Rows))] {
					row.Row += int(offset)
					row.Database, row.Table = plan.database, plan.table
					aggregate.Rows = append(aggregate.Rows, row)
				}
				offset += plan.spool.rows
				if e != nil || !result.Committed {
					err = e
					if err == nil {
						err = errors.New("source import has no final commit receipt")
					}
					break
				}
			}
		}
		aggregate.Committed = err == nil && len(receipts) == len(plans)
		if journal != nil {
			if e := journal.Flush(); e != nil && err == nil {
				err = e
				aggregate.CommitUncertain = true
			}
		}
		destination := ""
		if len(plans) > 0 {
			destination = plans[len(plans)-1].table
		}
		out <- mysqlDataTransferResult{Batch: receipts, Journal: journal, Streamed: aggregate, Request: request, Generation: gen, Profile: profile, Destination: destination, TotalRows: total, Write: true, Err: err}
		services.Invalidate()
	}()
}

func mysqlDataBatchRowSettlement(batch []mysqlDataBatchReceipt, index int64, stale bool) (bool, bool) {
	for _, receipt := range batch {
		if index >= receipt.From && index < receipt.To {
			if receipt.Result == nil {
				return false, true
			}
			return receipt.Result.Committed, stale || receipt.Result.CommitUncertain
		}
	}
	return false, stale
}

func (s *State) publishImportBatch(services ui.Services, t *Document, result mysqlDataTransferResult, stale bool) {
	t.transfer.Batch = slices.Clone(result.Batch)
	for _, receipt := range result.Batch {
		r := receipt.Result
		if (r.Created || r.CreationUncertain || r.Committed) && s.deps.TableImported != nil {
			s.deps.TableImported(services, TableImportEvent{ProfileID: result.Profile, Generation: result.Generation, Database: receipt.Database, Table: receipt.Table, Created: r.Created, Completed: r.Committed, Uncertain: stale || r.CreationUncertain || r.CommitUncertain})
		}
		if r.Committed && receipt.Database == t.database && receipt.Table == t.table && !stale {
			s.Load(services, t, true)
		}
	}
}
