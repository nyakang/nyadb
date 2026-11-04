package connectioneditor

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// EngineFile identifies a public file path. The editor owns the request token;
// the native window supplies only a main-thread completion.
type EngineFile int

const (
	SQLiteAttachedFile EngineFile = iota
	PostgreSQLCAFile
	PostgreSQLCertificateFile
	PostgreSQLKeyFile
	SQLiteMainFile
)

func (s *State) SetEngineFilePicker(pick func(EngineFile, int, uint64)) { s.engineFilePicker = pick }

func (s *State) EngineFileVersion() uint64 { return s.engineFileVersion }

func (s *State) SelectEngineFile(kind EngineFile, row int, version uint64, path string) {
	if !s.active || version != s.engineFileVersion || path == "" {
		return
	}
	if kind == SQLiteMainFile {
		if s.connectionDraft.Kind != connections.SQLite {
			return
		}
		s.connectionDraft.Database = path
	} else if kind == SQLiteAttachedFile {
		if s.connectionDraft.Kind != connections.SQLite || row < 0 || row >= len(s.connectionDraft.SQLiteOptions.Attachments) {
			return
		}
		s.connectionDraft.SQLiteOptions.Attachments[row].File = path
	} else {
		if s.connectionDraft.Kind != connections.PostgreSQL {
			return
		}
		a := &s.connectionDraft.PostgreSQLOptions
		switch kind {
		case PostgreSQLCAFile:
			a.CAFile = path
		case PostgreSQLCertificateFile:
			a.CertificateFile = path
		case PostgreSQLKeyFile:
			a.KeyFile = path
		default:
			return
		}
	}
	s.connectionError = ""
}

func (s *State) EngineFilePickerError(version uint64) {
	if s.active && version == s.engineFileVersion {
		s.connectionError = locales.Text("无法选择文件，请手动输入文件路径。")
	}
}

func (s *State) initEngineOptionsInputs() {
	s.sqliteAttachmentList = ui.ListState{}
	s.sqliteAttachmentRow = -1
	if s.connectionDraft.Kind == connections.PostgreSQL {
		a, err := connections.NormalizePostgreSQLOptions(s.connectionDraft.PostgreSQLOptions)
		if err == nil {
			s.connectionDraft.PostgreSQLOptions = a
		}
		for i, n := range []int{a.ConnectTimeoutSeconds, a.MaxConnections, a.StatementTimeoutMS, a.LockTimeoutMS} {
			s.engineNumbers[i] = strconv.Itoa(n)
		}
		s.sqlMaxConnections = strconv.Itoa(a.SQLMaxConnections)
		if s.connectionDraft.TLSMode == "" {
			s.connectionDraft.TLSMode = "require"
		}
	} else if s.connectionDraft.Kind == connections.SQLite {
		a, err := connections.NormalizeSQLiteOptions(s.connectionDraft.SQLiteOptions)
		if err == nil {
			s.connectionDraft.SQLiteOptions = a
		}
		s.sqliteBusyTimeout = strconv.Itoa(a.BusyTimeoutMS)
		s.sqlMaxConnections = strconv.Itoa(a.SQLMaxConnections)
		s.sqliteMaxConnections = strconv.Itoa(a.MaxConnections)
	}
}

func (s *State) advanceEngineOptions(p *connections.Profile) error {
	if p.Kind == connections.PostgreSQL || p.Kind == connections.SQLite {
		n, err := strconv.Atoi(strings.TrimSpace(s.sqlMaxConnections))
		if err != nil || n < 1 || n > 64 {
			return fmt.Errorf("%s: 1–64", locales.Text("SQL 会话上限"))
		}
		if p.Kind == connections.PostgreSQL {
			p.PostgreSQLOptions.SQLMaxConnections = n
		} else {
			p.SQLiteOptions.SQLMaxConnections = n
		}
	}
	switch p.Kind {
	case connections.PostgreSQL:
		values := [4]int{}
		labels := []string{"连接超时（秒）", "最大连接数", "语句超时（毫秒）", "锁等待超时（毫秒）"}
		for i, raw := range s.engineNumbers {
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				return fmt.Errorf("%s: %s", locales.Text(labels[i]), locales.Text("请输入整数。"))
			}
			values[i] = n
		}
		p.PostgreSQLOptions.ConnectTimeoutSeconds, p.PostgreSQLOptions.MaxConnections = values[0], values[1]
		p.PostgreSQLOptions.StatementTimeoutMS, p.PostgreSQLOptions.LockTimeoutMS = values[2], values[3]
		a, err := connections.NormalizePostgreSQLOptions(p.PostgreSQLOptions)
		p.PostgreSQLOptions = a
		return err
	case connections.SQLite:
		limit, e := strconv.Atoi(strings.TrimSpace(s.sqliteMaxConnections))
		if e != nil || limit < 1 || limit > 64 {
			return fmt.Errorf("%s: 1–64", locales.Text("后台连接上限"))
		}
		p.SQLiteOptions.MaxConnections = limit
		n, err := strconv.Atoi(strings.TrimSpace(s.sqliteBusyTimeout))
		if err != nil {
			return fmt.Errorf("%s: %s", locales.Text("锁等待超时（毫秒）"), locales.Text("请输入整数。"))
		}
		p.SQLiteOptions.BusyTimeoutMS = n
		a, err := connections.NormalizeSQLiteOptions(p.SQLiteOptions)
		p.SQLiteOptions = a // normalization also captures an independent attachment slice
		return err
	}
	return nil
}

func (s *State) connectionEngineOptions(c *ui.Context, width float32, tab string) bool {
	formWidth := connectionFormWidth(width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	switch s.connectionDraft.Kind {
	case connections.ClickHouse:
		if tab != "SSL" {
			return false
		}
		ui.Column(c).Width(formWidth).Gap(12).Children(func() {
			ui.Text(c, locales.Text("ClickHouse 使用原生 TCP 协议，默认端口 9000；TLS 服务通常使用 9440。"))
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.Text("TLS 模式：")).Width(labelWidth)
				connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"disable", "verify-full"}, "ClickHouse TLS 模式", fieldWidth, false)
			})
		})
		return true
	case connections.PostgreSQL:
		a := &s.connectionDraft.PostgreSQLOptions
		switch tab {
		case "高级":
			ui.Column(c).Width(formWidth).Gap(0).Children(func() {
				for i, label := range []string{"连接超时（秒）", "最大连接数", "语句超时（毫秒）", "锁等待超时（毫秒）"} {
					display := label
					if i == 1 {
						display = "后台连接上限"
					}
					s.editorInputRow(c, display+"：", label, &s.engineNumbers[i], labelWidth, 120)
				}
				s.editorInputRow(c, "SQL 会话上限", "SQL 会话上限", &s.sqlMaxConnections, labelWidth, 120)
				s.editorInputRow(c, "应用名称：", "应用名称", &a.ApplicationName, labelWidth, fieldWidth)
				s.editorInputRow(c, "备份工具目录：", "PostgreSQL 工具目录", &a.ToolsDirectory, labelWidth, fieldWidth)
				s.editorInputRow(c, "默认模式：", "默认模式", &a.DefaultSchema, labelWidth, fieldWidth)
				ui.Text(c, locales.Text("语句及锁等待超时为 0 时不设置超时；默认模式在每条物理连接初始化。"))
			})
		case "SSL":
			ui.Column(c).Width(formWidth).Gap(0).Children(func() {
				connectionFormRow(c).Children(func() {
					ui.Text(c, locales.Text("TLS 模式")).Width(labelWidth)
					connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"disable", "prefer", "require", "verify-ca", "verify-full"}, "PostgreSQL TLS 模式", fieldWidth, false)
				})
				for _, f := range []struct {
					name  string
					value *string
					kind  EngineFile
				}{
					{"CA 证书", &a.CAFile, PostgreSQLCAFile},
					{"客户端证书", &a.CertificateFile, PostgreSQLCertificateFile},
					{"客户端密钥", &a.KeyFile, PostgreSQLKeyFile},
				} {
					connectionFormRow(c).Children(func() {
						ui.Text(c, locales.Text(f.name)).Width(labelWidth)
						connectionFormTextInput(c, f.value, "PostgreSQL "+f.name, fieldWidth-29, false, false)
						ui.Box(c).Width(connectionFormBrowseGap)
						connectionFormBrowse(c, "选择 PostgreSQL "+f.name, s.engineFilePicker == nil, func() {
							if s.engineFilePicker != nil {
								s.engineFilePicker(f.kind, -1, s.engineFileVersion)
							}
						})
					})
				}
				ui.Text(c, locales.Text("verify-full 校验证书链和主机名；客户端证书与密钥必须成对设置。"))
			})
		case "SSH", "HTTP":
			ui.Text(c, locales.Text("此引擎的隧道尚未实现。")).Width(formWidth)
		default:
			return false
		}
	case connections.SQLite:
		a := &s.connectionDraft.SQLiteOptions
		switch tab {
		case "高级":
			ui.Column(c).Width(formWidth).Gap(8).Children(func() {
				connectionFormCheckbox(c, &a.ReadOnly, "只读连接", false)
				connectionFormCheckbox(c, &a.RequireExisting, "要求主数据库文件已存在", false)
				connectionFormCheckbox(c, &a.ForeignKeys, "启用外键约束", false)
				s.editorInputRow(c, "锁等待超时（毫秒）", "SQLite 锁等待超时", &s.sqliteBusyTimeout, labelWidth, 120)
				s.editorInputRow(c, "后台连接上限", "后台连接上限", &s.sqliteMaxConnections, labelWidth, 120)
				s.editorInputRow(c, "SQL 会话上限", "SQL 会话上限", &s.sqlMaxConnections, labelWidth, 120)
				ui.Text(c, locales.Text("取消要求文件存在后，连接时可创建主数据库；只读连接始终要求文件存在。"))
			})
		case "附加的数据库":
			s.connectionSQLiteAttachments(c, formWidth)
		case "HTTP":
			ui.Text(c, locales.Text("此引擎的隧道尚未实现。")).Width(formWidth)
		default:
			return false
		}
	default:
		return false
	}
	return true
}

func (s *State) connectionSQLiteAttachments(c *ui.Context, width float32) {
	a := &s.connectionDraft.SQLiteOptions
	ui.Column(c).Width(width).Gap(8).Children(func() {
		s.sqliteAttachmentList.Selected = &s.sqliteAttachmentRow
		theme.Table(c, &s.sqliteAttachmentList, []ui.TableColumn{
			{Title: locales.Text("名称"), Width: 130}, {Title: locales.Text("数据库文件"), Width: width - 220}, {Title: locales.Text("只读"), Width: 85},
		}, len(a.Attachments), func(row, col int) {
			v := a.Attachments[row]
			text := v.Name
			if col == 1 {
				text = v.File
			}
			if col == 2 {
				text = ""
				if v.ReadOnly {
					text = locales.Text("只读")
				}
			}
			ui.Text(c, text).SingleLine().Tooltip(text)
		}).Height(150)
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, locales.Text("添加附加库")).Disabled(len(a.Attachments) >= 10).Clicked() {
				s.engineFileVersion++
				a.Attachments = append(a.Attachments, connections.SQLiteAttachment{})
				s.sqliteAttachmentRow = len(a.Attachments) - 1
			}
			if ui.Button(c, locales.Text("移除附加库")).Disabled(s.sqliteAttachmentRow < 0 || s.sqliteAttachmentRow >= len(a.Attachments)).Clicked() {
				s.engineFileVersion++
				i := s.sqliteAttachmentRow
				a.Attachments = append(a.Attachments[:i:i], a.Attachments[i+1:]...)
				s.sqliteAttachmentRow = min(i, len(a.Attachments)-1)
			}
		})
		if i := s.sqliteAttachmentRow; i >= 0 && i < len(a.Attachments) {
			v := &a.Attachments[i]
			s.editorInputRow(c, "附加库名称：", "附加库名称", &v.Name, connectionFormLabelWidth, width-connectionFormLabelWidth)
			connectionFormRow(c).Children(func() {
				ui.Text(c, locales.Text("数据库文件")).Width(connectionFormLabelWidth)
				connectionFormTextInput(c, &v.File, "附加库文件", width-connectionFormLabelWidth-29, false, false)
				ui.Box(c).Width(connectionFormBrowseGap)
				connectionFormBrowse(c, "选择附加库文件", s.engineFilePicker == nil, func() {
					if s.engineFilePicker != nil {
						s.engineFilePicker(SQLiteAttachedFile, i, s.engineFileVersion)
					}
				})
			})
			connectionFormCheckbox(c, &v.ReadOnly, "附加库只读", false)
		}
		ui.Text(c, locales.Text("最多 10 个附加库，名称唯一；连接时不会创建附加文件。SQL 请使用附加库名限定对象。"))
	})
}
