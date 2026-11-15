package connectioneditor

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) initializeTDengineInputs() {
	o := s.connectionDraft.TDengineOptions
	s.engineNumbers = [4]string{strconv.Itoa(o.ConnectTimeoutSeconds), strconv.Itoa(o.QueryTimeoutSeconds), strconv.Itoa(o.MaxConnections), strconv.Itoa(o.SQLMaxConnections)}
}
func (s *State) advanceTDengineOptions(p *connections.Profile) error {
	o := &p.TDengineOptions
	for i, target := range []*int{&o.ConnectTimeoutSeconds, &o.QueryTimeoutSeconds, &o.MaxConnections, &o.SQLMaxConnections} {
		n, err := strconv.Atoi(strings.TrimSpace(s.engineNumbers[i]))
		if err != nil {
			return fieldError("最大连接数", "高级", "请输入有效范围内的整数")
		}
		*target = n
	}
	checked, err := connections.NormalizeTDengineOptions(*o)
	*o = checked
	return err
}
func (s *State) tdengineOptions(c *ui.Context, width float32, tab string) bool {
	formWidth := connectionScaledFormWidth(c, width)
	fieldWidth := formWidth - connectionFormLabelWidth
	o := &s.connectionDraft.TDengineOptions
	if tab == "高级" {
		theme.Form(c, formWidth, func() {
			for i, label := range []string{"连接超时（秒）", "查询超时（秒）", "后台连接上限", "SQL 会话上限"} {
				s.editorInputRow(c, label, label, &s.engineNumbers[i], connectionFormLabelWidth, 120)
			}
			connectionFormCheckbox(c, &o.ReadOnly, "只读模式", false)
		})
		return true
	}
	if tab == "SSL" {
		theme.Form(c, formWidth, func() {
			theme.Field(c, locales.Text("TLS 模式："), func() {
				connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"disable", "require", "verify-full"}, "TDengine TLS 模式", fieldWidth, false)
			})
			if s.connectionDraft.TLSMode == "verify-full" {
				s.editorInputRow(c, "CA 证书", "TDengine CA 证书", &o.CAFile, connectionFormLabelWidth, fieldWidth)
			}
		})
		return true
	}
	return false
}
