package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

// SQL can contain literal credentials. It is never stored in Profile,
// sqlite configuration, logs or error reports. This editor is UI-only until
// encrypted storage and actual connection execution are independently built.
func (s *State) beginMySQLInitialQuery() {
	if !s.connectionDialog || s.connectionStep != 1 || s.connectionDraft.Kind != connections.MySQL {
		return
	}
	s.mysqlInitialQueryDraft = s.mysqlInitialQuery
	s.mysqlInitialQueryError = ""
	s.mysqlInitialQueryOpen = true
}

func (s *State) confirmMySQLInitialQuery() {
	if !s.mysqlInitialQueryOpen {
		return
	}
	if len(s.mysqlInitialQueryDraft) > 64*1024 || strings.ContainsRune(s.mysqlInitialQueryDraft, 0) {
		s.mysqlInitialQueryError = "初始查询不能超过 64 KiB，也不能包含 NUL 字符"
		return
	}
	s.mysqlInitialQuery = s.mysqlInitialQueryDraft
	s.mysqlInitialQueryPreview = strings.Join(strings.Fields(s.mysqlInitialQuery), " ")
	s.mysqlInitialQueryDraft = ""
	s.mysqlInitialQueryError = ""
	s.mysqlInitialQueryOpen = false
}

func (s *State) cancelMySQLInitialQuery() {
	s.mysqlInitialQueryDraft = ""
	s.mysqlInitialQueryError = ""
	s.mysqlInitialQueryOpen = false
}

func (s *State) clearMySQLInitialQuery() {
	s.mysqlInitialQuery = ""
	s.mysqlInitialQueryPreview = ""
	s.cancelMySQLInitialQuery()
}

func (s *State) mysqlInitialQueryModal(c *ui.Context) {
	palette := theme.Colors(c)

	if !s.mysqlInitialQueryOpen {
		// Escape and backdrop dismissal can close a native MyGo modal
		// without passing through the Cancel button. Discard its temporary
		// text on the next frame as well.
		if s.mysqlInitialQueryDraft != "" || s.mysqlInitialQueryError != "" {
			s.cancelMySQLInitialQuery()
		}
		return
	}
	width, height := c.Size()
	dialogWidth := min(float32(700), width-40)
	dialogHeight := min(float32(500), height-40)
	ui.Modal(c, &s.mysqlInitialQueryOpen, func() {
		ui.Column(c).Width(dialogWidth).Height(dialogHeight).Padding(15, 24).
			Background(palette.Header).Gap(14).Children(func() {
			ui.Row(c).Justify(ui.Center).Children(func() {
				ui.Text(c, "初始查询").Bold().FontSize(theme.Font(c, 13))
			})
			ui.Text(c, "初始查询：").FontSize(theme.Font(c, 12))
			ui.TextArea(c, &s.mysqlInitialQueryDraft).
				Label("初始查询 SQL 编辑器").
				Placeholder("e.g.\nSET ROLE ALL;\nSET @var_name = 'value';").
				Width(min(float32(602), dialogWidth-48)).Grow(1).MinHeight(120).FontSize(theme.Font(c, 12))
			ui.Row(c).Children(func() {
				if ui.Button(c, "复制到剪贴板").Clicked() {
					c.WriteClipboard(s.mysqlInitialQueryDraft)
				}
			})
			if s.mysqlInitialQueryError != "" {
				ui.Text(c, s.mysqlInitialQueryError).
					FontSize(theme.Font(c, 12)).TextColor(palette.Error)
			}
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ui.PrimaryButton(c, "确定").Label("确认初始查询").Width(80).Clicked() {
					s.confirmMySQLInitialQuery()
				}
				if ui.Button(c, "取消").Label("取消初始查询").Width(80).Clicked() {
					s.cancelMySQLInitialQuery()
				}
			})
		})
	})
}
