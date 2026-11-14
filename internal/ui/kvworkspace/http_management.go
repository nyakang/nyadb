package kvworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type httpManagementDraft struct{ Operation, Kind, Identity, Options, Body string }
type httpManagementState struct {
	capabilities                           []contract.KVHTTPManagementResult
	browser                                privateJSONBrowser
	deletionImpact                         *contract.KVHTTPEnterpriseImpact
	drafts                                 map[string]httpManagementDraft
	savedDrafts                            map[string]httpManagementDraft
	receiptSelection                       string
	filter                                 string
	page                                   int
	selectedRow                            int
	open, checked, revealed, exportConsent bool
	draft                                  httpManagementDraft
	saved                                  httpManagementDraft
	revision                               uint64
	plan                                   contract.KVHTTPManagementPreparation
	lease                                  contract.KVRequestLease
	editorRevision                         uint64
	last, unknown                          *contract.KVHTTPManagementResult
}

func (d *Document) HTTPManagementAvailable() bool {
	p, ok := d.lease.(contract.KVHTTPManaging)
	return !d.closed && ok && len(p.HTTPManagementOperations()) > 0
}
func (d *Document) managementContent() bool {
	return d.httpManagement != nil && (d.httpManagement.last != nil || len(d.httpManagement.capabilities) > 0 || !reflect.DeepEqual(d.httpManagement.drafts, d.httpManagement.savedDrafts) || d.httpManagement.draft != d.httpManagement.saved)
}
func (d *Document) managementRevision() uint64 {
	if d.httpManagement == nil {
		return 0
	}
	return d.httpManagement.revision
}
func (s *State) OpenHTTPManagement(d *Document) {
	if !s.active(d) || !d.HTTPManagementAvailable() {
		return
	}
	if d.httpManagement == nil {
		d.httpManagement = &httpManagementState{draft: httpManagementDraft{Operation: "agent-members", Options: "{}"}}
		d.httpManagement.saved = d.httpManagement.draft
	}
	d.httpManagement.ensureDrafts()

	d.httpManagement.open = true
}
func (s *State) managementInput(d *Document) (contract.KVHTTPManagementRequest, error) {
	return parseManagementInput(d.httpManagement.draft, d.lease.(contract.KVHTTPManaging).HTTPManagementOperations())
}
func parseManagementInput(draft httpManagementDraft, operations []contract.KVHTTPManagementOperation) (contract.KVHTTPManagementRequest, error) {
	x := struct{ draft httpManagementDraft }{draft}
	in := contract.KVHTTPManagementRequest{Operation: x.draft.Operation, Kind: x.draft.Kind, Identity: x.draft.Identity}
	if x.draft.Options != "" {
		if jsondocument.Decode([]byte(x.draft.Options), &in.Options, 64<<10) != nil || !validManagementOptions(x.draft.Options) {
			return in, contract.ErrUnsupported
		}
	}
	for _, op := range operations {
		if op.Name == in.Operation {
			if !strings.Contains(op.Path, ":kind") && in.Kind != "" || !strings.Contains(op.Path, ":id") && in.Identity != "" || op.BodyKind == "" && x.draft.Body != "" || len(op.Options) == 0 && len(in.Options) != 0 {
				return in, contract.ErrUnsupported
			}
			if op.BodyKind == "bytes" {
				b, e := base64.StdEncoding.Strict().DecodeString(x.draft.Body)
				if e != nil || base64.StdEncoding.EncodeToString(b) != x.draft.Body {
					return in, contract.ErrUnsupported
				}
				in.Body = b
			} else if op.BodyKind != "" {
				in.Body = []byte(x.draft.Body)
				var object map[string]json.RawMessage
				if jsondocument.Decode(in.Body, &object, 8<<20) != nil || object == nil {
					return in, contract.ErrUnsupported
				}
			}
			if len(in.Body) > 8<<20 {
				return in, contract.ErrUnsupported
			}
			return in, nil
		}
	}
	return in, contract.ErrUnsupported
}
func (s *State) PrepareHTTPManagement(d *Document) {
	if !s.check(d) || d.FileBusy() || !d.HTTPManagementAvailable() || d.httpManagement == nil {
		return
	}
	x := d.httpManagement
	d.clearHTTPManagementPlan()
	x.checked = false
	x.revealed = false
	in, err := s.managementInput(d)
	if err != nil {
		d.error = locales.Text("请检查 Consul 管理输入")
		return
	}
	if in.Operation == "namespace-delete" || in.Operation == "partition-delete" {
		s.prepareHTTPEnterpriseDeletion(d, in)
		return
	}
	p, err := d.lease.(contract.KVHTTPManaging).PrepareHTTPManagement(in.Clone())
	if err != nil || p == nil || !reflect.DeepEqual(p.Request(), in) || !sameEventScope(p.Scope(), d.scope) {
		d.error = locales.Text("请检查 Consul 管理输入和写入开关")
		return
	}
	if p.Writes() {
		if d.hasUnknownWrites() {
			d.error = contract.ErrKVWriteUnknown.Error()
			return
		}
		x.plan = p
		x.lease = d.lease
		x.editorRevision = d.Editor.Buffer.Version()
		return
	}
	s.executeHTTPManagement(d, p)
}
func (s *State) ConfirmHTTPManagement(d *Document, checked bool) {
	if !s.check(d) || d.FileBusy() || d.httpManagement == nil || !checked {
		return
	}
	x := d.httpManagement
	p := x.plan
	x.plan = nil
	in, err := s.managementInput(d)
	if p == nil || err != nil || x.lease != d.lease || x.editorRevision != d.Editor.Buffer.Version() || !reflect.DeepEqual(in, p.Request()) || !sameEventScope(p.Scope(), d.scope) || d.hasUnknownWrites() {
		if closer, ok := p.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		return
	}
	s.executeHTTPManagement(d, p)
}
func (s *State) executeHTTPManagement(d *Document, p contract.KVHTTPManagementPreparation) {
	facet := d.lease.(contract.KVHTTPManaging)
	input, record := p.Request(), p.LocalRecordID()
	s.start(d, "http-management", contract.KVRequest{}, func(ctx context.Context) outcome {
		r, err := facet.ExecuteHTTPManagement(ctx, p)
		r = r.Clone()
		return outcome{httpManagement: &r, err: err}
	})
	d.job.httpManagementRequest = input
	d.job.httpManagementRecord = record
}
func (s *State) consumeHTTPManagement(d *Document, job *operation, out outcome) {
	if d.httpManagement == nil {
		d.httpManagement = &httpManagementState{}
	}
	x := d.httpManagement
	x.ensureDrafts()
	x.revealed = false
	x.exportConsent = false
	d.clearHTTPManagementPlan()
	if out.err != nil {
		d.error = locales.Text("Consul 管理请求失败；原私有回执保留")
	}
	if out.httpManagement == nil || !out.httpManagement.Receipt.Attempted && out.httpManagement.EnterpriseImpact == nil {
		return
	}
	r := out.httpManagement.Clone()
	if r.LocalRecordID != job.httpManagementRecord || !reflect.DeepEqual(r.Request, job.httpManagementRequest) || !sameEventScope(r.Scope, job.scope) {
		d.error = contract.ErrRevoked.Error()
		return
	}
	x.last = &r
	if r.Receipt.WriteOutcomeUnknown {
		x.unknown = &r
	}
	x.revision++
	d.permit = nil
}
func (s *State) managementFile(d *Document, kind string) {
	if !s.active(d) || d.Busy() || d.FileBusy() || d.httpManagement == nil {
		return
	}
	x := d.httpManagement
	f := s.deps.Files
	draft := x.draft
	if kind == "management-open" {
		if f.OpenPath == nil || f.OpenReader == nil {
			return
		}
		s.fileJob(d, kind, func(ctx context.Context) fileResult {
			path, err := f.OpenPath()
			if err != nil || path == "" {
				return fileResult{err: err}
			}
			if ctx.Err() != nil {
				return fileResult{err: ctx.Err()}
			}
			r, err := f.OpenReader(path)
			if err != nil {
				return fileResult{err: err}
			}
			defer r.Close()
			b, err := io.ReadAll(io.LimitReader(r, inputLimit+1))
			if err != nil || len(b) > inputLimit {
				return fileResult{err: contract.ErrUnsupported}
			}
			var envelope struct {
				Version int
				Kind    string
				Input   httpManagementDraft
			}
			if jsondocument.Decode(b, &envelope, inputLimit) != nil || envelope.Version != 1 || envelope.Kind != "consul-management-input" || !validManagementEnvelope(b) {
				return fileResult{err: contract.ErrUnsupported}
			}
			return fileResult{managementDraft: &envelope.Input, err: ctx.Err()}
		})
		return
	}
	if f.SavePath == nil || f.WriteStream == nil || !x.exportConsent {
		return
	}
	x.exportConsent = false
	var raw []byte
	var err error
	if kind == "management-save" {
		if _, e := s.managementInput(d); e != nil {
			return
		}
		raw, err = json.MarshalIndent(struct {
			Version int
			Kind    string
			Input   httpManagementDraft
		}{1, "consul-management-input", draft}, "", "  ")
	} else if kind == "management-capabilities-export" {
		raw, err = json.MarshalIndent(x.capabilities, "", "  ")
	} else if kind == "management-detail-export" {
		raw = x.browser.selectedRaw()
	} else if x.last != nil {
		r := x.last
		if x.receiptSelection == "unknown" && x.unknown != nil {
			r = x.unknown
		}
		raw, err = json.MarshalIndent(r.Clone(), "", "  ")
	} else {
		return
	}
	if err != nil || len(raw) > 256<<20 || kind == "management-save" && len(raw) > inputLimit || kind == "management-detail-export" && len(raw) == 0 {
		return
	}
	s.fileJob(d, kind, func(ctx context.Context) fileResult {
		path, err := f.SavePath("consul-management.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{err: writeCaptured(ctx, f, path, bytes.Clone(raw)), managementDraft: &draft}
	})
}
func (s *State) consumeManagementFile(d *Document, job *fileOperation, r fileResult) bool {
	if !strings.HasPrefix(job.kind, "management-") {
		return false
	}
	if r.err != nil {
		d.error = locales.Text("Consul 私有文件操作失败")
		return true
	}
	if job.canceled || d.httpManagement == nil {
		return true
	}
	if r.managementDraft != nil && job.kind == "management-open" {
		d.httpManagement.draft = *r.managementDraft
		d.httpManagement.saved = *r.managementDraft
		d.httpManagement.drafts[r.managementDraft.Operation] = *r.managementDraft
		d.httpManagement.savedDrafts[r.managementDraft.Operation] = *r.managementDraft
		d.clearHTTPManagementPlan()
		d.httpManagement.revision++
	}
	if r.managementDraft != nil && job.kind == "management-save" {
		d.httpManagement.saved = *r.managementDraft
		d.httpManagement.savedDrafts[r.managementDraft.Operation] = *r.managementDraft
	}
	return true
}
func (s *State) httpManagementModals(c *ui.Context) {
	for _, d := range s.documents {
		if !s.active(d) || d.httpManagement == nil || !d.httpManagement.open || s.leave != nil {
			continue
		}
		x := d.httpManagement
		x.ensureDrafts()
		width, height := c.Size()
		theme.Modal(c.Key(fmt.Sprintf("consul-management-%d", d.id)), &x.open, func() {
			ui.Column(c).Width(min(float32(680), max(float32(1), width-68))).Height(min(float32(520), max(float32(1), height-68))).Gap(6).Children(func() {
				ui.Text(c, locales.Text("Consul 管理工具")).Bold()
				ui.Text(c, fmt.Sprintf(locales.Text("连接 #%d · 原会话代次 %d"), d.profileID, d.scope.Generation)+" · Consul "+d.scope.Version)
				ui.Text(c, fmt.Sprintf("DC %s · Namespace %s · Partition %s", d.scope.HTTP.Datacenter, d.scope.HTTP.Namespace, d.scope.HTTP.Partition)).Wrap()
				if s.deps.EditProfile != nil && ui.Button(c, locales.Text("更改 Consul 作用域…")).Disabled(d.Busy() || d.FileBusy()).Clicked() {
					x.open = false
					s.deps.EditProfile(c.Services(), d.profileID)
				}
				ui.Text(c, locales.Text("作用域保存后断开此连接；原文档与回执保留，新作用域需新建请求文档")).Wrap()
				busy := d.Busy() || d.FileBusy()
				before := x.draft
				var choices []string
				var selected contract.KVHTTPManagementOperation
				if facet, ok := d.lease.(contract.KVHTTPManaging); ok {
					for _, op := range facet.HTTPManagementOperations() {
						choices = append(choices, op.Name)
						if op.Name == x.draft.Operation {
							selected = op
						}
					}
				}
				ui.Scroll(c).Fill().MinHeight(0).Children(func() {
					ui.Column(c).FillWidth().Gap(5).Children(func() {
						ui.Select(c, &x.draft.Operation, choices).Label(locales.Text("管理操作")).FillWidth().Disabled(busy)
						ui.Text(c, managementTitle(selected)+" · "+selected.Method+" "+selected.Path).Wrap().FillWidth()
						if selected.Guard != "" {
							ui.Text(c, locales.Text("此操作使用集群或原 Agent 范围；KV 前缀、Namespace 和 Partition 不限定其影响")).Wrap().TextColor(theme.Colors(c).Warning)
						}
						if strings.Contains(selected.Path, ":kind") {
							ui.TextInput(c, &x.draft.Kind).Label("Kind").Placeholder("service-defaults").FillWidth().Disabled(busy)
						}
						if strings.Contains(selected.Path, ":id") {
							ui.TextInput(c, &x.draft.Identity).Label(locales.Text("原标识或名称")).Placeholder(locales.Text("原标识或名称")).FillWidth().Disabled(busy)
						}
						if len(selected.Options) > 0 {
							ui.Text(c, locales.Text("原生参数 JSON")+" · "+strings.Join(selected.Options, ", ")).Wrap().FillWidth()
							ui.TextArea(c, &x.draft.Options).Label(locales.Text("原生参数 JSON")).Height(65).FillWidth().ReadOnly(busy)
						}
						if selected.BodyKind != "" {
							label := locales.Text("完整原生定义 JSON")
							if selected.BodyKind == "bytes" {
								label = locales.Text("完整原值 Base64")
							}
							ui.Text(c, label)
							ui.TextArea(c, &x.draft.Body).Label(label).Height(140).FillWidth().ReadOnly(busy)
						}
						if i := x.deletionImpact; i != nil {
							ui.Text(c, fmt.Sprintf(locales.Text("删除影响完整 %t · ACL 过滤 %t · 未读取 %d 项"), i.Complete, i.ACLFiltered, len(i.Missing))).Wrap()
							for _, o := range i.Observations {
								ui.Text(c, fmt.Sprintf("%s · %s/%s · %d · complete %t", o.Resource, o.Partition, o.Namespace, o.Count, o.Complete)).Wrap()
							}
							ui.Text(c, locales.Text("执行前重新读取并核对影响；原生 DELETE 与盘点之间没有原子条件")).Wrap().TextColor(theme.Colors(c).Warning)
						}
						if x.plan != nil {
							r := x.plan.HTTP()
							ui.Text(c, r.Method+" "+r.Path).Wrap()
							reviewed, _ := json.MarshalIndent(x.plan.Request(), "", "  ")
							full := string(reviewed)
							ui.TextArea(c, &full).Label(locales.Text("完整原生定义 JSON")).Height(150).FillWidth().ReadOnly(true)
							ui.Checkbox(c, &x.checked, locales.Text("确认此原连接的完整写入请求"))
							if ui.Button(c, locales.Text("确认执行管理写入")).Disabled(busy || !x.checked).Clicked() {
								s.ConfirmHTTPManagement(d, x.checked)
							}
						}
						if x.last != nil {
							status := 0
							if x.last.Receipt.HTTP != nil {
								status = x.last.Receipt.HTTP.Status
							}
							ui.Text(c, fmt.Sprintf("HTTP %d · ACK %t · unknown %t", status, x.last.Receipt.Completed, x.last.Receipt.WriteOutcomeUnknown))
							ui.Checkbox(c, &x.revealed, locales.Text("显示原私有回执"))
							if x.revealed {
								selectedReceipt := x.last
								ui.Select(c, &x.receiptSelection, []string{"last", "unknown"}).Label(locales.Text("原私有回执"))
								if x.receiptSelection == "unknown" && x.unknown != nil {
									selectedReceipt = x.unknown
								}
								if selectedReceipt.Receipt.HTTP != nil {
									x.browser.set(selectedReceipt.LocalRecordID, selectedReceipt.Receipt.HTTP.Body)
									s.privateJSONView(c, d, &x.browser, "management")
								}
							}
						}
						for _, probe := range x.capabilities {
							ui.Text(c, probe.Request.Operation+" · "+locales.Text(capabilityCaption(probe))).Wrap()
						}
						if len(x.capabilities) > 0 {
							ui.Text(c, locales.Text("能力探测仅表示当前连接和权限下的响应；未知或禁止不能证明功能不存在")).Wrap()
							if ui.Button(c, locales.Text("导出能力探测回执")).Disabled(busy || !x.exportConsent).Clicked() {
								s.managementFile(d, "management-capabilities-export")
							}
						}
						if x.unknown != nil {
							ui.Text(c, locales.Text("原写入结果未知；后续读取不会确认该写入")).Wrap().FillWidth().TextColor(theme.Colors(c).Warning)
						}
						ui.Checkbox(c, &x.exportConsent, locales.Text("允许将此完整私有输入或回执导出到文件"))
					})
				})
				if before != x.draft {
					d.clearHTTPManagementPlan()
					x.checked = false
					x.revision++
					d.permit = nil
					if before.Operation != x.draft.Operation {
						x.drafts[before.Operation] = before
						operation := x.draft.Operation
						if prior, ok := x.drafts[operation]; ok {
							x.draft = prior
						} else {
							x.draft = httpManagementDraft{Operation: operation, Options: "{}"}
							x.savedDrafts[operation] = x.draft
						}
						x.saved = x.savedDrafts[operation]
					}
				}
				x.drafts[x.draft.Operation] = x.draft
				if d.error != "" {
					ui.Text(c, clipped(d.error)).SingleLine().Tooltip(clipped(d.error)).TextColor(theme.Colors(c).Warning)
				}
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("探测 Consul 能力")).Disabled(busy || d.NeedsRecovery()).Clicked() {
						s.RunHTTPCapabilities(d)
					}
					if ui.Button(c, locales.Text("运行或预览管理请求")).Disabled(busy || d.NeedsRecovery()).Clicked() {
						s.PrepareHTTPManagement(d)
					}
				}).FillWidth().Gap(5)
				ui.Toolbar(c, func() {
					if ui.Button(c, locales.Text("打开私有输入")).Disabled(busy).Clicked() {
						s.managementFile(d, "management-open")
					}
					if ui.Button(c, locales.Text("保存私有输入")).Disabled(busy || !x.exportConsent).Clicked() {
						s.managementFile(d, "management-save")
					}
					if ui.Button(c, locales.Text("导出原私有回执")).Disabled(busy || x.last == nil || !x.exportConsent).Clicked() {
						s.managementFile(d, "management-export")
					}
					if ui.Button(c, locales.Text("取消")).Disabled(!d.Busy()).Clicked() {
						s.RequestCancel(d)
					}
				}).FillWidth().Gap(5)
			})
		})
	}
}

func (s *State) consumeHTTPManagementRecovery(d *Document, job *operation, out *outcome) {
	if out.err != nil {
		return
	}
	if d.httpManagement == nil && (out.recoveryManagement != nil || out.recoveryManagementUnknown != nil) {
		d.httpManagement = &httpManagementState{}
	}
	x := d.httpManagement
	if x == nil {
		return
	}
	for _, pair := range []struct {
		expected, actual *contract.KVHTTPManagementResult
	}{{job.managementRecovery, out.recoveryManagement}, {job.managementRecoveryUnknown, out.recoveryManagementUnknown}} {
		if pair.expected != nil && (pair.actual == nil || !reflect.DeepEqual(pair.expected, pair.actual)) {
			if out.lease != nil {
				out.lease.Close()
				out.lease = nil
			}
			out.err = contract.ErrRevoked
			return
		}
	}
	if x.last == nil && out.recoveryManagement != nil {
		v := out.recoveryManagement.Clone()
		x.last = &v
	}
	if x.unknown == nil && out.recoveryManagementUnknown != nil {
		v := out.recoveryManagementUnknown.Clone()
		x.unknown = &v
	}
	d.clearHTTPManagementPlan()
	x.revealed = false
	x.exportConsent = false
}
func (s *State) closeHTTPManagement(d *Document) {
	if d.httpManagement != nil {
		d.clearHTTPManagementPlan()
		d.httpManagement.draft = httpManagementDraft{}
		d.httpManagement.last = nil
		d.httpManagement.unknown = nil
		d.httpManagement = nil
	}
}

func validManagementOptions(raw string) bool {
	var f map[string][]json.RawMessage
	if jsondocument.Decode([]byte(raw), &f, 64<<10) != nil || f == nil {
		return false
	}
	for _, vs := range f {
		if len(vs) != 1 {
			return false
		}
		v := bytes.TrimSpace(vs[0])
		if len(v) == 0 || v[0] != '"' {
			return false
		}
	}
	return true
}
func validManagementEnvelope(raw []byte) bool {
	var e map[string]json.RawMessage
	if jsondocument.Decode(raw, &e, inputLimit) != nil || len(e) != 3 {
		return false
	}
	for _, k := range []string{"Version", "Kind", "Input"} {
		if len(e[k]) == 0 || bytes.Equal(bytes.TrimSpace(e[k]), []byte("null")) {
			return false
		}
	}
	var f map[string]json.RawMessage
	if json.Unmarshal(e["Input"], &f) != nil || len(f) != 5 {
		return false
	}
	for _, k := range []string{"Operation", "Kind", "Identity", "Options", "Body"} {
		v := bytes.TrimSpace(f[k])
		if len(v) == 0 || v[0] != '"' {
			return false
		}
	}
	return true
}
func managementRecoveryExpected(d *Document) (last, unknown *contract.KVHTTPManagementResult) {
	x := d.httpManagement
	if x == nil {
		return
	}
	if x.last != nil {
		v := x.last.Clone()
		last = &v
	}
	// Capability probes use the same executor and can be newer than the user's
	// displayed management receipt. Capture that executor's original record for
	// recovery validation without replacing the displayed receipt or replaying.
	if facet, ok := d.lease.(contract.KVHTTPManaging); ok {
		v := facet.LastHTTPManagement().Clone()
		if v.LocalRecordID != "" {
			last = &v
		}
	}
	if x.unknown != nil {
		v := x.unknown.Clone()
		unknown = &v
	}
	return
}
