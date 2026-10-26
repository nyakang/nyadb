package shell

import (
	"fmt"
	"net"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// The object inspector keeps its mode per data tab. Selecting a grid cell does
// not replace table statistics; field preview is an explicit action.
func (s *State) mysqlDataObjectInspector(c *ui.Context, t *tab) {
	if !t.dataInfo.attempted {
		s.loadMySQLTableInfo(c.Services(), t, false)
	}
	if t.dataInfo.fieldMode {
		s.mysqlDataInspector(c, t)
		return
	}
	s.mysqlTableInfoInspector(c, t)
}

func (s *State) mysqlDataInspectorHeader(c *ui.Context, t *tab, fieldMode bool) {
	ui.Row(c).Label(locales.Text("数据信息切换栏")).Height(42).Padding(1, 5).Gap(3).AlignItems(ui.Center).Children(func() {
		ui.Toolbar(c, func() {
			for _, item := range []struct {
				label string
				field bool
			}{{"表信息", false}, {"字段信息", true}} {
				button := ui.Button(c, locales.Text(item.label)).Height(theme.ControlHeight).FontSize(theme.SmallFontSize).Padding(2, 4)
				if item.field == fieldMode {
					button.Background(theme.ToolbarSelected)
				}
				if button.Clicked() {
					if item.field {
						s.showMySQLFieldInspector(t)
					} else {
						t.dataInfo.fieldMode = false
						s.cancelMySQLChunk(t)
						if !t.dataInfo.attempted {
							s.loadMySQLTableInfo(c.Services(), t, false)
						}
					}
				}
			}
		}).Grow(1).MinWidth(0).Padding(0).Gap(3)
		if ui.ButtonBase(c).Label(locales.Text("隐藏信息面板")).Tooltip(locales.Text("隐藏信息面板")).Size(20, 20).
			Children(func() { theme.Icon(c, "close", 12, theme.Muted) }).Clicked() {
			s.inspectorOn = false
		}
	})
}

func (s *State) mysqlTableInfoInspector(c *ui.Context, t *tab) {
	d := &t.dataInfo
	connected := t.dataProfileID == s.mysqlSessionProfileID && s.mysqlSessionProfileID > 0 && s.mysqlSessionStatus == "已连接"
	ui.Column(c).Label(locales.Text("数据表信息面板")).Fill().Background(theme.Panel).Children(func() {
		s.mysqlDataInspectorHeader(c, t, false)
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("刷新")).Label(locales.Text("刷新表信息")).Height(theme.ControlHeight).
				Disabled(!connected || d.loading).Clicked() {
				s.loadMySQLTableInfo(c.Services(), t, true)
			}
			if d.countLoading {
				if ui.Button(c, locales.Text("取消计数")).Height(theme.ControlHeight).Clicked() {
					s.cancelMySQLTableCount(t)
					d.countErr = locales.Text("行数统计已取消")
				}
			} else if ui.Button(c, locales.Text("获取行数")).Label(locales.Text("获取整表行数")).
				Height(theme.ControlHeight).Disabled(!connected).Clicked() {
				s.loadMySQLTableCount(c.Services(), t)
			}
		}).Height(theme.CommandBarHeight).FillWidth().Padding(1, 4).Gap(3)
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Padding(12, 10).Gap(9).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					icon := "table"
					if d.info != nil && d.info.Kind == "VIEW" {
						icon = "view"
					}
					theme.Icon(c, icon, 26, theme.Blue)
					ui.Text(c, t.dataTable).Bold().FontSize(14).SingleLine().Grow(1).MinWidth(0).Tooltip(t.dataTable)
				})
				for _, p := range s.profileEntries {
					if p.ID != t.dataProfileID {
						continue
					}
					mysqlTableInfoProperty(c, "连接", p.Name)
					if active, err := connections.ResolveActiveSettings(p); err == nil {
						mysqlTableInfoProperty(c, "主机", net.JoinHostPort(active.Host, strconv.Itoa(int(active.Port))))
					}
					break
				}
				mysqlTableInfoProperty(c, "数据库", t.dataDatabase)
				ui.Divider(c)
				if d.loading {
					ui.Text(c, locales.Text("正在读取表信息…")).FontSize(11).TextColor(theme.Muted)
				}
				if d.err != "" {
					ui.Text(c, locales.Text("表信息读取失败：")+d.err).FontSize(11).TextColor(theme.Gold)
					if d.info != nil {
						ui.Text(c, locales.Text("显示上次读取的表信息")).FontSize(11).TextColor(theme.Muted)
					}
				}
				if d.info == nil && !d.loading && d.err == "" {
					ui.Text(c, locales.Text("尚未读取表信息")).FontSize(11).TextColor(theme.Muted)
				}
				estimate := "—"
				if d.info != nil && d.info.EstimatedRows != nil {
					estimate = strconv.FormatInt(*d.info.EstimatedRows, 10)
				}
				mysqlTableInfoProperty(c, "估算行数", estimate)
				if d.count != nil {
					mysqlTableInfoProperty(c, "整表行数", strconv.FormatInt(*d.count, 10))
					mysqlTableInfoProperty(c, "行数统计时间", d.countAt.Local().Format("2006-01-02 15:04:05"))
				}
				if d.countLoading {
					ui.Text(c, locales.Text("正在统计整表行数…")).FontSize(11).TextColor(theme.Muted)
				}
				if d.countErr != "" {
					ui.Text(c, locales.Text("行数统计：")+d.countErr).FontSize(11).TextColor(theme.Gold)
				}
				ui.Text(c, locales.Text("行数统计不应用当前筛选条件")).FontSize(11).TextColor(theme.Muted)
				if d.info == nil {
					return
				}
				info := d.info
				kind := "表"
				if info.Kind == "VIEW" {
					kind = "视图"
				}
				mysqlTableInfoProperty(c, "类型", locales.Text(kind))
				mysqlTableInfoProperty(c, "引擎", info.Engine)
				mysqlTableInfoProperty(c, "自动递增值", mysqlTableInfoNumber(info.AutoIncrement))
				mysqlTableInfoProperty(c, "行格式", info.RowFormat)
				mysqlTableInfoProperty(c, "修改日期", info.ModifiedAt)
				mysqlTableInfoProperty(c, "创建日期", info.CreatedAt)
				mysqlTableInfoProperty(c, "检查时间", info.CheckedAt)
				mysqlTableInfoProperty(c, "索引长度", mysqlTableInfoBytes(info.IndexLength))
				mysqlTableInfoProperty(c, "数据长度", mysqlTableInfoBytes(info.DataLength))
				mysqlTableInfoProperty(c, "最大数据长度", mysqlTableInfoBytes(info.MaxDataLength))
				mysqlTableInfoProperty(c, "数据可用空间", mysqlTableInfoBytes(info.DataFree))
				mysqlTableInfoProperty(c, "排序规则", info.Collation)
				mysqlTableInfoProperty(c, "创建选项", info.CreateOptions)
				mysqlTableInfoProperty(c, "注释", info.Comment)
			})
		})
	})
}

func mysqlTableInfoProperty(c *ui.Context, label, value string) {
	if value == "" {
		value = "—"
	}
	mysqlInspectorProperty(c, locales.Text(label), value)
}

func mysqlTableInfoNumber(value *int64) string {
	if value == nil {
		return "—"
	}
	return strconv.FormatInt(*value, 10)
}

func mysqlTableInfoBytes(value *int64) string {
	if value == nil {
		return "—"
	}
	if *value < 1024 {
		return fmt.Sprintf("%d B", *value)
	}
	amount := float64(*value)
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	i := 0
	for amount >= 1024 && i < len(units)-1 {
		amount /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s (%d B)", amount, units[i], *value)
}
