package kvworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

const AuthManagementMethod = "/nyadb.kv/AuthManagement"

var authMethods = []Method{
	{"AuthStatus", "/etcdserverpb.Auth/AuthStatus"},
	{"UserList", "/etcdserverpb.Auth/UserList"}, {"UserGet", "/etcdserverpb.Auth/UserGet"},
	{"UserAdd", "/etcdserverpb.Auth/UserAdd"}, {"UserDelete", "/etcdserverpb.Auth/UserDelete"},
	{"UserChangePassword", "/etcdserverpb.Auth/UserChangePassword"},
	{"UserGrantRole", "/etcdserverpb.Auth/UserGrantRole"}, {"UserRevokeRole", "/etcdserverpb.Auth/UserRevokeRole"},
	{"RoleList", "/etcdserverpb.Auth/RoleList"}, {"RoleGet", "/etcdserverpb.Auth/RoleGet"},
	{"RoleAdd", "/etcdserverpb.Auth/RoleAdd"}, {"RoleDelete", "/etcdserverpb.Auth/RoleDelete"},
	{"RoleGrantPermission", "/etcdserverpb.Auth/RoleGrantPermission"}, {"RoleRevokePermission", "/etcdserverpb.Auth/RoleRevokePermission"},
}

func authPath(name string) string {
	for _, method := range authMethods {
		if method.Name == name {
			return method.Path
		}
	}
	return ""
}

func (d *Document) AuthManagementAvailable() bool {
	if d.lease == nil {
		return false
	}
	p, ok := d.lease.(contract.KVAuthManagement)
	if !ok {
		return false
	}
	for _, method := range authMethods {
		if slices.Contains(p.AuthMethods(), method.Path) {
			return true
		}
	}
	return false
}

func (d *Document) authSupports(method string) bool {
	p, ok := d.lease.(contract.KVAuthManagement)
	return ok && slices.Contains(p.AuthMethods(), method)
}

type authBrowser struct {
	split                                        float32
	category, filter, selectedUser, selectedRole string
	target                                       string
	status, users, roles, userDetail, roleDetail *Receipt
	row, detailRow                               int
	columns, detailColumns                       ui.ListState
}

func (d *Document) authState() *authBrowser {
	if d.authBrowser == nil {
		d.authBrowser = &authBrowser{category: "users", row: -1, detailRow: -1, split: 38}
	}
	return d.authBrowser
}

func DefaultAuthManagementRequest() contract.KVRequest {
	return contract.KVRequest{Method: AuthManagementMethod, JSON: "{\n  \"view\": \"users\"\n}"}
}

func authRequestView(request contract.KVRequest) (string, error) {
	var fields map[string]json.RawMessage
	if err := jsondocument.Decode([]byte(request.JSON), &fields, inputLimit); err != nil {
		return "", err
	}
	if fields == nil || len(fields) > 1 {
		return "", jsondocument.ErrInvalid
	}
	view := "users"
	if raw, ok := fields["view"]; ok {
		if string(raw) == "null" || json.Unmarshal(raw, &view) != nil || view != "users" && view != "roles" {
			return "", jsondocument.ErrInvalid
		}
	} else if len(fields) != 0 {
		return "", jsondocument.ErrInvalid
	}
	return view, nil
}

func (s *State) SetAuthCategory(d *Document, category string) {
	if !s.owned(d) || d.Busy() || category != "users" && category != "roles" {
		return
	}
	a := d.authState()
	if a.category == category {
		return
	}
	a.category, a.filter, a.target, a.row, a.detailRow = category, "", "", -1, -1
	d.Editor.Buffer.Set(authJSON(AuthManagementMethod, map[string]string{"view": category}).JSON)
	s.CancelConfirmation(d)
	d.permit = nil
}

func (s *State) RunAuthManagement(d *Document) {
	if !s.check(d) || !d.AuthManagementAvailable() {
		return
	}
	view, err := authRequestView(d.Request())
	if err != nil {
		d.error = locales.Text("KV 请求无效")
		return
	}
	d.authState().category = view
	method := "UserList"
	if view == "roles" {
		method = "RoleList"
	}
	s.ReadAuth(d, method, "")
}

func (s *State) SelectAuthName(d *Document, name string) {
	if !s.owned(d) || d.Busy() {
		return
	}
	a := d.authState()
	r := a.users
	if a.category == "roles" {
		r = a.roles
	}
	if r == nil || !sameEventScope(r.Scope, d.scope) || r.Result.Reply == nil || r.Result.Reply.Auth == nil {
		return
	}
	names := r.Result.Reply.Auth.Users
	if a.category == "roles" {
		names = r.Result.Reply.Auth.Roles
	}
	if !slices.Contains(names, name) {
		return
	}
	if a.category == "roles" {
		a.selectedRole = name
	} else {
		a.selectedUser = name
	}
	a.target, a.detailRow = name, -1
}

func (s *State) ClearAuthBrowser(d *Document) {
	if !s.active(d) || d.Busy() || d.FileBusy() {
		return
	}
	d.authBrowser = nil
	d.permit = nil
}

type authDraft struct {
	method, name, role, password, initialRoles string
	access, rangeKind, encoding, key, end      string
	noPassword, initialPermission, open        bool
	lease                                      contract.KVRequestLease
	scope                                      contract.KVScope
	editor                                     contract.KVRequest
}

type AuthStepReceipt struct {
	Result contract.KVResult
	Error  string
}

type AuthManagementReceipt struct {
	DocumentID uint64
	Scope      contract.KVScope
	Requests   []contract.KVRequestRecord
	Steps      []AuthStepReceipt
}

func (r *AuthManagementReceipt) clone() *AuthManagementReceipt {
	if r == nil {
		return nil
	}
	c := *r
	c.Scope = r.Scope.Clone()
	c.Requests = slices.Clone(r.Requests)
	for i := range c.Requests {
		c.Requests[i] = c.Requests[i].Clone()
	}
	c.Steps = slices.Clone(r.Steps)
	for i := range c.Steps {
		c.Steps[i].Result = c.Steps[i].Result.Clone()
	}
	return &c
}

type authConfirmation struct {
	plans   []contract.KVPreparation
	receipt *AuthManagementReceipt
}

func authJSON(method string, fields any) contract.KVRequest {
	raw, _ := json.Marshal(fields)
	return contract.KVRequest{Method: method, JSON: string(raw)}
}

func (s *State) ReadAuth(d *Document, name, target string) {
	method := authPath(name)
	if !s.check(d) || !d.authSupports(method) {
		return
	}
	fields := map[string]string{}
	switch name {
	case "AuthStatus", "UserList", "RoleList":
	case "UserGet":
		if !validAuthFormName(target) {
			return
		}
		fields["name"] = target
	case "RoleGet":
		if !validAuthFormName(target) {
			return
		}
		fields["role"] = target
	default:
		return
	}
	request := authJSON(method, fields)
	plan, err := d.lease.Prepare(request)
	if err != nil || plan == nil || plan.Writes() || plan.Snapshot().Method != method {
		if err != nil {
			d.error = err.Error()
		}
		return
	}
	lease := d.lease
	s.start(d, "auth-read", request, func(ctx context.Context) outcome {
		r, err := lease.Execute(ctx, plan)
		return outcome{result: r, err: err}
	})
}

func (s *State) consumeAuth(d *Document, job *operation, result outcome) {
	r := &Receipt{Scope: job.scope.Clone(), Authored: job.authored, Result: result.result.Clone()}
	if result.err != nil {
		r.Error = result.err.Error()
	}
	if job.kind == "auth-write" {
		r.AuthManagement = job.auth.clone()
		r.AuthManagement.Steps = result.authSteps
		r.AuthManagement = r.AuthManagement.clone()
		d.last = r
		d.selectedBrowser, d.selectedRenewal, d.selectedEvents = false, false, false
		for _, step := range result.authSteps {
			if step.Result.WriteOutcomeUnknown {
				d.unknown = r
				break
			}
		}
		return
	}
	d.last = r
	if result.err != nil || !r.Result.Completed || r.Result.WriteOutcomeUnknown || r.Result.ProjectionError != "" || r.Result.Reply == nil || r.Result.Reply.Auth == nil || d.needsRecovery || job.lease != d.lease || !sameEventScope(job.scope, d.scope) || d.lease.CheckKVLease() != nil {
		return
	}
	a := d.authState()
	switch r.Result.Request.Method {
	case authPath("AuthStatus"):
		a.status = r
	case authPath("UserList"):
		a.users = r
	case authPath("RoleList"):
		a.roles = r
	case authPath("UserGet"):
		a.userDetail = r
	case authPath("RoleGet"):
		a.roleDetail = r
	}
}

func validAuthFormName(name string) bool {
	return name != "" && len(name) <= 8<<20 && utf8.ValidString(name)
}

func authRoleNames(text string) ([]string, error) {
	if len(text) > 128<<10 {
		return nil, jsondocument.ErrBounds
	}
	var names []string
	for _, name := range strings.Split(text, "\n") {
		if name == "" {
			continue
		}
		if !validAuthFormName(name) || slices.Contains(names, name) || len(names) == 32 {
			return nil, jsondocument.ErrInvalid
		}
		names = append(names, name)
	}
	return names, nil
}

func authFormBytes(text, encoding string) ([]byte, error) {
	if len(text) > 8192 {
		return nil, jsondocument.ErrBounds
	}
	if encoding == "base64" {
		return base64.StdEncoding.Strict().DecodeString(text)
	}
	if encoding != "text" || !utf8.ValidString(text) {
		return nil, jsondocument.ErrInvalid
	}
	return []byte(text), nil
}

func authDraftPermission(p *authDraft) (contract.KVAuthPermission, error) {
	r := contract.KVAuthPermission{Access: p.access}
	if p.access != "READ" && p.access != "WRITE" && p.access != "READWRITE" {
		return r, jsondocument.ErrInvalid
	}
	if p.rangeKind == "all" {
		r.Key, r.End = []byte{0}, []byte{0}
		return r, nil
	}
	var err error
	r.Key, err = authFormBytes(p.key, p.encoding)
	if err != nil || len(r.Key) == 0 || len(r.Key) > 4096 {
		return r, jsondocument.ErrInvalid
	}
	switch p.rangeKind {
	case "exact":
	case "range":
		r.End, err = authFormBytes(p.end, p.encoding)
		if err != nil || len(r.End) == 0 || len(r.End) > 4096 || bytes.Compare(r.Key, r.End) >= 0 {
			return r, jsondocument.ErrInvalid
		}
	case "unbounded":
		r.End = []byte{0}
	case "prefix":
		r.End = slices.Clone(r.Key)
		for i := len(r.End) - 1; i >= 0; i-- {
			if r.End[i] < 255 {
				r.End[i]++
				r.End = r.End[:i+1]
				return r, nil
			}
		}
		r.End = []byte{0}
	default:
		return r, jsondocument.ErrInvalid
	}
	return r, nil
}

func authPermissionFields(p contract.KVAuthPermission) map[string]string {
	f := map[string]string{"key": base64.StdEncoding.EncodeToString(p.Key)}
	if len(p.End) > 0 {
		f["range_end"] = base64.StdEncoding.EncodeToString(p.End)
	}
	return f
}

func authDraftRequests(p *authDraft) ([]contract.KVRequest, error) {
	if !validAuthFormName(p.name) {
		return nil, jsondocument.ErrInvalid
	}
	var fields any
	var extra []contract.KVRequest
	switch p.method {
	case authPath("UserAdd"), authPath("UserChangePassword"):
		if len(p.password) > 1<<20 || !utf8.ValidString(p.password) || p.password == "" && !p.noPassword || p.noPassword && (p.password != "" || p.method != authPath("UserAdd")) {
			return nil, jsondocument.ErrInvalid
		}
		f := map[string]any{"name": p.name}
		if p.noPassword {
			f["options"] = map[string]bool{"no_password": true}
		} else {
			f["password"] = p.password
		}
		fields = f
		if p.method == authPath("UserAdd") {
			roles, err := authRoleNames(p.initialRoles)
			if err != nil {
				return nil, err
			}
			for _, role := range roles {
				extra = append(extra, authJSON(authPath("UserGrantRole"), map[string]string{"user": p.name, "role": role}))
			}
		}
	case authPath("UserDelete"):
		fields = map[string]string{"name": p.name}
	case authPath("RoleAdd"):
		fields = map[string]string{"name": p.name}
		if p.initialPermission {
			permission, err := authDraftPermission(p)
			if err != nil {
				return nil, err
			}
			f := authPermissionFields(permission)
			f["permType"] = permission.Access
			extra = append(extra, authJSON(authPath("RoleGrantPermission"), map[string]any{"name": p.name, "perm": f}))
		}
	case authPath("RoleDelete"):
		fields = map[string]string{"role": p.name}
	case authPath("UserGrantRole"), authPath("UserRevokeRole"):
		if !validAuthFormName(p.role) {
			return nil, jsondocument.ErrInvalid
		}
		f := map[string]string{"role": p.role}
		if p.method == authPath("UserGrantRole") {
			f["user"] = p.name
		} else {
			f["name"] = p.name
		}
		fields = f
	case authPath("RoleGrantPermission"), authPath("RoleRevokePermission"):
		permission, err := authDraftPermission(p)
		if err != nil {
			return nil, err
		}
		f := authPermissionFields(permission)
		if p.method == authPath("RoleGrantPermission") {
			f["permType"] = permission.Access
			fields = map[string]any{"name": p.name, "perm": f}
		} else {
			f["role"] = p.name
			fields = f
		}
	default:
		return nil, jsondocument.ErrInvalid
	}
	return append([]contract.KVRequest{authJSON(p.method, fields)}, extra...), nil
}

func (s *State) OpenAuthAction(d *Document, action string) {
	method := authPath(action)
	if !s.check(d) || !d.authSupports(method) || d.hasUnknownWrites() {
		return
	}
	s.CancelConfirmation(d)
	a := d.authState()
	d.error = ""
	name := a.selectedUser
	cache := a.users
	if strings.HasPrefix(action, "Role") {
		name = a.selectedRole
		cache = a.roles
	}
	if cache == nil || !sameEventScope(cache.Scope, d.scope) {
		name = ""
	}
	if action == "UserAdd" || action == "RoleAdd" {
		name = ""
	}
	d.authDraft = &authDraft{method: method, name: name, access: "READ", rangeKind: "exact", encoding: "text", lease: d.lease, scope: d.scope.Clone(), editor: d.Request(), open: true}
}

func (s *State) OpenSelectedAuthPermission(d *Document, revoke bool) {
	if !s.check(d) || d.hasUnknownWrites() {
		return
	}
	a := d.authState()
	r := a.roleDetail
	if r == nil || !sameEventScope(r.Scope, d.scope) || r.Result.Reply == nil || r.Result.Reply.Auth == nil {
		return
	}
	var f struct {
		Role string `json:"role"`
	}
	if json.Unmarshal([]byte(r.Authored.JSON), &f) != nil || f.Role != a.selectedRole || a.detailRow < 0 || a.detailRow >= len(r.Result.Reply.Auth.Permissions) {
		return
	}
	permission := r.Result.Reply.Auth.Permissions[a.detailRow].Clone()
	if len(permission.NativeExtensions) > 0 {
		d.error = locales.Text("权限包含未知原生字段，请保留原回执并重新输入明确请求")
		return
	}
	action := "RoleGrantPermission"
	if revoke {
		action = "RoleRevokePermission"
	}
	s.OpenAuthAction(d, action)
	if d.authDraft == nil {
		return
	}
	p := d.authDraft
	p.name, p.access, p.encoding = f.Role, permission.Access, "base64"
	p.key, p.end = base64.StdEncoding.EncodeToString(permission.Key), base64.StdEncoding.EncodeToString(permission.End)
	p.rangeKind = "range"
	if len(permission.End) == 0 {
		p.rangeKind = "exact"
	} else if bytes.Equal(permission.End, []byte{0}) {
		p.rangeKind = "unbounded"
	}
}

func (s *State) PrepareAuthAction(d *Document) {
	if !s.check(d) || d.authDraft == nil || d.hasUnknownWrites() {
		return
	}
	p := d.authDraft
	if p.lease != d.lease || p.editor != d.Request() || !sameEventScope(p.scope, d.scope) {
		s.CancelConfirmation(d)
		return
	}
	requests, err := authDraftRequests(p)
	if err != nil {
		d.error = err.Error()
		return
	}
	r := &AuthManagementReceipt{DocumentID: d.id, Scope: d.scope.Clone()}
	var plans []contract.KVPreparation
	for _, request := range requests {
		if !d.authSupports(request.Method) {
			d.error = contract.ErrUnsupported.Error()
			return
		}
		plan, err := p.lease.Prepare(request)
		if err != nil || plan == nil || !plan.Writes() || plan.Snapshot().Method != request.Method {
			if err != nil {
				d.error = err.Error()
			}
			return
		}
		recorder, ok := plan.(contract.KVPreparationRecord)
		if !ok {
			d.error = contract.ErrUnsupported.Error()
			return
		}
		record := recorder.RecordedRequest().Clone()
		if (request.Method == authPath("UserAdd") || request.Method == authPath("UserChangePassword")) && !p.noPassword && !slices.Contains(record.OmittedFields, "password") {
			d.error = contract.ErrUnsupported.Error()
			return
		}
		r.Requests = append(r.Requests, record)
		plans = append(plans, plan)
	}
	p.password = ""
	d.authDraft = nil
	d.confirmation = &confirmation{lease: d.lease, plan: plans[0], authored: r.Requests[0].Request, editor: d.Request(), auth: &authConfirmation{plans: plans, receipt: r}, open: true}
}

func (s *State) executeAuthAction(d *Document, p *confirmation) {
	lease, plans := p.lease, slices.Clone(p.auth.plans)
	r := p.auth.receipt.clone()
	d.confirmation = nil
	d.authDraft = nil
	s.start(d, "auth-write", r.Requests[0].Request, func(ctx context.Context) outcome {
		out := outcome{result: contract.KVResult{Request: r.Requests[0].Request, RequestOmittedFields: slices.Clone(r.Requests[0].OmittedFields)}}
		retained := 0
		for _, plan := range plans {
			// Reserve another complete transport-bounded native reply before
			// starting a step. Earlier confirmed steps are never evicted/replayed.
			if retained > 16<<20 {
				out.err = jsondocument.ErrBounds
				break
			}
			if err := ctx.Err(); err != nil {
				out.err = err
				break
			}
			result, err := lease.Execute(ctx, plan)
			step := AuthStepReceipt{Result: result.Clone()}
			if err != nil {
				step.Error = err.Error()
			}
			out.authSteps = append(out.authSteps, step)
			retained += len(result.Native)
			out.result, out.err = result, err
			if err != nil || !result.Completed || result.WriteOutcomeUnknown {
				break
			}
		}
		return out
	})
	d.job.auth = r
}
