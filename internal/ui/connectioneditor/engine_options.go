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
	DuckDBMainFile
	DuckDBLibraryFile
)

func (s *State) SetEngineFilePicker(pick func(EngineFile, int, uint64)) { s.engineFilePicker = pick }

func (s *State) EngineFileVersion() uint64 { return s.engineFileVersion }

func (s *State) SelectEngineFile(kind EngineFile, row int, version uint64, path string) {
	if !s.active || version != s.engineFileVersion || path == "" {
		return
	}
	if kind == DuckDBMainFile || kind == DuckDBLibraryFile {
		if s.connectionDraft.Kind != connections.DuckDB {
			return
		}
		if kind == DuckDBMainFile {
			s.connectionDraft.Database = path
		} else {
			s.connectionDraft.DuckDBOptions.LibraryFile = path
		}
		s.connectionError = ""
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
		if !s.connectionDraft.Kind.IsPostgreSQLFamily() {
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
	if s.connectionDraft.Kind == connections.Redis {
		a, _ := connections.NormalizeRedisOptions(s.connectionDraft.RedisOptions)
		s.connectionDraft.RedisOptions = a
		s.engineNumbers[0], s.engineNumbers[1], s.sqlMaxConnections = strconv.Itoa(a.ConnectTimeoutSeconds), strconv.Itoa(a.MaxConnections), strconv.Itoa(a.QueryMaxConnections)
	}
	if s.connectionDraft.Kind == connections.MongoDB {
		a, _ := connections.NormalizeMongoDBOptions(s.connectionDraft.MongoDBOptions)
		s.connectionDraft.MongoDBOptions = a
		s.engineNumbers[0], s.engineNumbers[1], s.sqlMaxConnections = strconv.Itoa(a.ConnectTimeoutSeconds), strconv.Itoa(a.MaxConnections), strconv.Itoa(a.QueryMaxConnections)
	}
	if s.connectionDraft.Kind == connections.Oracle {
		a, err := connections.NormalizeOracleOptions(s.connectionDraft.OracleOptions)
		if err == nil {
			s.connectionDraft.OracleOptions = a
		}
		s.engineNumbers[0], s.engineNumbers[1] = strconv.Itoa(a.ConnectTimeoutSeconds), strconv.Itoa(a.MaxConnections)
		s.sqlMaxConnections = strconv.Itoa(a.SQLMaxConnections)
	}

	if s.connectionDraft.Kind == connections.DuckDB {
		a, err := connections.NormalizeDuckDBOptions(s.connectionDraft.DuckDBOptions)
		if err == nil {
			s.connectionDraft.DuckDBOptions = a
		}
		s.engineNumbers = [4]string{strconv.Itoa(a.MemoryLimitMB), strconv.Itoa(a.Threads), strconv.Itoa(a.MaxConnections), strconv.Itoa(a.SQLMaxConnections)}
	}
	if s.connectionDraft.Kind == connections.SQLServer {
		a, err := connections.NormalizeSQLServerOptions(s.connectionDraft.SQLServerOptions)
		if err == nil {
			s.connectionDraft.SQLServerOptions = a
		}
		s.engineNumbers[0], s.engineNumbers[1] = strconv.Itoa(a.ConnectTimeoutSeconds), strconv.Itoa(a.MaxConnections)
		s.sqlMaxConnections = strconv.Itoa(a.SQLMaxConnections)
	}
	s.sqliteAttachmentList = ui.ListState{}
	s.sqliteAttachmentRow = -1
	if s.connectionDraft.Kind.IsPostgreSQLFamily() {
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
	if p.Kind == connections.Redis {
		values := []*int{&p.RedisOptions.ConnectTimeoutSeconds, &p.RedisOptions.MaxConnections, &p.RedisOptions.QueryMaxConnections}
		for i, raw := range []string{s.engineNumbers[0], s.engineNumbers[1], s.sqlMaxConnections} {
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				return fieldError("最大连接数", "高级", "请输入有效范围内的整数")
			}
			*values[i] = n
		}
		a, err := connections.NormalizeRedisOptions(p.RedisOptions)
		p.RedisOptions = a
		return err
	}
	if p.Kind == connections.MongoDB {
		values := []*int{&p.MongoDBOptions.ConnectTimeoutSeconds, &p.MongoDBOptions.MaxConnections, &p.MongoDBOptions.QueryMaxConnections}
		for i, raw := range []string{s.engineNumbers[0], s.engineNumbers[1], s.sqlMaxConnections} {
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				return fieldError("最大连接数", "高级", "请输入有效范围内的整数")
			}
			*values[i] = n
		}
		a, err := connections.NormalizeMongoDBOptions(p.MongoDBOptions)
		p.MongoDBOptions = a
		return err
	}
	if p.Kind == connections.Oracle {
		values := []*int{&p.OracleOptions.ConnectTimeoutSeconds, &p.OracleOptions.MaxConnections, &p.OracleOptions.SQLMaxConnections}
		for i, raw := range []string{s.engineNumbers[0], s.engineNumbers[1], s.sqlMaxConnections} {
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				return fieldError("最大连接数", "高级", "请输入有效范围内的整数")
			}
			*values[i] = n
		}
		a, err := connections.NormalizeOracleOptions(p.OracleOptions)
		p.OracleOptions = a
		return err
	}

	if p.Kind == connections.DuckDB {
		values := []*int{&p.DuckDBOptions.MemoryLimitMB, &p.DuckDBOptions.Threads, &p.DuckDBOptions.MaxConnections, &p.DuckDBOptions.SQLMaxConnections}
		for i, raw := range s.engineNumbers {
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				return fieldError("DuckDB 运行限制", "高级", "请输入有效范围内的整数")
			}
			*values[i] = n
		}
		a, err := connections.NormalizeDuckDBOptions(p.DuckDBOptions)
		p.DuckDBOptions = a
		return err
	}
	if p.Kind == connections.SQLServer {
		values := []*int{&p.SQLServerOptions.ConnectTimeoutSeconds, &p.SQLServerOptions.MaxConnections, &p.SQLServerOptions.SQLMaxConnections}
		for i, raw := range []string{s.engineNumbers[0], s.engineNumbers[1], s.sqlMaxConnections} {
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				return fieldError("最大连接数", "高级", "请输入有效范围内的整数")
			}
			*values[i] = n
		}
		a, err := connections.NormalizeSQLServerOptions(p.SQLServerOptions)
		p.SQLServerOptions = a
		return err
	}
	if p.Kind.IsPostgreSQLFamily() || p.Kind == connections.SQLite {
		n, err := strconv.Atoi(strings.TrimSpace(s.sqlMaxConnections))
		if err != nil || n < 1 || n > 64 {
			return fieldError("SQL 会话上限", "高级", "SQL 会话上限必须是 1–64 之间的整数")
		}
		if p.Kind.IsPostgreSQLFamily() {
			p.PostgreSQLOptions.SQLMaxConnections = n
		} else {
			p.SQLiteOptions.SQLMaxConnections = n
		}
	}
	switch p.Kind {
	case connections.PostgreSQL, connections.CockroachDB, connections.OpenGauss, connections.GaussDB:
		values := [4]int{}
		labels := []string{"连接超时（秒）", "最大连接数", "语句超时（毫秒）", "锁等待超时（毫秒）"}
		for i, raw := range s.engineNumbers {
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				return &FieldError{Field: labels[i], Page: "高级", Message: fmt.Sprintf(locales.Text("%s：请输入有效范围内的整数"), locales.Text(labels[i]))}
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
			return fieldError("后台连接上限", "高级", "后台连接上限必须是 1–64 之间的整数")
		}
		p.SQLiteOptions.MaxConnections = limit
		n, err := strconv.Atoi(strings.TrimSpace(s.sqliteBusyTimeout))
		if err != nil {
			return fieldError("SQLite 锁等待超时", "高级", "锁等待超时必须是有效的非负整数")
		}
		p.SQLiteOptions.BusyTimeoutMS = n
		a, err := connections.NormalizeSQLiteOptions(p.SQLiteOptions)
		p.SQLiteOptions = a // normalization also captures an independent attachment slice
		return err
	}
	return nil
}

func (s *State) connectionEngineOptions(c *ui.Context, width float32, tab string) bool {
	formWidth := connectionScaledFormWidth(c, width)
	labelWidth, fieldWidth := connectionFormLabelWidth, formWidth-connectionFormLabelWidth
	switch s.connectionDraft.Kind {
	case connections.Redis:
		return s.redisOptions(c, formWidth, tab)
	case connections.MongoDB:
		return s.mongoDBOptions(c, formWidth, tab)
	case connections.Oracle:
		return s.oracleOptions(c, formWidth, tab)
	case connections.DuckDB:
		if tab == "高级" {
			s.duckDBAdvanced(c, width)
			return true
		}
		return false
	case connections.SQLServer:
		a := &s.connectionDraft.SQLServerOptions
		if tab == "高级" {
			theme.Form(c, formWidth, func() {
				s.editorInputRow(c, "连接超时（秒）", "连接超时（秒）", &s.engineNumbers[0], labelWidth, 120)
				s.editorInputRow(c, "后台连接上限", "后台连接上限", &s.engineNumbers[1], labelWidth, 120)
				s.editorInputRow(c, "SQL 会话上限", "SQL 会话上限", &s.sqlMaxConnections, labelWidth, 120)
				s.editorInputRow(c, "应用名称：", "应用名称", &a.ApplicationName, labelWidth, fieldWidth)
				s.editorInputRow(c, "默认模式：", "默认模式", &a.DefaultSchema, labelWidth, fieldWidth)
				ui.Text(c, locales.Text("默认模式用于导航排序；SQL 中请使用模式名限定对象。"))
			})
			return true
		}
		if tab == "SSL" {
			theme.Form(c, formWidth, func() {
				theme.Field(c, locales.Text("TLS 模式："), func() {
					connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"verify-full", "require", "disable"}, "SQL Server TLS 模式", fieldWidth, false)
				})
				s.editorInputRow(c, "CA 证书", "SQL Server CA 证书", &a.CAFile, labelWidth, fieldWidth)
				s.editorInputRow(c, "证书服务器名", "证书服务器名", &a.ServerName, labelWidth, fieldWidth)
				ui.Text(c, locales.Text("verify-full 校验服务器证书；require 仅加密，disable 关闭 TLS。"))
			})
			return true
		}
		return false
	case connections.ClickHouse:
		if tab != "SSL" {
			return false
		}
		theme.Form(c, formWidth, func() {
			ui.Text(c, locales.Text("ClickHouse 使用原生 TCP 协议，默认端口 9000；TLS 服务通常使用 9440。"))
			theme.Field(c, locales.Text("TLS 模式："), func() {

				connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"disable", "verify-full"}, "ClickHouse TLS 模式", fieldWidth, false)
			})
		})
		return true
	case connections.PostgreSQL, connections.CockroachDB, connections.OpenGauss, connections.GaussDB:
		a := &s.connectionDraft.PostgreSQLOptions
		product := "PostgreSQL"
		if s.connectionDraft.Kind == connections.CockroachDB {
			product = "CockroachDB"
		} else if s.connectionDraft.Kind.IsGaussFamily() {
			product = "openGauss"
			if s.connectionDraft.Kind == connections.GaussDB {
				product = "GaussDB"
			}
		}
		switch tab {
		case "高级":
			theme.Form(c, formWidth, func() {
				if s.connectionDraft.Kind == connections.GaussDB {
					s.editorInputRow(c, "其他端点：", "GaussDB 其他端点", &s.connectionDraft.GaussDBOptions.AdditionalEndpoints, labelWidth, fieldWidth)
					theme.Field(c, locales.Text("目标会话："), func() {
						connectionFormSelect(c, &s.connectionDraft.GaussDBOptions.TargetSessionAttrs, []string{"read-write", "read-only", "any", "primary", "standby", "prefer-standby"}, "GaussDB 目标会话", fieldWidth, false)
					})
					ui.Text(c, locales.Text("其他端点用逗号分隔；IPv6 和端口写作 [地址]:端口。"))
				}
				for i, label := range []string{"连接超时（秒）", "最大连接数", "语句超时（毫秒）", "锁等待超时（毫秒）"} {
					if i == 3 && s.connectionDraft.Kind.IsGaussFamily() {
						continue
					}
					display := label
					if i == 1 {
						display = "后台连接上限"
					}
					s.editorInputRow(c, display+"：", label, &s.engineNumbers[i], labelWidth, 120)
				}
				s.editorInputRow(c, "SQL 会话上限", "SQL 会话上限", &s.sqlMaxConnections, labelWidth, 120)
				s.editorInputRow(c, "应用名称：", "应用名称", &a.ApplicationName, labelWidth, fieldWidth)
				if s.connectionDraft.Kind == connections.PostgreSQL {
					s.editorInputRow(c, "备份工具目录：", "PostgreSQL 工具目录", &a.ToolsDirectory, labelWidth, fieldWidth)
				}
				s.editorInputRow(c, "默认模式：", "默认模式", &a.DefaultSchema, labelWidth, fieldWidth)
				if s.connectionDraft.Kind.IsGaussFamily() {
					ui.Text(c, locales.Text("锁等待使用服务器配置；默认模式在每条物理连接初始化。"))
				} else {
					ui.Text(c, locales.Text("语句及锁等待超时为 0 时不设置超时；默认模式在每条物理连接初始化。"))
				}
			})
		case "SSL":
			theme.Form(c, formWidth, func() {
				theme.Field(c, locales.Text("TLS 模式"), func() {

					connectionFormSelect(c, &s.connectionDraft.TLSMode, []string{"disable", "prefer", "require", "verify-ca", "verify-full"}, product+" TLS 模式", fieldWidth, false)
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
					theme.Field(c, locales.Text(f.name), func() {
						ui.Row(c).Gap(0).FillWidth().AlignItems(ui.Center).Children(func() {

							s.formTextInput(c, f.value, product+" "+f.name, fieldWidth-29, false, false)
							ui.Box(c).Width(connectionFormBrowseGap)
							connectionFormBrowse(c, "选择 "+product+" "+f.name, s.engineFilePicker == nil, func() {
								if s.engineFilePicker != nil {
									s.engineFilePicker(f.kind, -1, s.engineFileVersion)
								}
							})
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
				s.formTextInput(c, &v.File, "附加库文件", width-connectionFormLabelWidth-29, false, false)
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
