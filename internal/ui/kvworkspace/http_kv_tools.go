package kvworkspace

import (
	"context"
	"encoding/json"
	"github.com/nyakang/nyadb/internal/db/consulspec"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
	"io"
	"reflect"
	"sync/atomic"
)

type httpKVToolsState struct {
	searchPage, previewPage                                         int
	searchBrowser, previewBrowser                                   privateJSONBrowser
	exportOriginal                                                  *contract.KVResult
	open, keys, values, caseSensitive, prefixMode, checked, consent bool
	prefix, query, limit, maxScan, policy                           string
	search                                                          *consulspec.SearchResult
	bundle                                                          *consulspec.Bundle
	preview                                                         *consulspec.ImportPreview
	observations                                                    []contract.KVResult
	batches                                                         []Receipt
	batch                                                           int
	plan                                                            contract.KVPreparation
	planLease                                                       contract.KVRequestLease
	planRequest                                                     contract.KVRequest
	planScope                                                       contract.KVScope
	progress                                                        *atomic.Int64
	revision                                                        uint64
}
type httpKVToolsOutcome struct {
	search       *consulspec.SearchResult
	bundle       *consulspec.Bundle
	preview      *consulspec.ImportPreview
	observations []contract.KVResult
	batch        *contract.KVResult
	batchIndex   int
	request      contract.KVRequest
}

func (s *State) OpenHTTPKVTools(d *Document) {
	if !s.active(d) || d.scope.HTTP.Protocol != "consul-http" || d.closed {
		return
	}
	if d.httpKVTools == nil {
		d.httpKVTools = &httpKVToolsState{prefix: string(d.scope.Prefix), prefixMode: true, keys: true, values: true, limit: "100", maxScan: "10000", policy: "abort"}
	}
	d.httpKVTools.open = true
}
func (s *State) RunHTTPKVSearch(d *Document, export bool) {
	if !s.check(d) || d.FileBusy() || d.httpKVTools == nil {
		return
	}
	x := d.httpKVTools
	if export && x.preview != nil && x.batch < len(x.preview.Requests) {
		return
	}
	search, err := httpKVSearchInput(x)
	if !export && err != nil {
		d.error = locales.Text("请检查 Consul 搜索输入")
		return
	}
	request, err := consulspec.ReadRequest(x.prefix, x.prefixMode || !export)
	if err != nil {
		return
	}
	p, err := d.lease.Prepare(request)
	if err != nil || p == nil || p.Writes() {
		d.error = locales.Text("原 KV 读取准备失败")
		return
	}
	scope, lease, key, prefix := d.scope.Clone(), d.lease, x.prefix, x.prefixMode
	progress := new(atomic.Int64)
	x.progress = progress
	x.plan = nil
	x.checked = false
	s.start(d, "http-kv-read", request, func(ctx context.Context) outcome {
		r, err := lease.Execute(ctx, p)
		v := r.Clone()
		o := &httpKVToolsOutcome{observations: []contract.KVResult{v}}
		if export {
			if err == nil {
				b, e := consulspec.ExportBundle(scope, key, prefix, v)
				err = e
				o.bundle = &b
			}
		} else {
			found, e := consulspec.Search(search, v, func(n int) error { progress.Store(int64(n)); return ctx.Err() })
			o.search = &found
			if err == nil {
				err = e
			}
		}
		return outcome{httpKV: o, err: err}
	})
}
func (s *State) OpenHTTPKVBundle(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpKVTools == nil || len(d.httpKVTools.batches) > 0 {
		return
	}
	f := s.deps.Files
	if f.OpenPath == nil || f.OpenReader == nil {
		return
	}
	s.fileJob(d, "http-kv-bundle-open", func(ctx context.Context) fileResult {
		path, err := f.OpenPath()
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		if err := ctx.Err(); err != nil {
			return fileResult{err: err}
		}
		reader, err := f.OpenReader(path)
		if err != nil {
			return fileResult{err: err}
		}
		raw, err := io.ReadAll(io.LimitReader(reader, consulspec.BundleLimit+1))
		closeErr := reader.Close()
		if err != nil || closeErr != nil || len(raw) > consulspec.BundleLimit || ctx.Err() != nil {
			return fileResult{err: contract.ErrUnsupported}
		}
		b, err := consulspec.DecodeBundle(raw)
		if err != nil {
			return fileResult{err: err}
		}
		return fileResult{httpBundle: &b, path: path}
	})
}
func (s *State) SaveHTTPKVBundle(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpKVTools == nil {
		return
	}
	x := d.httpKVTools
	f := s.deps.Files
	if x.bundle == nil || !x.consent || f.SavePath == nil || f.WriteStream == nil {
		return
	}
	x.consent = false
	b := x.bundle.Clone()
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil || len(raw) > consulspec.BundleLimit {
		return
	}
	s.fileJob(d, "http-kv-bundle-save", func(ctx context.Context) fileResult {
		path, err := f.SavePath("consul-kv-bundle.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{err: writeCaptured(ctx, f, path, raw)}
	})
}
func (s *State) PreviewHTTPKVImport(d *Document) {
	if !s.check(d) || d.FileBusy() || d.httpKVTools == nil || d.hasUnknownWrites() {
		return
	}
	x := d.httpKVTools
	if x.bundle == nil || len(x.batches) > 0 {
		return
	}
	b, policy := x.bundle.Clone(), x.policy
	lease := d.lease
	progress := new(atomic.Int64)
	x.progress = progress
	if consulspec.ValidateBundle(b) != nil {
		return
	}
	s.start(d, "http-kv-preview", contract.KVRequest{}, func(ctx context.Context) outcome {
		o := &httpKVToolsOutcome{}
		cost := 0
		for i, e := range b.Entries {
			if err := ctx.Err(); err != nil {
				return outcome{httpKV: o, err: err}
			}
			request, err := consulspec.ReadRequest(e.Key, false)
			if err != nil {
				return outcome{httpKV: o, err: err}
			}
			p, err := lease.Prepare(request)
			if err != nil {
				return outcome{httpKV: o, err: err}
			}
			r, err := lease.Execute(ctx, p)
			cost += httpKVReceiptBytes(r)
			o.observations = append(o.observations, r.Clone())
			progress.Store(int64(i + 1))
			if err != nil || !consulspec.CompleteRead(r) || cost > 64<<20 {
				return outcome{httpKV: o, err: contract.ErrUnsupported}
			}
		}
		preview, err := consulspec.PlanImport(b, policy, o.observations)
		o.preview = &preview
		return outcome{httpKV: o, err: err}
	})
}
func (s *State) PrepareHTTPKVImportBatch(d *Document) {
	if !s.check(d) || d.FileBusy() || d.hasUnknownWrites() || d.httpKVTools == nil {
		return
	}
	x := d.httpKVTools
	p := x.preview
	if p == nil || !p.CanApply || p.Policy != x.policy || x.bundle == nil || !reflect.DeepEqual(p.Bundle, *x.bundle) || x.batch >= len(p.Requests) {
		return
	}
	if len(x.batches) > 0 {
		last := x.batches[len(x.batches)-1]
		if last.Error != "" || last.Result.HTTP == nil || last.Result.HTTP.Txn == nil || !last.Result.HTTP.Txn.Committed {
			return
		}
	}
	request := p.Requests[x.batch]
	plan, err := d.lease.Prepare(request)
	if err != nil || plan == nil || !plan.Writes() {
		d.error = locales.Text("原 Bundle 事务准备失败")
		return
	}
	x.plan, x.planLease, x.planRequest, x.planScope = plan, d.lease, request, d.scope.Clone()
	x.checked = false
}
func (s *State) ConfirmHTTPKVImportBatch(d *Document) {
	if !s.check(d) || d.FileBusy() || d.hasUnknownWrites() || d.httpKVTools == nil {
		return
	}
	x := d.httpKVTools
	p := x.plan
	if x.preview == nil || x.bundle == nil || x.preview.Policy != x.policy || !reflect.DeepEqual(x.preview.Bundle, *x.bundle) {
		x.plan = nil
		x.checked = false
		return
	}
	if !x.checked || p == nil || x.planLease != d.lease || !sameEventScope(x.planScope, d.scope) || p.Snapshot() != x.planRequest || x.preview == nil || x.batch >= len(x.preview.Requests) || x.preview.Requests[x.batch] != x.planRequest {
		return
	}
	lease, request, index := d.lease, x.planRequest, x.batch
	x.plan = nil
	x.checked = false
	s.start(d, "http-kv-import", request, func(ctx context.Context) outcome {
		r, err := lease.Execute(ctx, p)
		v := r.Clone()
		return outcome{httpKV: &httpKVToolsOutcome{batch: &v, batchIndex: index, request: request}, err: err}
	})
}
func (s *State) consumeHTTPKVTools(d *Document, job *operation, out outcome) {
	if d.httpKVTools == nil {
		d.httpKVTools = &httpKVToolsState{}
	}
	x := d.httpKVTools
	o := out.httpKV
	if o == nil {
		return
	}
	x.plan = nil
	x.checked = false
	x.consent = false
	if out.err != nil {
		d.error = locales.Text("Consul KV 工具未完成；原响应保留")
	}
	if job.kind == "http-kv-read" && len(o.observations) == 1 && o.search == nil {
		v := o.observations[0].Clone()
		x.exportOriginal = &v
	}
	if o.search != nil {
		v := o.search.Clone()
		x.search = &v
		x.searchPage = 0
		x.searchBrowser = privateJSONBrowser{selected: -1}
	}
	if o.bundle != nil && out.err == nil {
		v := o.bundle.Clone()
		x.bundle = &v
		x.preview = nil
	}
	if job.kind == "http-kv-preview" {
		x.observations = o.observations
		x.preview = nil
		if o.preview != nil && out.err == nil {
			v := o.preview.Clone()
			x.preview = &v
			x.previewPage = 0
			x.previewBrowser = privateJSONBrowser{selected: -1}
			x.batch = 0
		}
	}
	if o.batch != nil {
		if o.batchIndex != x.batch || o.request != job.authored || o.batch.Request != job.authored {
			d.error = contract.ErrRevoked.Error()
			return
		}
		r := Receipt{Scope: job.scope.Clone(), Authored: job.authored, Result: o.batch.Clone()}
		if out.err != nil {
			r.Error = locales.Text("原 Bundle 事务未完成")
		}
		x.batches = append(x.batches, r)
		d.last = &r
		if r.Result.WriteOutcomeUnknown {
			d.unknown = &r
		}
		if r.Result.HTTP != nil && r.Result.HTTP.Txn != nil && r.Result.HTTP.Txn.Committed && r.Result.Completed && out.err == nil {
			x.batch++
		}
	}
	x.revision++
	d.permit = nil
}
func (s *State) consumeHTTPKVFile(d *Document, job *fileOperation, r fileResult) bool {
	if job.kind != "http-kv-bundle-open" && job.kind != "http-kv-bundle-save" {
		return false
	}
	if r.err != nil {
		d.error = locales.Text("Consul Bundle 文件操作失败")
		return true
	}
	if job.canceled || d.httpKVTools == nil {
		return true
	}
	x := d.httpKVTools
	if r.httpBundle != nil {
		v := r.httpBundle.Clone()
		x.bundle = &v
		x.preview = nil
		x.plan = nil
		x.checked = false
		x.batch = 0
	}
	x.revision++
	d.permit = nil
	return true
}
func httpKVReceiptBytes(r contract.KVResult) int {
	if r.HTTP == nil {
		return 0
	}
	n := len(r.HTTP.Body) + len(r.Request.JSON)
	for _, e := range consulspec.ReadEntries(r) {
		n += len(e.Value) + len(e.Key)
	}
	return n
}
func (d *Document) httpKVToolsRevision() uint64 {
	if d.httpKVTools == nil {
		return 0
	}
	return d.httpKVTools.revision
}
func (d *Document) httpKVToolsContent() bool {
	return d.httpKVTools != nil && (d.httpKVTools.bundle != nil || d.httpKVTools.search != nil || len(d.httpKVTools.batches) > 0 || d.httpKVTools.preview != nil)
}
