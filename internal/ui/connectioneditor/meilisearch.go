package connectioneditor

import (
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	"strconv"
	"strings"
)

func (s *State) advanceMeilisearchOptions(p *connections.Profile) error {
	values := []*int{&p.MeilisearchOptions.ConnectTimeoutSeconds, &p.MeilisearchOptions.RequestTimeoutSeconds}
	labels := []string{"Meilisearch 连接超时", "Meilisearch 请求超时"}
	for i, target := range values {
		n, err := strconv.Atoi(strings.TrimSpace(s.engineNumbers[i]))
		limit := 120
		if i == 1 {
			limit = 600
		}
		if err != nil || n < 1 || n > limit {
			return fieldError(labels[i], "高级", "请检查 Meilisearch 超时设置")
		}
		*target = n
	}
	return nil
}

func (s *State) setMeilisearchAuth(auth string) {
	if s.connectionDraft.Kind != connections.Meilisearch || auth == s.connectionDraft.MeilisearchOptions.Auth || auth != "none" && auth != "bearer" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.MeilisearchOptions.Auth = auth
	s.meilisearchAuthChoice = meilisearchAuthCaption(auth)
	s.connectionDraft.Username = ""
	s.connectionSecret = ""
	s.connectionSavePassword = false
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func meilisearchAuthCaption(auth string) string {
	if auth == "bearer" {
		return "Bearer API key"
	}
	return "无认证"
}

func (s *State) editorMeilisearchFields(c *ui.Context, labelWidth, fieldWidth float32) {
	o := &s.connectionDraft.MeilisearchOptions
	s.editorInputRow(c, "服务路径：", "Meilisearch 服务路径", &o.ContextPath, labelWidth, fieldWidth)
	s.editorInputRow(c, "索引范围：", "Meilisearch 索引范围", &o.IndexUID, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.meilisearchAuthChoice, []string{"无认证", "Bearer API key"}, "Meilisearch 认证方式", fieldWidth, false)
	})
	if s.meilisearchAuthChoice == "Bearer API key" {
		s.setMeilisearchAuth("bearer")
	} else {
		s.setMeilisearchAuth("none")
	}
	if o.Auth == "bearer" {
		theme.Field(c, locales.Text("API key："), func() { s.formTextInput(c, &s.connectionSecret, "Meilisearch API key", fieldWidth, false, true) })
		ui.Row(c).Height(theme.RowHeight(c, 28)).Gap(0).AlignItems(ui.Center).Children(func() { connectionFormCheckbox(c, &s.connectionSavePassword, "保存凭据", s.credentialVault == nil) })
	}
	ui.Text(c, locales.Text("单索引 API key 请填写索引范围。")).FontSize(theme.Font(c, 12)).TextColor(theme.Colors(c).Muted)
}

func (s *State) meilisearchOptions(c *ui.Context, width float32, tab string) bool {
	if tab != "高级" && tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	o := &s.connectionDraft.MeilisearchOptions
	theme.Form(c, formWidth, func() {
		if tab == "高级" {
			s.editorInputRow(c, "连接超时（秒）", "Meilisearch 连接超时", &s.engineNumbers[0], labelWidth, 120)
			s.editorInputRow(c, "请求超时（秒）", "Meilisearch 请求超时", &s.engineNumbers[1], labelWidth, 120)
			return
		}
		ui.Text(c, locales.Text("Meilisearch 默认端口 7700，服务路径留空表示服务器根路径。"))
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"verify-full", "disable"}, "Meilisearch TLS 模式", fieldWidth, false)
		})
		s.editorInputRow(c, "CA 证书", "Meilisearch CA 证书", &o.CAFile, labelWidth, fieldWidth)
		s.editorInputRow(c, "证书服务器名", "证书服务器名", &o.ServerName, labelWidth, fieldWidth)
		s.editorInputRow(c, "客户端证书", "Meilisearch 客户端证书", &o.ClientCertFile, labelWidth, fieldWidth)
		s.editorInputRow(c, "客户端私钥", "Meilisearch 客户端私钥", &o.ClientKeyFile, labelWidth, fieldWidth)
	})
	return true
}

func (s *State) validateMeilisearchDraft(p connections.Profile) error {
	if _, err := connections.NormalizeMeilisearchOptions(p.MeilisearchOptions); err != nil {
		return fieldError("Meilisearch 服务路径", "常规", "请检查 Meilisearch 服务路径、索引范围和认证设置")
	}
	cfg, err := connections.MeilisearchProfileConfig(p)
	if err != nil {
		return fieldError("Meilisearch TLS 模式", "SSL", "请检查 Meilisearch TLS 和客户端证书设置")
	}
	if connections.ValidateMeilisearchCredential(cfg, s.connectionSecret, false) != nil {
		return fieldError("Meilisearch API key", "常规", "请检查 Meilisearch API key")
	}
	return nil
}
