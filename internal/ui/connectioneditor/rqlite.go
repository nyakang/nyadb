package connectioneditor

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) initializeRQLiteInputs() {
	o := s.connectionDraft.RQLiteOptions
	s.rqliteAuthChoice = neo4jAuthCaption(o.Auth)
	s.rqliteTLSChoice = neo4jTLSCaption(s.connectionDraft.TLSMode)
	s.rqliteConsistencyChoice = o.Consistency
	for i, n := range []int{o.FreshnessMilliseconds, o.ConnectTimeoutSeconds, o.RequestTimeoutSeconds, o.ForwardTimeoutSeconds, o.DatabaseTimeoutMilliseconds, o.LinearizableTimeoutMilliseconds, o.MaxRequests, o.SQLMaxConnections} {
		s.rqliteNumbers[i] = strconv.Itoa(n)
	}
}

func (s *State) setRQLiteAuth(auth string) {
	if s.connectionDraft.Kind != connections.RQLite || s.connectionDraft.RQLiteOptions.Auth == auth || auth != "none" && auth != "basic" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.engineFileVersion++
	s.connectionDraft.RQLiteOptions.Auth = auth
	s.rqliteAuthChoice = neo4jAuthCaption(auth)
	s.connectionDraft.Username, s.connectionSecret = "", ""
	s.connectionSavePassword = false
	s.connectionError, s.validationField, s.validationErrorField = "", "", ""
}

func (s *State) setRQLiteTLS(mode string) {
	if s.connectionDraft.Kind != connections.RQLite || s.connectionDraft.TLSMode == mode || mode != "disable" && mode != "require" && mode != "verify-full" {
		return
	}
	s.cancelMySQLConnectionTest()
	s.engineFileVersion++
	s.connectionDraft.TLSMode = mode
	s.rqliteTLSChoice = neo4jTLSCaption(mode)
	o := &s.connectionDraft.RQLiteOptions
	if mode != "verify-full" {
		o.CAFile, o.ServerName = "", ""
	}
	if mode == "disable" {
		o.ClientCertFile, o.ClientKeyFile = "", ""
	}
}

func (s *State) editorRQLiteFields(c *ui.Context, labelWidth, fieldWidth float32) {
	p := &s.connectionDraft
	s.editorInputRow(c, "服务路径：", "RQLite 服务路径", &p.RQLiteOptions.BasePath, labelWidth, fieldWidth)
	theme.Field(c, locales.Text("数据库："), func() { ui.Text(c, "main") })
	theme.Field(c, locales.Text("认证方式："), func() {
		connectionFormSelect(c, &s.rqliteAuthChoice, []string{"无认证", "用户名和密码"}, "RQLite 认证方式", fieldWidth, false)
	})
	auth := "none"
	if s.rqliteAuthChoice == "用户名和密码" {
		auth = "basic"
	}
	s.setRQLiteAuth(auth)
	if auth == "basic" {
		s.editorInputRow(c, "用户名：", "RQLite 用户名", &p.Username, labelWidth, fieldWidth)
		theme.Field(c, locales.Text("密码："), func() {
			s.formTextInput(c, &s.connectionSecret, "RQLite 密码", fieldWidth, false, true)
		})
		connectionFormCheckbox(c, &s.connectionSavePassword, "保存密码", s.credentialVault == nil)
	}
}

var rqliteNumberLabels = [8]string{"RQLite 新鲜度（毫秒）", "RQLite 连接超时（秒）", "RQLite 请求超时（秒）", "RQLite 转发超时（秒）", "RQLite 数据库超时（毫秒）", "RQLite 线性一致超时（毫秒）", "RQLite 请求并发上限", "RQLite SQL 连接上限"}

func (s *State) rqliteOptions(c *ui.Context, width float32, tab string) bool {
	if tab != "高级" && tab != "SSL" {
		return false
	}
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	o := &s.connectionDraft.RQLiteOptions
	theme.Form(c, formWidth, func() {
		if tab == "高级" {
			theme.Field(c, locales.Text("读取一致性："), func() {
				connectionFormSelect(c, &s.rqliteConsistencyChoice, []string{"none", "weak", "strong", "linearizable"}, "RQLite 读取一致性", fieldWidth, false)
			})
			if o.Consistency != s.rqliteConsistencyChoice {
				s.cancelMySQLConnectionTest()
				o.Consistency = s.rqliteConsistencyChoice
			}
			for i, label := range rqliteNumberLabels {
				s.editorInputRow(c, label, label, &s.rqliteNumbers[i], labelWidth, 120)
			}
			for _, f := range []struct {
				label string
				value *bool
			}{{"RQLite 严格新鲜度", &o.FreshnessStrict}, {"RQLite 只读保护", &o.ReadOnly}, {"RQLite SQL 批次事务", &o.BatchTransaction}} {
				connectionFormCheckbox(c, f.value, f.label, false)
			}
			ui.Text(c, locales.Text("新鲜度仅适用于 none 读取；SQL 批次事务将一次多语句请求作为事务执行。导入始终按原子批次提交。"))
			return
		}
		theme.Field(c, locales.Text("TLS 模式："), func() {
			connectionFormSelect(c, &s.rqliteTLSChoice, []string{"禁用 TLS", "接受自签名证书", "验证服务器证书"}, "RQLite TLS 模式", fieldWidth, false)
		})
		mode := "disable"
		if s.rqliteTLSChoice == "接受自签名证书" {
			mode = "require"
		} else if s.rqliteTLSChoice == "验证服务器证书" {
			mode = "verify-full"
		}
		s.setRQLiteTLS(mode)
		if mode == "disable" {
			return
		}
		if mode == "verify-full" {
			s.neo4jFileRow(c, "CA 证书", "RQLite CA 证书", "选择 RQLite CA 证书", &o.CAFile, RQLiteCAFile, fieldWidth)
			s.editorInputRow(c, "证书服务器名", "RQLite 证书服务器名", &o.ServerName, labelWidth, fieldWidth)
		}
		s.neo4jFileRow(c, "客户端证书", "RQLite 客户端证书", "选择 RQLite 客户端证书", &o.ClientCertFile, RQLiteCertificateFile, fieldWidth)
		s.neo4jFileRow(c, "客户端私钥", "RQLite 客户端私钥", "选择 RQLite 客户端私钥", &o.ClientKeyFile, RQLiteKeyFile, fieldWidth)
	})
	return true
}

func (s *State) advanceRQLiteOptions(p *connections.Profile) error {
	o := &p.RQLiteOptions
	values := []*int{&o.FreshnessMilliseconds, &o.ConnectTimeoutSeconds, &o.RequestTimeoutSeconds, &o.ForwardTimeoutSeconds, &o.DatabaseTimeoutMilliseconds, &o.LinearizableTimeoutMilliseconds, &o.MaxRequests, &o.SQLMaxConnections}
	limits := [8]int{86400000, 300, 86400, 86399, 86399999, 86399999, 32, 32}
	for i, target := range values {
		n, err := strconv.Atoi(strings.TrimSpace(s.rqliteNumbers[i]))
		if err != nil || n < 0 || i != 0 && n == 0 || n > limits[i] {
			return fieldError(rqliteNumberLabels[i], "高级", "请检查 RQLite 数字设置")
		}
		*target = n
	}
	if o.ForwardTimeoutSeconds >= o.RequestTimeoutSeconds || o.DatabaseTimeoutMilliseconds >= o.RequestTimeoutSeconds*1000 || o.LinearizableTimeoutMilliseconds >= o.RequestTimeoutSeconds*1000 || o.SQLMaxConnections > o.MaxRequests {
		return fieldError("RQLite 请求超时（秒）", "高级", "内部超时必须小于请求超时，SQL 连接上限不得超过请求并发上限")
	}
	if (o.FreshnessMilliseconds != 0 || o.FreshnessStrict) && o.Consistency != "none" || o.FreshnessStrict && o.FreshnessMilliseconds == 0 {
		return fieldError("RQLite 读取一致性", "高级", "新鲜度要求 none 读取；严格新鲜度要求正数毫秒值")
	}
	return nil
}

func (s *State) validateRQLiteDraft(p connections.Profile) error {
	if p.RQLiteOptions.Auth == "basic" && (p.Username == "" || strings.Contains(p.Username, ":")) {
		return fieldError("RQLite 用户名", "常规", "请输入有效的 RQLite 用户名")
	}
	cfg, err := connections.RQLiteProfileConfig(p)
	if err != nil {
		return fieldError("RQLite TLS 模式", "SSL", "请检查 RQLite 主机、服务路径和 TLS 设置")
	}
	if connections.ValidateRQLiteCredential(cfg, s.connectionSecret) != nil {
		return fieldError("RQLite 密码", "常规", "请检查 RQLite 凭据")
	}
	return nil
}
