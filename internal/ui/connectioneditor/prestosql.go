package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

var prestoNumberLabels = []string{"PrestoSQL 连接超时（秒）", "PrestoSQL 查询超时（秒）", "PrestoSQL SQL 会话上限", "PrestoSQL 连接等待（毫秒）"}
var prestoTLSChoices = []struct{ mode, caption string }{{"disable", "禁用 TLS"}, {"verify-full", "验证服务器证书"}, {"verify-ca", "仅验证证书链"}, {"require", "加密但不验证证书"}}

func (s *State) initializePrestoSQLInputs() {
	a := s.connectionDraft.PrestoSQLOptions
	for i, n := range []int{a.ConnectTimeoutSeconds, a.QueryTimeoutSeconds, a.MaxQueryProcesses, a.PoolWaitMillis} {
		s.prestosqlNumbers[i] = strconv.Itoa(n)
	}
	s.prestosqlDriverChoice = a.DriverVersion
	for _, choice := range prestoTLSChoices {
		if choice.mode == s.connectionDraft.TLSMode {
			s.prestosqlTLSChoice = choice.caption
		}
	}
}
func (s *State) prestoCredentials() connections.PrestoSQLCredentials {
	return connections.PrestoSQLCredentials{Password: s.connectionSecret, TrustStorePassword: s.prestosqlSecrets[0], KeyStorePassword: s.prestosqlSecrets[1], AccessToken: s.prestosqlSecrets[2], ExtraCredentials: s.prestosqlSecrets[3]}
}
func (s *State) restorePrestoCredentials(c connections.PrestoSQLCredentials) {
	s.connectionSecret = c.Password
	s.prestosqlSecrets = [4]string{c.TrustStorePassword, c.KeyStorePassword, c.AccessToken, c.ExtraCredentials}
}
func (s *State) selectPrestoSQLDriver(d connections.ManagedDriver) {
	if d.Key != "prestosql" || !d.Supports("prestosql") {
		s.driverWork.message = "驱动包不包含 PrestoSQL 驱动。"
		return
	}
	a := s.connectionDraft.PrestoSQLOptions
	a.DriverVersion, a.DriverSHA256, a.DriverDirectory = d.Version, d.SHA256, d.Directory
	if _, err := connections.NormalizePrestoSQLOptions(a); err != nil {
		s.driverWork.message = "无法读取或导入驱动包，请检查文件、包类型和安装目录。"
		return
	}
	s.cancelMySQLConnectionTest()
	s.connectionDraft.PrestoSQLOptions = a
	s.prestosqlDriverChoice, s.driverWork.message = d.Version, ""
}
func (s *State) editorPrestoSQLFields(c *ui.Context, labelWidth, fieldWidth float32) {
	a := &s.connectionDraft.PrestoSQLOptions
	s.editorInputRow(c, "默认 Catalog：", "PrestoSQL 默认 Catalog", &s.connectionDraft.Database, labelWidth, fieldWidth)
	s.editorInputRow(c, "默认 Schema：", "PrestoSQL 默认 Schema", &a.DefaultSchema, labelWidth, fieldWidth)
	s.editorInputRow(c, "用户名：", "用户名", &s.connectionDraft.Username, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("密码："), func() { s.formTextInput(c, &s.connectionSecret, "连接密码", fieldWidth, false, true) })
	connectionFormCheckbox(c, &s.connectionSavePassword, "保存凭据", s.credentialVault == nil)
	versions := []string{"未选择驱动包"}
	for _, d := range s.driverWork.packages {
		versions = append(versions, d.Version)
	}
	if s.prestosqlDriverChoice == "" {
		s.prestosqlDriverChoice = versions[0]
	}
	before := s.prestosqlDriverChoice
	theme.Field(c, locales.Text("驱动包："), func() {
		connectionFormSelect(c, &s.prestosqlDriverChoice, versions, "PrestoSQL 驱动包", fieldWidth, s.DriverBusy())
	})
	if before != s.prestosqlDriverChoice {
		if s.prestosqlDriverChoice == versions[0] {
			s.cancelMySQLConnectionTest()
			a.DriverVersion, a.DriverSHA256 = "", ""
		} else {
			for _, d := range s.driverWork.packages {
				if d.Version == s.prestosqlDriverChoice {
					s.selectPrestoSQLDriver(d)
					break
				}
			}
		}
	}
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if ui.Button(c, locales.Text("刷新驱动包")).Disabled(s.driverPackages.List == nil || s.DriverBusy()).Clicked() {
			s.RefreshDriverPackages()
		}
		if ui.Button(c, locales.Text("导入驱动包")).Disabled(s.engineFilePicker == nil || s.driverPackages.Import == nil || s.DriverBusy()).Clicked() {
			s.engineFilePicker(PrestoSQLDriverPackageFile, -1, s.engineFileVersion)
		}
		if ui.Button(c, locales.Text("移除驱动包")).Disabled(s.driverPackages.Remove == nil || a.DriverVersion == "" || s.DriverBusy()).Clicked() {
			for _, d := range s.driverWork.packages {
				if d.Version == a.DriverVersion && d.SHA256 == a.DriverSHA256 {
					copy := cloneDrivers([]connections.ManagedDriver{d})[0]
					s.prestosqlRemoveOriginal = &copy
					s.prestosqlRemoveOpen = true
					break
				}
			}
		}
	})
	if s.driverWork.message != "" {
		ui.Text(c, locales.Text(s.driverWork.message)).Width(fieldWidth).TextColor(theme.Colors(c).Error)
	}
}
func (s *State) prestoSQLOptions(c *ui.Context, width float32, tab string) bool {
	if tab != "高级" && tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	fieldWidth := formWidth - connectionFormLabelWidth
	a := &s.connectionDraft.PrestoSQLOptions
	theme.Form(c, formWidth, func() {
		if tab == "高级" {
			for i, label := range prestoNumberLabels {
				s.editorInputRow(c, strings.TrimPrefix(label, "PrestoSQL "), label, &s.prestosqlNumbers[i], connectionFormLabelWidth, 120)
			}
			s.editorInputRow(c, "JDBC 参数：", "PrestoSQL JDBC 参数", &a.JDBCParameters, connectionFormLabelWidth, fieldWidth)
			theme.Field(c, locales.Text("访问令牌："), func() { s.formTextInput(c, &s.prestosqlSecrets[2], "PrestoSQL 访问令牌", fieldWidth, false, true) })
			theme.Field(c, locales.Text("附加凭据："), func() { s.formTextInput(c, &s.prestosqlSecrets[3], "PrestoSQL 附加凭据", fieldWidth, false, true) })
			ui.Text(c, locales.Text("JDBC 参数仅保存非凭据选项；密码、令牌和附加凭据使用独立输入框。")).Width(formWidth).FontSize(theme.Font(c, 11)).TextColor(theme.Colors(c).Muted)
			return
		}
		choices := make([]string, len(prestoTLSChoices))
		for i, v := range prestoTLSChoices {
			choices[i] = v.caption
		}
		old := s.prestosqlTLSChoice
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.prestosqlTLSChoice, choices, "PrestoSQL TLS 模式", fieldWidth, false)
		})
		if old != s.prestosqlTLSChoice {
			for _, v := range prestoTLSChoices {
				if v.caption == s.prestosqlTLSChoice {
					s.cancelMySQLConnectionTest()
					s.engineFileVersion++
					s.connectionDraft.TLSMode = v.mode
					break
				}
			}
		}
		if s.connectionDraft.TLSMode == "disable" {
			return
		}
		s.neo4jFileRow(c, "信任库", "PrestoSQL 信任库", "选择 PrestoSQL 信任库", &a.TrustStorePath, PrestoSQLTrustStoreFile, fieldWidth)
		theme.Field(c, locales.Text("信任库类型："), func() {
			connectionFormSelect(c, &a.TrustStoreType, []string{"", "JKS", "PKCS12"}, "PrestoSQL 信任库类型", fieldWidth, false)
		})
		theme.Field(c, locales.Text("信任库密码："), func() {
			s.formTextInput(c, &s.prestosqlSecrets[0], "PrestoSQL 信任库密码", fieldWidth, false, true)
		})
		s.neo4jFileRow(c, "客户端密钥库", "PrestoSQL 客户端密钥库", "选择 PrestoSQL 客户端密钥库", &a.KeyStorePath, PrestoSQLKeyStoreFile, fieldWidth)
		theme.Field(c, locales.Text("密钥库类型："), func() {
			connectionFormSelect(c, &a.KeyStoreType, []string{"", "JKS", "PKCS12"}, "PrestoSQL 密钥库类型", fieldWidth, false)
		})
		theme.Field(c, locales.Text("密钥库密码："), func() {
			s.formTextInput(c, &s.prestosqlSecrets[1], "PrestoSQL 密钥库密码", fieldWidth, false, true)
		})
	})
	return true
}
func (s *State) advancePrestoSQLOptions(p *connections.Profile) error {
	a := &p.PrestoSQLOptions
	values := []*int{&a.ConnectTimeoutSeconds, &a.QueryTimeoutSeconds, &a.MaxQueryProcesses, &a.PoolWaitMillis}
	minima, maxima := []int{1, 0, 1, 250}, []int{300, 3600, 16, 300000}
	for i, raw := range s.prestosqlNumbers {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < minima[i] || n > maxima[i] {
			return fieldError(prestoNumberLabels[i], "高级", "请检查 PrestoSQL 超时和会话上限")
		}
		*values[i] = n
	}
	if a.DriverVersion == "" || a.DriverSHA256 == "" {
		return fieldError("PrestoSQL 驱动包", "常规", "请选择或导入已安装的 PrestoSQL 驱动包")
	}
	return nil
}
func (s *State) validatePrestoSQLDraft(p connections.Profile) error {
	if _, err := connections.PrestoSQLProfileManagedConfig(p); err != nil {
		return fieldError("PrestoSQL 默认 Catalog", "常规", "请检查 PrestoSQL 服务器、Catalog、Schema 和 TLS 设置")
	}
	credentials := s.prestoCredentials()
	if err := connections.ValidatePrestoSQLCredentials(credentials); err != nil {
		return fieldError("连接密码", "常规", "请检查 PrestoSQL 凭据")
	}
	if p.TLSMode == "disable" && (credentials.Password != "" || credentials.AccessToken != "") {
		return fieldError("PrestoSQL TLS 模式", "SSL", "PrestoSQL 使用密码或访问令牌时必须启用 HTTPS")
	}
	return nil
}
func (s *State) prestoSQLRemoveModal(c *ui.Context) {
	theme.Modal(c, &s.prestosqlRemoveOpen, func() {
		original := s.prestosqlRemoveOriginal
		if original == nil {
			return
		}
		ui.Column(c).Width(480).MaxWidthPercent(95).Gap(10).Children(func() {
			ui.Text(c, locales.Text("移除 PrestoSQL 驱动包？")).Bold()
			ui.Text(c, original.Version).Wrap()
			ui.Text(c, original.SHA256).Wrap()
			ui.Text(c, locales.Text("正在使用的驱动包无法移除。已有连接档案会保留原版本选择，重新连接前需要重新导入或选择驱动包。")).Wrap()
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, locales.Text("取消")).Clicked() {
					s.prestosqlRemoveOpen = false
					s.prestosqlRemoveOriginal = nil
				}
				if ui.Button(c, locales.Text("确认移除驱动包")).Clicked() {
					s.removePrestoSQLDriver(*original)
				}
			})
		})
	})
}
func (s *State) removePrestoSQLDriver(d connections.ManagedDriver) {
	if !s.active || s.connectionDraft.Kind != connections.PrestoSQL || s.driverPackages.Remove == nil || d.Key != "prestosql" {
		return
	}
	s.cancelDriverWork()
	s.cancelMySQLConnectionTest()
	s.prestosqlRemoveOpen = false
	s.prestosqlRemoveOriginal = nil
	original := cloneDrivers([]connections.ManagedDriver{d})[0]
	_, _, directory, profile := s.currentDriverSelection()
	generation := s.driverWork.generation
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	results := make(chan driverWorkResult, 1)
	s.driverWork.cancel, s.driverWork.results, s.driverWork.message = cancel, results, ""
	remove := s.driverPackages.Remove
	go func() {
		defer cancel()
		err := remove(ctx, connections.PrestoSQL, original)
		results <- driverWorkResult{generation: generation, kind: connections.PrestoSQL, directory: directory, profile: profile, removed: &original, err: err}
	}()
}
func (s *State) consumePrestoSQLDriverRemoval(out driverWorkResult) {
	if out.err != nil {
		s.driverWork.message = "无法移除驱动包，请刷新列表后重试。"
		if errors.Is(out.err, connections.ErrManagedDriverInUse) {
			s.driverWork.message = "驱动包仍在使用，请先关闭相关连接。"
		}
		return
	}
	d := *out.removed
	s.driverWork.packages = slices.DeleteFunc(s.driverWork.packages, func(p connections.ManagedDriver) bool {
		return p.Version == d.Version && p.SHA256 == d.SHA256 && p.Directory == d.Directory
	})
	a := &s.connectionDraft.PrestoSQLOptions
	if a.DriverVersion == d.Version && a.DriverSHA256 == d.SHA256 {
		a.DriverVersion, a.DriverSHA256 = "", ""
		s.prestosqlDriverChoice = "未选择驱动包"
	}
	s.driverWork.message = "驱动包已移除。"
}

func prestoSnapshotCredentials(snap connectionEditorSnapshot) connections.PrestoSQLCredentials {
	return connections.PrestoSQLCredentials{Password: snap.secret, TrustStorePassword: snap.prestosqlSecrets[0], KeyStorePassword: snap.prestosqlSecrets[1], AccessToken: snap.prestosqlSecrets[2], ExtraCredentials: snap.prestosqlSecrets[3]}
}
func invalidPrestoCredentialError() error {
	return fmt.Errorf("%s", locales.Text("无法读取已保存的 PrestoSQL 凭据"))
}
