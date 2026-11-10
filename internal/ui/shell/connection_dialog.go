package shell

import (
	"fmt"
	"reflect"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/connectioncatalog"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func (s *State) openConnectionDialog() {
	s.connectionEditor.Close()
	s.connectionDialog = true
	s.connectionStep = 0
	s.connectionKind = ""
	s.connectionCatalog.Reset()
	s.connectionGroup = s.navigation.Group
	if s.connectionGroup == "" {
		if selected, ok := s.currentProfile(); ok {
			s.connectionGroup = selected.GroupName
		}
	}
	*s.connectionEditor.Draft() = connections.Profile{GroupName: s.connectionGroup}
	s.connectionError = ""
	if s.showConnectionWindow != nil {
		s.showConnectionWindow()
	}
}

func (s *State) closeConnectionDialog() {
	s.connectionEditor.Close()
	s.connectionDialog = false
	s.connectionError = ""
	if s.hideConnectionWindow != nil {
		s.hideConnectionWindow()
	}
}

func (s *State) chooseConnectionKind(kind connections.Kind) {
	s.connectionKind = kind
	s.connectionError = ""
}

func (s *State) nextConnectionStep() {
	if s.connectionKind == "" {
		if choice := s.connectionCatalog.Selected(); choice.Name == "" || choice.Kind != "" {
			s.connectionError = locales.Text("请先选择数据库类型")
		}
		return
	}
	if err := s.connectionEditor.OpenNew(s.connectionKind, s.connectionGroup); err != nil {
		s.connectionError = err.Error()
		return
	}
	s.connectionError = ""
	s.connectionStep = 1
	recent := s.connectionCatalog.Selected().Name
	if recent == "" {
		recent = connectionKindName(s.connectionKind)
	}
	s.connectionCatalog.RecordRecent(recent)
	s.updateConnectionWindowTitle()
}

func (s *State) connectionSaved(saved connections.Profile) {
	presentationOnly := false
	for _, old := range s.navigation.Profiles() {
		if old.ID == saved.ID {
			old.Color = saved.Color
			old.UpdatedAt = saved.UpdatedAt
			presentationOnly = reflect.DeepEqual(old, saved)
			break
		}
	}
	if !presentationOnly && s.sessions.Current().ProfileID == saved.ID {
		s.disconnectMySQLSession()
	}
	s.selectedProfileID = saved.ID
	s.loadProfiles()
	s.connectionDialog = false
	s.connectionError = ""
	if s.hideConnectionWindow != nil {
		s.hideConnectionWindow()
	}
	s.status = "已保存连接：" + saved.Name + "（未建立数据库会话）"
}

func (s *State) backToConnectionCatalog() {
	s.connectionStep = 0
	s.connectionError = ""
	s.updateConnectionWindowTitle()
}

func (s *State) connectionEditorView(c *ui.Context) {
	width, height := c.Size()
	if s.showConnectionWindow == nil {
		width = min(float32(760), width-96)
		height = min(float32(630), height-96)
	}
	if s.connectionEditor.URIActive() {
		s.connectionEditor.URIView(c, width, height, func() {
			s.connectionKind = s.connectionEditor.Draft().Kind
			s.connectionStep = 1
			s.updateConnectionWindowTitle()
		})
		return
	}
	s.connectionEditor.View(c, width, height)
}

// connectionModal is a native MyGo modal, not a WebView or imported UI asset.
func (s *State) connectionModal(c *ui.Context) {
	if !s.connectionDialog || s.showConnectionWindow != nil {
		return
	}
	theme.Modal(c, &s.connectionDialog, func() {
		if s.connectionStep == 0 {
			s.connectionTypes(c, false)
			return
		}
		s.connectionEditorView(c)
	})
}

func connectionKindName(kind connections.Kind) string {
	switch kind {
	case connections.Etcd:
		return "etcd"
	case connections.Qdrant:
		return "Qdrant"
	case connections.DuckDB:
		return "DuckDB"
	case connections.MySQL:
		return "MySQL"
	case connections.MariaDB:
		return "MariaDB"
	case connections.OceanBase:
		return "OceanBase"
	case connections.TiDB:
		return "TiDB"
	case connections.Elasticsearch:
		return "Elasticsearch"
	case connections.Easysearch:
		return "Easysearch"
	case connections.Neo4j:
		return "Neo4j"
	case connections.Cassandra:
		return "Apache Cassandra"
	case connections.Meilisearch:
		return "Meilisearch"
	case connections.CouchDB:
		return "Apache CouchDB"
	case connections.Solr:
		return "Apache Solr"
	case connections.Redis:
		return "Redis"
	case connections.MongoDB:
		return "MongoDB"
	case connections.Dameng:
		return "达梦"
	case connections.Oracle:
		return "Oracle"
	case connections.SQLServer:
		return "SQL Server"
	case connections.CockroachDB:
		return "CockroachDB"
	case connections.OpenGauss:
		return "openGauss"
	case connections.GaussDB:
		return "GaussDB"
	case connections.Kingbase:
		return "KingbaseES"
	case connections.PostgreSQL:
		return "PostgreSQL"
	case connections.SQLite:
		return "SQLite"
	case connections.Trino:
		return "Trino"
	case connections.StarRocks:
		return "StarRocks"
	case connections.Doris:
		return "Apache Doris"
	case connections.ClickHouse:
		return "ClickHouse"
	default:
		return "未知类型"
	}
}

// LoadLocalResearchIcons preserves bootstrap's opt-in research entry point.
func (s *State) LoadLocalResearchIcons(dir string) (int, error) {
	return s.connectionCatalog.LoadLocalResearchIcons(dir)
}

func (s *State) chooseCatalogChoice(choice connectioncatalog.Choice) {
	if choice.Kind == "" {
		s.connectionKind = ""
		s.connectionError = fmt.Sprintf(locales.Text("%s 的连接驱动尚未支持"), choice.Name)
		return
	}
	s.chooseConnectionKind(choice.Kind)
}

const catalogFooterHeight float32 = 42
const connectionFooterControlHeight float32 = 24

// The chooser follows the observed large two-pane information architecture.
// It uses independent NyaDB symbols by default. In explicit local research
// mode only, runtime-loaded PNGs may replace catalog symbols for comparison.
func (s *State) connectionTypes(c *ui.Context, nativeWindow bool) {
	palette := theme.Colors(c)

	w, h := c.Size()
	dialogWidth := min(float32(860), w-72)
	dialogHeight := min(float32(648), h-76)
	footerHeight := theme.RowHeight(c, catalogFooterHeight)
	bodyHeight := dialogHeight - footerHeight - 40
	if nativeWindow {
		dialogWidth = w
		dialogHeight = h
		bodyHeight = h - footerHeight - 1
	}
	ui.Column(c).Width(dialogWidth).Height(dialogHeight).Gap(0).Children(func() {
		if !nativeWindow {
			ui.Row(c).Height(38).Padding(0, 3).AlignItems(ui.Center).Children(func() {
				theme.Icon(c, "connection", 20, palette.Blue)
				ui.Text(c, locales.Text("新建连接")).FontSize(theme.Font(c, 17)).Bold()
			})
			ui.Box(c).Height(1).Background(palette.Border)
		}
		s.connectionCatalog.SetEditing(&s.connectionEditFocus, func(target theme.EditTarget) { s.connectionEditTarget = target }, func() bool { return s.connectionDialog && s.connectionStep == 0 })
		s.connectionCatalog.View(c, dialogWidth, bodyHeight, s.chooseCatalogChoice)
		ui.Box(c).Height(1).Background(palette.Border)
		ui.Row(c).Height(footerHeight).Padding(5, 9).Gap(7).AlignItems(ui.Center).Background(palette.Toolbar).Children(func() {
			if ui.Button(c, locales.Text("通过 URI 新建连接…")).Height(theme.RowHeight(c, connectionFooterControlHeight)).
				Label(locales.Text("通过 URI 新建连接…")).Clicked() {
				s.connectionEditor.BeginURI(s.connectionGroup)
				s.connectionStep = 2
				s.updateConnectionWindowTitle()
			}
			ui.Box(c).Grow(1)
			if s.connectionError != "" {
				ui.Text(c, s.connectionError).FontSize(theme.Font(c, 11)).TextColor(palette.Error).SingleLine().MinWidth(0).Grow(1)
			}
			if ui.Button(c, locales.Text("取消")).Label(locales.Text("取消连接向导")).MinWidth(theme.Font(c, 80)).Height(theme.RowHeight(c, connectionFooterControlHeight)).Clicked() {
				s.closeConnectionDialog()
			}
			if ui.Button(c, locales.Text("下一步")).Label(locales.Text("下一步")).MinWidth(theme.Font(c, 80)).Height(theme.RowHeight(c, connectionFooterControlHeight)).Disabled(s.connectionKind == "").Clicked() {
				s.nextConnectionStep()
			}
		})
	})
}
