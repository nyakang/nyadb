package kvworkspace

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) httpReceiptView(c *ui.Context, d *Document, r *Receipt) {
	h := r.Result.HTTP
	p := theme.Colors(c)
	status := ReceiptStatus(r.Result) + " · " + r.Result.Code
	if h.Metadata.Index != nil {
		status += " · index " + strconv.FormatUint(*h.Metadata.Index, 10)
	}
	ui.Text(c, status).Bold().SingleLine().Tooltip(h.Protocol + "\n" + h.Kind)
	ui.Text(c, h.Request.Method+" "+h.URL).SingleLine().Tooltip(h.Request.Method + " " + h.URL)
	if h.SessionID != "" {
		ui.Text(c, "Session UUID: "+h.SessionID).SingleLine().Tooltip(h.SessionID)
	}
	if !r.Scope.HTTP.AgentAvailable {
		ui.Text(c, locales.Text("原连接的 Agent 身份不可用")).TextColor(p.Muted)
	}
	if h.CaptureError != "" || h.ProjectionError != "" {
		message := h.CaptureError
		if h.ProjectionError != "" {
			if message != "" {
				message += " · "
			}
			message += h.ProjectionError
		}
		ui.Text(c, message).SingleLine().Tooltip(message).TextColor(p.Warning)
	}
	ui.Toolbar(c, func() {
		if ui.Button(c, locales.Text("HTTP 原正文")).Clicked() {
			d.httpSessionKeys = false
			d.httpHeaders = false
			d.httpSessions = false
		}
		if ui.Button(c, locales.Text("HTTP 头与元数据")).Clicked() {
			d.httpSessionKeys = false
			d.httpHeaders = true
			d.httpSessions = false
		}
		if h.Sessions != nil && r.Scope.Namespace.ProfileID == d.profileID {
			if ui.Button(c, locales.Text("Session 列表")).Disabled(d.Busy()).Clicked() {
				d.httpSessions = true
			}
		}
		if r.HTTPSessionKeys != nil && r.HTTPSessionKeys.ProjectionComplete {
			if ui.Button(c, locales.Text("Session 持有键")).Disabled(d.Busy()).Clicked() {
				d.httpSessionKeys = true
			}
		}
		if ui.Button(c, locales.Text("复制原请求")).Clicked() {
			c.WriteClipboard(r.Result.Request.Method + "\n" + r.Result.Request.JSON)
		}
	}).FillWidth().MinWidth(0).Gap(5)
	text := httpReceiptPreview(h)
	if d.httpHeaders {
		// This bounded preview omits bodies and typed entry arrays. The complete
		// operation record retains every original captured field.
		record := struct {
			Header, Trailer                                                                         map[string][]string
			Index, LastContact                                                                      *string
			KnownLeader, ACLFiltered                                                                *bool
			QueryBackend, DefaultACLPolicy, AgentID, SessionID                                      string
			Attempted, Ended, Captured, EOF, Acknowledged, Succeeded, WriteOutcomeUnknown, Canceled bool
		}{Header: h.Header, Trailer: h.Trailer, Index: uintRecord(h.Metadata.Index), LastContact: uintRecord(h.Metadata.LastContact),
			KnownLeader: h.Metadata.KnownLeader, ACLFiltered: h.Metadata.ACLFiltered, QueryBackend: h.Metadata.QueryBackend, DefaultACLPolicy: h.Metadata.DefaultACLPolicy,
			AgentID: r.Scope.HTTP.AgentID, SessionID: h.SessionID, Attempted: h.Attempted, Ended: h.Ended, Captured: h.Captured, EOF: h.EOF,
			Acknowledged: h.Acknowledged, Succeeded: h.Succeeded, WriteOutcomeUnknown: h.WriteOutcomeUnknown, Canceled: h.Canceled}
		raw, _ := json.MarshalIndent(record, "", "  ")
		text = clipped(string(raw))
	}
	if text == "" {
		text = r.Error
	}
	if len(text) > previewLimit || len(h.Body) > previewLimit {
		ui.Text(c, locales.Text("预览最多 64 KiB；复制与导出保留完整内容")).FontSize(theme.Font(c, 11)).TextColor(p.Muted)
	}
	area := ui.TextArea(c, &text).Label(locales.Text("KV 原生回执")).ReadOnly(true).Fill().MinHeight(0)
	theme.BindNativeEdit(c, area, fmt.Sprintf("kv/receipt/%d", d.id), s.deps.EditFocus, s.deps.EditTarget, func() bool { return s.active(d) }, false, false)
}

// Summaries describe the already prepared snapshot and carry no authority.
// Capture once at preparation, so large base64 bodies are not parsed per frame.
func requestConfirmationSummary(request contract.KVRequest) string {
	if request.Method != contract.KVConsulHTTPRequestMethod {
		return request.Method
	}
	var summary struct{ Method, Path string }
	if json.Unmarshal([]byte(request.JSON), &summary) != nil || summary.Method == "" || summary.Path == "" {
		return "HTTP"
	}
	return summary.Method + " " + summary.Path
}
