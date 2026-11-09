package dataworkspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
	"github.com/nyakang/nyadb/locales"
)

type graphImportTicket struct {
	mu      sync.Mutex
	revoked bool
	plan    contract.GraphImportPlan
}

func (t *graphImportTicket) publish(p contract.GraphImportPlan) {
	t.mu.Lock()
	if t.revoked {
		t.mu.Unlock()
		if p != nil {
			_ = p.Close()
		}
		return
	}
	t.plan = p
	t.mu.Unlock()
}
func (t *graphImportTicket) take() contract.GraphImportPlan {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.plan
	t.plan = nil
	return p
}
func (t *graphImportTicket) revoke() {
	t.mu.Lock()
	t.revoked = true
	p := t.plan
	t.plan = nil
	t.mu.Unlock()
	if p != nil {
		_ = p.Close()
	}
}

func releaseGraphImportPlan(tr *mysqlDataTransferState) {
	if tr.GraphTicket != nil {
		tr.GraphTicket.revoke()
		tr.GraphTicket = nil
	}
	if tr.GraphPlan != nil {
		_ = tr.GraphPlan.Close()
		tr.GraphPlan = nil
	}
}
func (s *State) graphImportBound(d *Document) bool {
	r := s.runtimeFor(d)
	return r.PreviewGraphImport != nil && r.SubmitGraphImport != nil
}
func (s *State) graphTransferAvailable(d *Document) bool {
	return d != nil && !d.closed && s.canUseSession(d) && s.sessionFor(d).Generation == d.transfer.Generation && !d.loading && !d.edit.Busy && d.edit.Uncertain == nil && !mysqlDataHasDraft(d) && !dataTransactionActive(d) && !dataTransactionBusy(d)
}

func (s *State) chooseGraphImportFile(services ui.Services, d *Document) {
	s.readGraphImportFile(services, d, "")
}

func (s *State) readGraphImportFile(services ui.Services, d *Document, sourcePath string) {
	tr := &d.transfer
	if tr.Busy || tr.Submitted || tr.Uncertain || !s.graphTransferAvailable(d) {
		return
	}
	releaseGraphImportPlan(tr)
	tr.GraphSnapshot = contract.GraphImportSnapshot{}
	tr.GraphBundle = nil
	tr.Path = ""
	ctx, out, request, generation := s.beginDataTransfer(d)
	var pickerDone chan struct{}
	if sourcePath == "" {
		pickerDone = s.beginDataPicker(d)
	}
	format, profile := tr.Format, d.profileID
	open := tr.OpenDialog
	if open == nil {
		open = mygo.Dialog.Open
	}
	go func() {
		paths, err := []string{sourcePath}, error(nil)
		if pickerDone != nil {
			paths, err = open(mygo.OpenDialogOptions{Title: locales.Text("选择图交换文件"), Filters: []mygo.FileFilter{{Name: format, Extensions: []string{strings.ToLower(format)}}}})
			close(pickerDone)
			services.Invalidate()
		}
		var bundle *contract.GraphImportBundle
		path := ""
		if err == nil && len(paths) > 0 && ctx.Err() == nil {
			path = paths[0]
			f, openErr := os.Open(path)
			err = openErr
			if err == nil {
				b, parseErr := graphimport.Parse(f, format)
				_ = f.Close()
				err = parseErr
				if err == nil {
					bundle = &b
				}
			}
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		out <- mysqlDataTransferResult{GraphExchange: true, GraphBundle: bundle, Path: path, Request: request, Generation: generation, Profile: profile, Err: err}
		services.Invalidate()
	}()
}

func graphTransferRequest(tr *mysqlDataTransferState) (contract.GraphImportRequest, error) {
	if tr.GraphBundle == nil {
		return contract.GraphImportRequest{}, contract.ErrUnsupported
	}
	r := contract.GraphImportRequest{Bundle: *tr.GraphBundle, Bindings: tr.GraphBindings}
	if err := graphimport.ValidateRequest(r); err != nil {
		return contract.GraphImportRequest{}, err
	}
	return contract.CloneGraphImportRequest(r), nil
}

func (s *State) previewGraphTransfer(services ui.Services, d *Document) {
	tr := &d.transfer
	r := s.runtimeFor(d)
	if !s.graphTransferAvailable(d) || tr.Busy || tr.Submitted || tr.Uncertain || r.PreviewGraphImport == nil || r.SubmitGraphImport == nil {
		return
	}
	request, err := graphTransferRequest(tr)
	if err != nil {
		tr.Err = err.Error()
		return
	}
	releaseGraphImportPlan(tr)
	ctx, out, number, generation := s.beginDataTransfer(d)
	ticket := &graphImportTicket{}
	tr.GraphTicket = ticket
	preview, profile, database, name := r.PreviewGraphImport, d.profileID, d.database, d.table
	go func() {
		p, err := preview(ctx, profile, database, name, request)
		ticket.publish(p)
		if err == nil {
			err = ctx.Err()
		}
		out <- mysqlDataTransferResult{GraphExchange: true, GraphPreview: true, Request: number, Generation: generation, Profile: profile, Err: err}
		services.Invalidate()
	}()
}

func (s *State) confirmGraphTransfer(services ui.Services, d *Document) {
	tr := &d.transfer
	submit := s.runtimeFor(d).SubmitGraphImport
	if !s.graphTransferAvailable(d) || tr.Busy || tr.Submitted || tr.Uncertain || tr.GraphPlan == nil || submit == nil || tr.Confirmation != d.database {
		return
	}
	r, err := graphTransferRequest(tr)
	if err != nil || !reflect.DeepEqual(r, tr.GraphSnapshot.Request) || !reflect.DeepEqual(tr.GraphPlan.Snapshot(), tr.GraphSnapshot) {
		tr.Err = contract.ErrDataConflict.Error()
		releaseGraphImportPlan(tr)
		return
	}
	p, profile, database, name := tr.GraphPlan, d.profileID, d.database, d.table
	tr.GraphPlan = nil
	releaseGraphImportPlan(tr)
	snapshot := contract.CloneGraphImportSnapshot(tr.GraphSnapshot)
	tr.Submitted, tr.Step = true, 3
	ctx, out, number, generation := s.beginDataTransfer(d)
	description := "CREATE · " + database + " · " + graphimport.Format
	complete := s.beginActivity(d, "import", description)
	go func() {
		started := time.Now()
		result, err := submit(ctx, profile, database, name, p)
		_ = p.Close()
		activity := contract.ActivityOutcome(err)
		if result != nil && result.OutcomeUnknown || err == nil && !graphImportReceiptComplete(snapshot, result) {
			activity.State = "uncertain"
		}
		if err == nil && graphImportReceiptComplete(snapshot, result) {
			activity.AffectedRows = int64(result.NodesCreated + result.RelationshipsCreated)
		}
		contract.CompleteActivity(complete, activity)
		history := []mysqlDataHistoryEntry{{Kind: "IMPORT", SQL: description, Summary: true, At: time.Now(), Duration: time.Since(started)}}
		out <- mysqlDataTransferResult{GraphExchange: true, GraphResult: contract.CloneGraphImportResult(result), Write: true, Request: number, Generation: generation, Profile: profile, History: history, Err: err}
		services.Invalidate()
	}()
}

// A complete mapping covers every binding and created source exactly once.
// Counts or a successful statement alone cannot authorize completion.
func graphImportReceiptComplete(snapshot contract.GraphImportSnapshot, r *contract.GraphImportResult) bool {
	if r == nil || r.Object != snapshot.Object || !r.BeginConfirmed || !r.CommitAttempted || !r.CommitConfirmed || r.OutcomeUnknown || r.RollbackAttempted || r.RollbackConfirmed || r.NativeJSON == "" || r.NodesCreated != snapshot.Nodes || r.RelationshipsCreated != len(snapshot.Request.Bundle.Relationships) {
		return false
	}
	wanted := map[string]string{}
	for _, n := range snapshot.Request.Bundle.Nodes {
		wanted["node\x00"+n.ElementID] = ""
	}
	for _, b := range snapshot.Request.Bindings {
		delete(wanted, "node\x00"+b.SourceID)
		wanted["binding\x00"+b.SourceID] = b.DestinationID
	}
	for _, r := range snapshot.Request.Bundle.Relationships {
		wanted["relationship\x00"+r.ElementID] = ""
	}
	if len(r.Identities) != len(wanted) {
		return false
	}
	seen, destinations := map[string]bool{}, map[string]bool{}
	for _, binding := range snapshot.Request.Bindings {
		destinations["node\x00"+binding.DestinationID] = true
	}
	for _, identity := range r.Identities {
		key := identity.Kind + "\x00" + identity.SourceID
		destination, exists := wanted[key]
		if !exists || seen[key] || identity.DestinationID == "" || len(identity.DestinationID) > 1024 || destination != "" && destination != identity.DestinationID {
			return false
		}
		if identity.Kind != "binding" {
			target := identity.Kind + "\x00" + identity.DestinationID
			if destinations[target] {
				return false
			}
			destinations[target] = true
		}
		seen[key] = true
	}
	return true
}

func (s *State) consumeGraphTransfer(services ui.Services, d *Document, r mysqlDataTransferResult, stale bool) {
	tr := &d.transfer
	if r.Write {
		tr.GraphResult = contract.CloneGraphImportResult(r.GraphResult)
		if r.GraphResult != nil && r.GraphResult.NativeJSON != "" {
			d.nativeReceipt.JSON = r.GraphResult.NativeJSON
		}
		complete := !stale && r.Err == nil && graphImportReceiptComplete(tr.GraphSnapshot, r.GraphResult)
		uncertain := stale || r.GraphResult != nil && r.GraphResult.OutcomeUnknown || r.Err == nil && !complete
		for _, entry := range r.History {
			entry.Failed = !complete
			if complete {
				entry.Rows = r.GraphResult.NodesCreated + r.GraphResult.RelationshipsCreated
			} else {
				entry.Message = mysqlDataHistoryError(r.Err, stale, false, uncertain)
			}
			s.appendDataHistory(d, entry)
		}
		tr.Completed, tr.Uncertain = complete, uncertain
		if uncertain {
			tr.Err = locales.Text("导入结果不确定，禁止重放；请核验数据库")
			d.edit.Uncertain = &contract.DataEditResult{CommitUncertain: true, NativeJSON: d.nativeReceipt.JSON}
			d.edit.Err = tr.Err
		} else if r.Err != nil {
			tr.Err = locales.Text("导入失败；本次向导不会重放：") + r.Err.Error()
		} else {
			tr.Err = fmt.Sprintf(locales.Text("已导入 %d 个节点和 %d 条关系"), r.GraphResult.NodesCreated, r.GraphResult.RelationshipsCreated)
			s.setStatus(tr.Err)
		}
		if s.deps.TableImported != nil && (complete || uncertain) {
			s.deps.TableImported(services, TableImportEvent{ProfileID: r.Profile, Generation: r.Generation, Database: d.database, Table: d.table, Completed: complete, Uncertain: uncertain})
		}
		if complete {
			s.Load(services, d, true)
		}
		return
	}
	if r.GraphPreview {
		if tr.GraphTicket != nil {
			tr.GraphPlan = tr.GraphTicket.take()
		}
		if stale || r.Err != nil || tr.GraphPlan == nil {
			releaseGraphImportPlan(tr)
			if stale {
				tr.Err = contract.ErrRevoked.Error()
			} else if r.Err != nil {
				tr.Err = r.Err.Error()
			} else {
				tr.Err = contract.ErrUnsupported.Error()
			}
			return
		}
		snapshot := tr.GraphPlan.Snapshot()
		request, err := graphTransferRequest(tr)
		// Canonicalization may normalize property components. Check immutable
		// source references and explicit bindings, then display the full result.
		if err != nil || snapshot.Object != d.object || snapshot.NativeJSON == "" || !graphimport.EquivalentRequest(snapshot.Request, request) || !graphImportAnchorsComplete(snapshot) {
			releaseGraphImportPlan(tr)
			tr.Err = contract.ErrDataConflict.Error()
			return
		}
		tr.GraphSnapshot = contract.CloneGraphImportSnapshot(snapshot)
		b := contract.CloneGraphImportBundle(snapshot.Request.Bundle)
		tr.GraphBundle = &b
		tr.Step = 2
		tr.Confirmation = ""
		return
	}
	if stale {
		tr.Err = contract.ErrRevoked.Error()
		return
	}
	if r.Err != nil {
		if !errors.Is(r.Err, context.Canceled) {
			tr.Err = r.Err.Error()
		}
		return
	}
	if r.GraphBundle != nil {
		b := contract.CloneGraphImportBundle(*r.GraphBundle)
		tr.GraphBundle = &b
		tr.Path = r.Path
		tr.GraphSourcePath = r.Path
		tr.GraphBindings = nil
		tr.GraphSnapshot = contract.GraphImportSnapshot{}
		tr.GraphBindingSource, tr.GraphBindingDestination = "", ""
		return
	}
	if r.Path != "" {
		tr.Completed = true
		tr.Step = 3
		tr.Path = r.Path
		tr.Err = locales.Text("图交换导出完成")
		s.setStatus(tr.Err)
	}
}

func (s *State) startGraphExport(services ui.Services, d *Document) {
	tr := &d.transfer
	export := s.runtimeFor(d).ExportGraph
	if !s.graphTransferAvailable(d) || tr.Busy || tr.Submitted || tr.Uncertain || export == nil || tr.Format != "JSON" && tr.Format != "JSONL" {
		return
	}
	ctx, out, number, generation := s.beginDataTransfer(d)
	tr.Submitted = true
	format, object, path := tr.Format, d.object, tr.ExportPath
	save := tr.SaveDialog
	if save == nil {
		save = mygo.Dialog.Save
	}
	var pickerDone chan struct{}
	if path == "" {
		pickerDone = s.beginDataPicker(d)
	}
	go func() {
		var err error
		if path == "" {
			path, err = save(mygo.SaveDialogOptions{Title: locales.Text("导出完整数据库图"), DefaultPath: object.Catalog + ".graph." + strings.ToLower(format), Filters: []mygo.FileFilter{{Name: format, Extensions: []string{strings.ToLower(format)}}}})
		}
		if pickerDone != nil {
			close(pickerDone)
			services.Invalidate()
		}
		if err == nil && path != "" {
			bundle, readErr := export(ctx, object)
			err = readErr
			if err == nil {
				err = mysqlDataAtomicFile(ctx, path, func(w io.Writer) error { return graphimport.Write(w, bundle, format) })
			}
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		out <- mysqlDataTransferResult{GraphExchange: true, Path: path, Request: number, Generation: generation, Profile: object.ProfileID, Err: err}
		services.Invalidate()
	}()
}
