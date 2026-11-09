package sqlworkspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
)

func nativePlanJSON(d *Document) (string, error) {
	if d == nil || d.closed || d.result == nil || d.result.Graph == nil || d.result.Graph.Summary.Plan == nil {
		return "", contract.ErrUnsupported
	}
	raw, err := json.Marshal(struct {
		Namespace contract.NamespaceRef
		Summary   contract.GraphSummary
	}{d.Namespace(), d.result.Graph.Summary})
	if err != nil || len(raw) > 16<<20 {
		return "", errors.New(locales.Text("原生计划导出超过 16 MiB 限制"))
	}
	return string(raw), nil
}

func (s *State) graphPlanNavigation(c *ui.Context, d *Document) {
	if d.plan == nil && d.planError == "" {
		return
	}
	if d.result != nil && d.result.Graph != nil {
		r := d.result.Graph
		mode := ""
		if r.Summary.Plan != nil {
			mode = strings.ToUpper(r.Summary.Plan.Mode) + " · "
		}
		ui.Text(c, mode+graphSummary(r)).Padding(3, 8)
	}
	ui.Row(c).Wrap().Gap(6).Padding(3, 8).Children(func() {
		if ui.Button(c, locales.Text("结构计划")).Disabled(d.plan == nil).Clicked() {
			d.planVisible, d.graphPlanNative = true, false
		}
		if ui.Button(c, locales.Text("原生计划")).Disabled(d.result == nil || d.result.Graph == nil || d.result.Graph.Summary.Plan == nil).Clicked() {
			d.planVisible, d.graphPlanNative = false, true
		}
		if ui.Button(c, locales.Text("查询结果")).Clicked() {
			d.planVisible, d.graphPlanNative = false, false
		}
	})
	if d.planError != "" {
		ui.Text(c, locales.Text("原生计划无法完整显示；原执行回执和查询结果仍保留")+": "+d.planError).Padding(3, 8)
	}
}

func (s *State) graphPlanNativeView(c *ui.Context, d *Document) {
	raw, err := nativePlanJSON(d)
	if err != nil {
		ui.Text(c, err.Error())
		return
	}
	ui.Column(c).Fill().Gap(6).Padding(8).Children(func() {
		ui.Text(c, locales.Text("计划保留原执行的查询、参数和回执；查看、复制或导出不会执行查询。"))
		ui.Row(c).Wrap().Gap(6).Children(func() {
			if ui.Button(c, locales.Text("复制完整原生计划")).Clicked() {
				c.WriteClipboard(raw)
			}
			if ui.Button(c, locales.Text("导出完整原生计划")).Disabled(s.persistence.files.SavePath == nil || s.persistence.files.Write == nil).Clicked() {
				s.exportNativePlan(d)
			}
		})
		if d.graphPlanJSON == "" {
			d.graphPlanJSON = raw
			if len(raw) > 256<<10 {
				d.graphPlanJSON = raw[:256<<10]
				for !utf8.ValidString(d.graphPlanJSON) {
					d.graphPlanJSON = d.graphPlanJSON[:len(d.graphPlanJSON)-1]
				}
				d.graphPlanJSON += "\n" + locales.Text("展示限制 256 KiB；复制或导出保留完整原生计划")
			}
		}
		ui.TextArea(c, &d.graphPlanJSON).Label(locales.Text("原生计划 JSON")).ReadOnly(true).Fill()
	})
}

func (s *State) exportNativePlan(d *Document) {
	raw, err := nativePlanJSON(d)
	if err != nil {
		d.err = err.Error()
		return
	}
	f := s.persistence.files
	if f.SavePath == nil || f.Write == nil {
		return
	}
	name := d.Name() + ".plan.json"
	s.startIO(d, nil, func() ioResult {
		path, err := f.SavePath(name)
		if err == nil && path != "" {
			err = f.Write(path, raw)
		}
		return ioResult{kind: "export", err: err}
	})
}

func nativeMetricText(metric contract.PlanMetric) string {
	if metric.Value == nil {
		return locales.Text("未知")
	}
	if metric.Unit == "ns" {
		value, err := strconv.ParseInt(metric.Value.Text, 10, 64)
		if err == nil {
			return time.Duration(value).String()
		}
	}
	return metric.Value.Text
}

func nativeMetricLabel(name string) string {
	for _, v := range []struct{ name, label string }{{"actual_rows", "实际行数"}, {"db_hits", "数据库命中"}, {"page_cache_hits", "页缓存命中"}, {"page_cache_misses", "页缓存未命中"}, {"page_cache_ratio", "页缓存命中率"}, {"time", "原生耗时"}} {
		if v.name == name {
			return locales.Text(v.label)
		}
	}
	return fmt.Sprint(name)
}
