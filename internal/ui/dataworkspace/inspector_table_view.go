package dataworkspace

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
func (s *inspectorState) View(c *ui.Context, t *Panel) {
	if t == nil || t.closed {
		return
	}
	if !t.info.attempted {
		s.LoadInfo(t, c.Services().Invalidate, false)
	}
	if t.info.fieldMode {
		s.FieldView(c, t)
		return
	}
	s.TableView(c, t)
}

func (s *inspectorState) header(c *ui.Context, t *Panel, fieldMode bool) {
	palette := theme.Colors(c)

	ui.Row(c).Label(locales.Text("数据信息切换栏")).Height(42).Padding(1, 5).Gap(3).AlignItems(ui.Center).Children(func() {
		ui.Toolbar(c, func() {
			for _, item := range []struct {
				label string
				field bool
			}{{"表信息", false}, {"字段信息", true}} {
				button := ui.Button(c, locales.Text(item.label)).Height(theme.RowHeight(c, theme.ControlHeight)).FontSize(theme.Font(c, theme.SmallFontSize)).Padding(2, 4)
				if item.field == fieldMode {
					button.Background(palette.ToolbarSelected)
				}
				if button.Clicked() {
					if item.field {
						s.ShowField(t)
					} else {
						t.info.fieldMode = false
						s.CancelChunk(t)
						if !t.info.attempted {
							s.LoadInfo(t, c.Services().Invalidate, false)
						}
					}
				}
			}
		}).Grow(1).MinWidth(0).Padding(0).Gap(3)
		if ui.ButtonBase(c).Label(locales.Text("隐藏信息面板")).Tooltip(locales.Text("隐藏信息面板")).Size(20, 20).
			Children(func() { theme.Icon(c, "close", 12, palette.Muted) }).Clicked() {
			if s.deps.Hide != nil {
				s.deps.Hide()
			}
		}
	})
}

func (s *inspectorState) TableView(c *ui.Context, t *Panel) {
	palette := theme.Colors(c)

	d := &t.info
	connected := s.connected(t)
	ui.Column(c).Label(locales.Text("数据表信息面板")).Fill().Background(palette.Panel).Children(func() {
		s.header(c, t, false)
		ui.Toolbar(c, func() {
			if ui.Button(c, locales.Text("刷新")).Label(locales.Text("刷新表信息")).Height(theme.RowHeight(c, theme.ControlHeight)).
				Disabled(!connected || d.loading).Clicked() {
				s.LoadInfo(t, c.Services().Invalidate, true)
			}
			if d.countLoading {
				if ui.Button(c, locales.Text("取消计数")).Height(theme.RowHeight(c, theme.ControlHeight)).Clicked() {
					s.CancelCount(t)
					d.countErr = locales.Text("行数统计已取消")
				}
			} else if ui.Button(c, locales.Text("获取行数")).Label(locales.Text("获取整表行数")).
				Height(theme.RowHeight(c, theme.ControlHeight)).Disabled(!connected).Clicked() {
				s.LoadCount(t, c.Services().Invalidate)
			}
		}).Height(theme.RowHeight(c, theme.CommandBarHeight)).FillWidth().Padding(1, 4).Gap(3)
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Padding(12, 10).Gap(9).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					icon := "table"
					if d.info != nil && d.info.Kind == "VIEW" {
						icon = "view"
					}
					theme.Icon(c, icon, 26, palette.Blue)
					ui.Text(c, t.table).Bold().FontSize(theme.Font(c, 14)).SingleLine().Grow(1).MinWidth(0).Tooltip(t.table)
				})
				if s.deps.Profile != nil {
					if p, ok := s.deps.Profile(t.profileID); ok {
						tableProperty(c, "连接", p.Name)
						if active, err := connections.ResolveActiveSettings(p); err == nil {
							tableProperty(c, "主机", net.JoinHostPort(active.Host, strconv.Itoa(int(active.Port))))
						}
					}
				}
				tableProperty(c, "数据库", t.database)
				ui.Divider(c)
				if d.loading {
					ui.Text(c, locales.Text("正在读取表信息…")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
				}
				if d.err != "" {
					ui.Text(c, locales.Text("表信息读取失败：")+d.err).FontSize(theme.Font(c, 11)).TextColor(palette.Error)
					if d.info != nil {
						ui.Text(c, locales.Text("显示上次读取的表信息")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
					}
				}
				if d.info == nil && !d.loading && d.err == "" {
					ui.Text(c, locales.Text("尚未读取表信息")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
				}
				estimate := "—"
				if d.info != nil && d.info.EstimatedRows != nil {
					estimate = strconv.FormatInt(*d.info.EstimatedRows, 10)
				}
				tableProperty(c, "估算行数", estimate)
				if d.count != nil {
					tableProperty(c, "整表行数", strconv.FormatInt(*d.count, 10))
					tableProperty(c, "行数统计时间", d.countAt.Local().Format("2006-01-02 15:04:05"))
				}
				if d.countLoading {
					ui.Text(c, locales.Text("正在统计整表行数…")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
				}
				if d.countErr != "" {
					ui.Text(c, locales.Text("行数统计：")+d.countErr).FontSize(theme.Font(c, 11)).TextColor(palette.Error)
				}
				ui.Text(c, locales.Text("行数统计不应用当前筛选条件")).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
				if d.info == nil {
					return
				}
				info := d.info
				kind := "表"
				if info.Kind == "VIEW" {
					kind = "视图"
				}
				tableProperty(c, "类型", locales.Text(kind))
				tableProperty(c, "引擎", info.Engine)
				tableProperty(c, "自动递增值", tableNumber(info.AutoIncrement))
				tableProperty(c, "行格式", info.RowFormat)
				tableProperty(c, "修改日期", info.ModifiedAt)
				tableProperty(c, "创建日期", info.CreatedAt)
				tableProperty(c, "检查时间", info.CheckedAt)
				tableProperty(c, "索引长度", tableBytes(info.IndexLength))
				tableProperty(c, "数据长度", tableBytes(info.DataLength))
				tableProperty(c, "最大数据长度", tableBytes(info.MaxDataLength))
				tableProperty(c, "数据可用空间", tableBytes(info.DataFree))
				tableProperty(c, "排序规则", info.Collation)
				tableProperty(c, "创建选项", info.CreateOptions)
				tableProperty(c, "注释", info.Comment)
			})
		})
	})
}

func tableProperty(c *ui.Context, label, value string) {
	if value == "" {
		value = "—"
	}
	theme.Property(c, locales.Text(label), value)
}

func tableNumber(value *int64) string {
	if value == nil {
		return "—"
	}
	return strconv.FormatInt(*value, 10)
}

func tableBytes(value *int64) string {
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
