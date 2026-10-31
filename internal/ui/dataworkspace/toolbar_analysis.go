package dataworkspace

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type mysqlDataFrequency struct {
	Value     string
	Count     int64
	Truncated bool
	Null      bool
}

type mysqlDataColumnAnalysis struct {
	Name, Kind                    string
	Rows, Null, Incomplete        int64
	Unique, Singletons, Numeric   int64
	NumericInvalid                int64
	Collation                     string
	Min, Max, Mean                string
	Median, StdDeviation          string
	Zero, MaxFrequency, ModeCount int64
	Modes                         []mysqlDataFrequency
	Frequencies                   []mysqlDataFrequency
}

// Results identify their page or complete server scope. Page numeric
// comparisons use rationals so large BIGINT/DECIMAL values retain ordering.
type mysqlDataAnalysis struct {
	Database, Table string
	Offset          int64
	Rows            int64
	At              time.Time
	HasDraft        bool
	Columns         []mysqlDataColumnAnalysis
	Scope           string
	FilterCount     int
}

type mysqlDataAnalyzer func(context.Context, int64, string, string, mysqlsvc.MySQLDataOptions, []string) (*mysqlsvc.MySQLDataAnalysis, error)

type mysqlDataAnalysisResult struct {
	Profile             int64
	Generation, Request uint64
	Scope               string
	FilterCount         int
	Result              *mysqlsvc.MySQLDataAnalysis
	Err                 error
}

var mysqlDataAnalysisNumberPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func mysqlDataAnalysisNumber(text string) (*big.Rat, bool) {
	if len(text) > 256 || !mysqlDataAnalysisNumberPattern.MatchString(text) {
		return nil, false
	}
	// Bound exponent allocation even if a custom loader supplied invalid data.
	if at := strings.IndexAny(text, "eE"); at >= 0 {
		exponent, err := strconv.Atoi(text[at+1:])
		if err != nil || exponent < -1000 || exponent > 1000 {
			return nil, false
		}
	}
	number, ok := new(big.Rat).SetString(text)
	return number, ok
}

func mysqlDataAnalyzePage(t *Document) *mysqlDataAnalysis {
	if t == nil || t.closed || t.page == nil || t.loading {
		return nil
	}
	a := &mysqlDataAnalysis{Database: t.database, Table: t.table, Offset: t.offset,
		Rows: int64(len(t.page.Rows)), At: t.page.CompletedAt, HasDraft: t.HasDraft(), Scope: "page"}
	for col, name := range t.page.Columns {
		kind := ""
		if col < len(t.page.ColumnKinds) {
			kind = t.page.ColumnKinds[col]
		}
		if col < len(t.page.ColumnMetadata) && t.page.ColumnMetadata[col].Name == name {
			kind = t.page.ColumnMetadata[col].DataType
		}
		stats := mysqlDataColumnAnalysis{Name: name, Kind: kind, Rows: int64(len(t.page.Rows))}
		frequencies := map[string]int64{}
		var minimum, maximum *big.Rat
		sum := new(big.Rat)
		squares := new(big.Rat)
		var numbers []*big.Rat
		for _, row := range t.page.Rows {
			if col >= len(row) {
				stats.Incomplete++
				continue
			}
			cell := row[col]
			if cell.Null {
				stats.Null++
				continue
			}
			if cell.Truncated {
				stats.Incomplete++
				continue
			}
			frequencies[cell.Text]++
			if mysqlColumnIsNumeric(kind) {
				number, ok := mysqlDataAnalysisNumber(cell.Text)
				if !ok {
					stats.NumericInvalid++
					continue
				}
				stats.Numeric++
				if number.Sign() == 0 {
					stats.Zero++
				}
				numbers = append(numbers, new(big.Rat).Set(number))
				squares.Add(squares, new(big.Rat).Mul(number, number))
				sum.Add(sum, number)
				if minimum == nil || number.Cmp(minimum) < 0 {
					minimum, stats.Min = new(big.Rat).Set(number), cell.Text
				}
				if maximum == nil || number.Cmp(maximum) > 0 {
					maximum, stats.Max = new(big.Rat).Set(number), cell.Text
				}
			}
		}
		stats.Unique = int64(len(frequencies))
		// O-16: Navicat counts NULL as one distinct group, and as a
		// singleton when exactly one row is NULL. Numeric aggregates still
		// exclude NULL, as do SQL COUNT(DISTINCT) and AVG on the service side.
		if stats.Null > 0 {
			stats.Unique++
		}
		if stats.Null == 1 {
			stats.Singletons++
		}
		for value, count := range frequencies {
			if count == 1 {
				stats.Singletons++
			}
			stats.Frequencies = append(stats.Frequencies, mysqlDataFrequency{Value: value, Count: count})
		}
		sort.Slice(stats.Frequencies, func(i, j int) bool {
			left, right := stats.Frequencies[i], stats.Frequencies[j]
			if left.Count != right.Count {
				return left.Count > right.Count
			}
			return left.Value < right.Value
		})
		if len(stats.Frequencies) > 0 {
			stats.MaxFrequency = stats.Frequencies[0].Count
		}
		stats.MaxFrequency = max(stats.MaxFrequency, stats.Null)
		for _, f := range stats.Frequencies {
			if f.Count == stats.MaxFrequency {
				stats.ModeCount++
				if len(stats.Modes) < 10 {
					stats.Modes = append(stats.Modes, f)
				}
			}
		}
		if stats.Null > 0 && stats.Null == stats.MaxFrequency {
			stats.ModeCount++
			stats.Modes = append(stats.Modes, mysqlDataFrequency{Null: true, Count: stats.Null})
		}
		if len(stats.Frequencies) > 10 {
			stats.Frequencies = append([]mysqlDataFrequency(nil), stats.Frequencies[:10]...)
		}
		if stats.Numeric > 0 {
			mean := sum.Quo(sum, big.NewRat(int64(stats.Numeric), 1))
			stats.Mean = mean.FloatString(8)
			sort.Slice(numbers, func(i, j int) bool { return numbers[i].Cmp(numbers[j]) < 0 })
			middle := new(big.Rat).Set(numbers[(len(numbers)-1)/2])
			if len(numbers)%2 == 0 {
				middle.Add(middle, numbers[len(numbers)/2]).Quo(middle, big.NewRat(2, 1))
			}
			stats.Median = middle.FloatString(8)
			variance := new(big.Rat).Quo(squares, big.NewRat(stats.Numeric, 1))
			variance.Sub(variance, new(big.Rat).Mul(mean, mean))
			if variance.Sign() >= 0 {
				deviation := new(big.Float).SetPrec(256).SetRat(variance)
				stats.StdDeviation = deviation.Sqrt(deviation).Text('f', 8)
			}
		}
		a.Columns = append(a.Columns, stats)
	}
	return a
}

func (s *State) openAnalysis(t *Document) {
	if t == nil || t.closed {
		return
	}
	s.closeDataAnalysis(t)
	t.tools.analysisOpen = true
	t.tools.analysisSettings = true
	t.tools.analysisHeaders = true
	t.tools.analysisScope = "all"
	t.tools.analysisErr = ""
}

func (s *State) closeDataAnalysis(t *Document) {
	if t == nil {
		return
	}
	if t.tools.analysisCancel != nil {
		t.tools.analysisCancel()
	}
	t.tools.analysisRequest++
	t.tools.analysisCancel, t.tools.analysisResults = nil, nil
	t.tools.analysisBusy, t.tools.analysisCanceling = false, false
	t.tools.analysisOpen = false
	if t.toolbar.dock == "列统计" {
		t.toolbar.dock = ""
	}
	t.tools.analysis = nil
	t.tools.analysisSettings = false
}

func (s *State) cancelDataAnalysis(t *Document) {
	if t == nil || !t.tools.analysisBusy {
		return
	}
	if t.tools.analysisCancel != nil {
		t.tools.analysisCancel()
	}
	t.tools.analysisCanceling = true
}

func (s *State) startDataAnalysis(services ui.Services, t *Document) {
	if t == nil || t.closed || t.tools.analysisBusy || t.page == nil || t.loading {
		return
	}
	scope := t.tools.analysisScope
	if scope == "page" || scope == "" {
		t.tools.analysis = mysqlDataAnalyzePage(t)
		t.tools.analysisErr = ""
		t.tools.analysisSettings = false
		return
	}
	if scope != "all" && scope != "filtered" {
		return
	}
	session := s.session()
	if session.ProfileID != t.profileID || !session.Connected {
		t.tools.analysisErr = locales.Text("原 MySQL 会话已失效，请重新连接后分析")
		return
	}
	analyzer := s.Analyzer
	if analyzer == nil {
		analyzer = s.runtime().Analyzer
	}
	if analyzer == nil {
		t.tools.analysisErr = locales.Text("完整数据分析服务不可用")
		return
	}
	options := mysqlsvc.MySQLDataOptions{}
	if scope == "filtered" {
		options.Filters, _ = mysqlDataCurrentRules(t)
	}
	fields := append([]string(nil), t.page.Columns...)
	ctx, cancel := context.WithCancel(context.Background())
	t.tools.analysisRequest++
	request := t.tools.analysisRequest
	t.tools.analysisCancel = cancel
	t.tools.analysisBusy, t.tools.analysisCanceling = true, false
	t.tools.analysisErr = ""
	out := make(chan mysqlDataAnalysisResult, 1)
	t.tools.analysisResults = out
	profile, generation, database, table := t.profileID, session.Generation, t.database, t.table
	filterCount := len(options.Filters)
	go func() {
		result, err := analyzer(ctx, profile, database, table, options, fields)
		out <- mysqlDataAnalysisResult{Profile: profile, Generation: generation, Request: request,
			Scope: scope, FilterCount: filterCount, Result: result, Err: err}
		services.Invalidate()
	}()
}

func (s *State) consumeDataAnalysis() {
	session := s.session()
	for _, t := range s.documents {
		if t.closed || t.tools.analysisResults == nil || t.profileID != session.ProfileID {
			continue
		}
		select {
		case result := <-t.tools.analysisResults:
			if result.Profile != t.profileID || result.Request != t.tools.analysisRequest {
				continue
			}
			canceling := t.tools.analysisCanceling
			if t.tools.analysisCancel != nil {
				t.tools.analysisCancel()
			}
			t.tools.analysisCancel, t.tools.analysisResults = nil, nil
			t.tools.analysisBusy, t.tools.analysisCanceling = false, false
			if result.Generation != session.Generation || !session.Connected {
				t.tools.analysisErr = locales.Text("原 MySQL 会话已失效，分析结果已丢弃")
				continue
			}
			if canceling || errors.Is(result.Err, context.Canceled) {
				t.tools.analysisErr = locales.Text("已取消数据分析")
				continue
			}
			if result.Err != nil {
				t.tools.analysisErr = result.Err.Error()
				continue
			}
			if result.Result == nil {
				t.tools.analysisErr = locales.Text("数据库没有返回分析结果")
				continue
			}
			a := &mysqlDataAnalysis{Database: t.database, Table: t.table, Rows: result.Result.Rows,
				At: result.Result.CompletedAt, Scope: result.Scope, FilterCount: result.FilterCount}
			for _, col := range result.Result.Columns {
				stats := mysqlDataColumnAnalysis{Name: col.Name, Kind: col.Kind, Rows: col.Rows,
					Null: col.Null, Unique: col.Distinct, Singletons: col.Singletons, Numeric: col.Numeric,
					Min: col.Min, Max: col.Max, Mean: col.Mean, Collation: col.Collation,
					Median: col.Median, StdDeviation: col.StdDeviation, Zero: col.Zero, MaxFrequency: col.MaxFrequency, ModeCount: col.ModeCount}
				if col.Null > 0 {
					stats.Unique++
				}
				if col.Null == 1 {
					stats.Singletons++
				}
				for _, f := range col.Modes {
					stats.Modes = append(stats.Modes, mysqlDataFrequency{Value: f.Value, Count: f.Count, Truncated: f.Truncated, Null: f.Null})
				}
				for _, f := range col.Distribution {
					stats.Frequencies = append(stats.Frequencies, mysqlDataFrequency{Value: f.Value, Count: f.Count, Truncated: f.Truncated})
				}
				a.Columns = append(a.Columns, stats)
			}
			t.tools.analysis = a
			t.tools.analysisSettings = false
		default:
		}
	}
}

func mysqlDataAnalysisDistribution(stats mysqlDataColumnAnalysis) string {
	parts := make([]string, 0, len(stats.Frequencies))
	for _, frequency := range stats.Frequencies {
		value := strings.ReplaceAll(strings.ReplaceAll(frequency.Value, "\r", "\\r"), "\n", "\\n")
		if value == "" {
			value = locales.Text("空字符串")
		}
		characters := []rune(value)
		if len(characters) > 60 {
			value = string(characters[:60]) + "…"
		} else if frequency.Truncated {
			value += "…"
		}
		parts = append(parts, fmt.Sprintf("%s × %d", value, frequency.Count))
	}
	return strings.Join(parts, " · ")
}

func mysqlDataAnalysisScope(a *mysqlDataAnalysis) string {
	if a.Scope != "page" {
		if a.Scope == "filtered" {
			return fmt.Sprintf(locales.Text("完整已应用筛选结果：%d 个条件，共 %d 行；统计由数据库计算。"), a.FilterCount, a.Rows)
		}
		return fmt.Sprintf(locales.Text("全部记录：共 %d 行；统计由数据库计算。"), a.Rows)
	}
	if a.Rows == 0 {
		return locales.Text("当前已加载页为空；统计不代表整张表。")
	}
	return fmt.Sprintf(locales.Text("仅分析当前已加载页：第 %d–%d 行，共 %d 行；统计不代表整张表。"), a.Offset+1, a.Offset+int64(a.Rows), a.Rows)
}

// Analysis is a table mode: its controls remain beneath the command bar,
// and results accompany the original data rather than replacing it in a modal.
func (s *State) analysisView(c *ui.Context, t *Document) {
	if !t.tools.analysisOpen {
		return
	}
	palette := theme.Colors(c)
	ui.Column(c).FillWidth().Shrink(0).Padding(4, 6).Gap(4).Background(palette.Panel).BorderWidth(0, 0, 1, 0).BorderColor(palette.Border).Children(func() {
		ui.Row(c).FillWidth().MinWidth(0).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.Text("数据分析")).FontSize(theme.Font(c, 12))
			if dataToolbarToggle(c, "设置", "", t.tools.analysisSettings, t.tools.analysisBusy).Clicked() {
				t.tools.analysisSettings = !t.tools.analysisSettings
			}
			ui.Box(c).Grow(1).MinWidth(0)
			if dataToolbarToggle(c, "表头统计", "", t.tools.analysisHeaders, t.tools.analysis == nil).Clicked() {
				t.tools.analysisHeaders = !t.tools.analysisHeaders
			}
			if dataToolbarToggle(c, "列统计", "", t.toolbar.dock == "列统计", t.tools.analysis == nil).Clicked() {
				if t.toolbar.dock == "列统计" {
					s.closeDataDock(t)
				} else {
					s.finishMySQLCellEdit(t, true)
					t.toolbar.dock = "列统计"
					if t.toolbar.dockHeight == 0 {
						t.toolbar.dockHeight = 54
					}
				}
			}
			if ui.Button(c, locales.Text("退出分析")).Clicked() {
				s.closeDataAnalysis(t)
			}
		})
		if t.tools.analysisSettings {
			ui.Row(c).Gap(6).AlignItems(ui.Center).FillWidth().Children(func() {
				mysqlDataRuleSelect(c, &t.tools.analysisScope, []string{"all", "filtered", "page"}, func(scope string) string {
					switch scope {
					case "all":
						return locales.Text("全部记录")
					case "filtered":
						return locales.Text("已应用筛选")
					default:
						return locales.Text("当前页")
					}
				}).Label(locales.Text("分析范围")).Width(140).Disabled(t.tools.analysisBusy)
				if ui.PrimaryButton(c, locales.Text("开始分析")).Disabled(t.tools.analysisBusy || t.loading || t.page == nil).Clicked() {
					s.startDataAnalysis(c.Services(), t)
				}
			})
		}
		if t.tools.analysisBusy {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(14, 14)
				message := locales.Text("正在分析…")
				if t.tools.analysisCanceling {
					message = locales.Text("正在等待数据库确认取消分析…")
				}
				ui.Text(c, message).SingleLine().Grow(1).MinWidth(0)
				if ui.Button(c, locales.Text("取消分析")).Disabled(t.tools.analysisCanceling).Clicked() {
					s.cancelDataAnalysis(t)
				}
			})
		}
		if t.tools.analysisErr != "" {
			ui.Text(c, t.tools.analysisErr).TextColor(palette.Error).SingleLine().Tooltip(t.tools.analysisErr)
		}
		if a := t.tools.analysis; a != nil {
			message := mysqlDataAnalysisScope(a)
			if a.HasDraft {
				message += " " + locales.Text("当前页预览包含未保存更改；这些统计尚未反映数据库中的最终值。")
			}
			ui.Text(c, message).SingleLine().Tooltip(message).FontSize(theme.Font(c, 11)).TextColor(palette.Muted)
		}
	})
}

func mysqlDataAnalysisColumn(t *Document, name string) *mysqlDataColumnAnalysis {
	if t.tools.analysis != nil {
		for i := range t.tools.analysis.Columns {
			if t.tools.analysis.Columns[i].Name == name {
				return &t.tools.analysis.Columns[i]
			}
		}
	}
	return nil
}

func mysqlDataAnalysisPercent(count, rows int64) string {
	if rows <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", float64(count)*100/float64(rows))
}

// This nonselectable sticky section shares the native list's horizontal
// scroll. Widths and visual order come from the same column layout as its
// native title cells. Its key is not a record identity.
func (s *State) analysisHeaders(c *ui.Context, t *Document, columns []ui.TableColumn) {
	ordered := make([]ui.TableColumn, 0, len(columns))
	ordered = append(ordered, columns[0]) // the fixed record selector
	seen := map[string]bool{}
	for _, name := range t.grid.Columns.Order {
		for _, col := range columns[1:] {
			if col.ID == name && !seen[name] {
				ordered = append(ordered, col)
				seen[name] = true
			}
		}
	}
	for _, col := range columns[1:] {
		if !seen[col.ID] {
			ordered = append(ordered, col)
		}
	}
	palette := theme.Colors(c)
	// Table's section rows include native cell padding. Cancel it around
	// the spanning row so each panel starts at the corresponding title edge.
	ui.Row(c).FillWidth().Margin(-c.Theme().Space(1.5), -c.Theme().Space(2.5)).Gap(0).AlignItems(ui.Stretch).Children(func() {
		for _, col := range ordered {
			width := col.Width
			if saved, ok := t.grid.Columns.Widths[col.ID]; ok && !col.Fixed {
				width = saved
			}
			if col.MaxWidth > 0 {
				width = min(width, col.MaxWidth)
			}
			width = max(width, col.MinWidth, 16)
			panel := ui.Column(c.Key(col.ID)).Width(width).Shrink(0).Padding(7, 8).Gap(4).FontWeight(400).BorderWidth(0, 1, 1, 0).BorderColor(palette.Border).Background(palette.Panel)
			stats := mysqlDataAnalysisColumn(t, col.ID)
			panel.Children(func() {
				if stats == nil {
					return
				}
				fraction := float32(0)
				if stats.Rows > 0 {
					fraction = float32(stats.Rows-stats.Null) / float32(stats.Rows)
				}
				ui.Row(c).Height(6).FillWidth().Gap(0).Children(func() {
					if fraction > 0 {
						ui.Box(c).Grow(fraction).Height(6).Background(palette.Blue)
					}
					if fraction < 1 {
						ui.Box(c).Grow(1 - fraction).Height(6).Background(palette.Muted)
					}
				})
				for _, entry := range []struct {
					label string
					count int64
				}{{locales.Text("不是 Null"), stats.Rows - stats.Null}, {"NULL", stats.Null}} {
					ui.Row(c).FillWidth().Children(func() {
						ui.Text(c, entry.label).SingleLine().Grow(1).MinWidth(0).FontSize(theme.Font(c, 10))
						ui.Text(c, mysqlDataAnalysisPercent(entry.count, stats.Rows)).FontSize(theme.Font(c, 10))
					})
				}
				s.analysisBars(c, *stats, 70, false)
				text := fmt.Sprintf(locales.Text("%d 非重复, %d 唯一"), stats.Unique, stats.Singletons)
				ui.Text(c, text).SingleLine().Tooltip(text).FontSize(theme.Font(c, 10))
			})
			if stats != nil && panel.Label(locales.Text("列统计")+": "+col.ID).Clicked() {
				s.finishMySQLCellEdit(t, true)
				t.grid.Cells = ui.ColumnRange{Anchor: col.ID, Cursor: col.ID}
			}
		}
	})
}

func (s *State) analysisBars(c *ui.Context, stats mysqlDataColumnAnalysis, height float32, labels bool) {
	palette := theme.Colors(c)
	frequencies := append([]mysqlDataFrequency(nil), stats.Frequencies...)
	if stats.Null > 0 {
		frequencies = append(frequencies, mysqlDataFrequency{Value: "(NULL)", Count: stats.Null, Null: true})
	}
	var largest int64
	for _, f := range frequencies {
		largest = max(largest, f.Count)
	}
	ui.Row(c).FillWidth().Height(height).Gap(3).AlignItems(ui.End).Children(func() {
		for i, frequency := range frequencies {
			bar := ui.Column(c.Key(i)).Width(7).Shrink(0).Gap(3).Justify(ui.End)
			if labels {
				bar.Width(36).Shrink(1).MinWidth(0)
			}
			bar.Children(func() {
				barHeight := height
				if labels {
					barHeight -= 20
				}
				if largest > 0 {
					barHeight *= float32(float64(frequency.Count) / float64(largest))
				}
				label := mysqlDataAnalysisDistribution(mysqlDataColumnAnalysis{Frequencies: []mysqlDataFrequency{frequency}})
				ui.Box(c).FillWidth().Height(max(1, barHeight)).Background(palette.Chart).Tooltip(label)
				if labels {
					ui.Text(c, frequency.Value).SingleLine().Tooltip(label).FontSize(theme.Font(c, 10)).FillWidth()
				}
			})
		}
	})
}

func (s *State) dataAnalysisDock(c *ui.Context, t *Document) {
	if t.tools.analysis == nil {
		return
	}
	name := t.grid.Cells.Cursor
	stats := mysqlDataAnalysisColumn(t, name)
	if stats == nil && t.page != nil {
		for _, field := range t.page.Columns {
			if !t.hiddenColumns[field] {
				stats = mysqlDataAnalysisColumn(t, field)
				if stats != nil {
					break
				}
			}
		}
	}
	if stats == nil {
		return
	}
	ui.Row(c).Fill().MinHeight(0).Padding(8).Gap(18).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(260).MaxWidthPercent(44).MinWidth(100).Gap(6).Children(func() {
			ui.Text(c, stats.Name).Bold().Label(locales.Text("分析字段"))
			ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
				ui.Column(c).Gap(4).FillWidth().Children(func() {
					values := [][2]string{
						{locales.Text("计数"), fmt.Sprint(stats.Rows)},
						{"NULL", fmt.Sprintf("%d · %s", stats.Null, mysqlDataAnalysisPercent(stats.Null, stats.Rows))},
						{locales.Text("不是 Null"), fmt.Sprintf("%d · %s", stats.Rows-stats.Null, mysqlDataAnalysisPercent(stats.Rows-stats.Null, stats.Rows))},
						{locales.Text("非重复"), fmt.Sprint(stats.Unique)}, {locales.Text("唯一"), fmt.Sprint(stats.Singletons)},
						{locales.Text("最高出现次数"), mysqlDataAnalysisModes(*stats)},
					}
					if mysqlColumnIsNumeric(stats.Kind) {
						values = append(values, [2]string{locales.Text("零"), fmt.Sprint(stats.Zero)}, [2]string{locales.Text("最小值"), stats.Min}, [2]string{locales.Text("最大值"), stats.Max}, [2]string{locales.Text("平均值（约）"), stats.Mean}, [2]string{locales.Text("中值"), stats.Median}, [2]string{locales.Text("标准差"), mysqlDataAnalysisRounded(stats.StdDeviation, 2)})
					}
					if stats.Incomplete > 0 {
						values = append(values, [2]string{locales.Text("截断/缺失"), fmt.Sprint(stats.Incomplete)})
					}
					for _, value := range values {
						ui.Row(c).FillWidth().Children(func() {
							ui.Text(c, value[0]).Grow(1).MinWidth(0).SingleLine().FontSize(theme.Font(c, 12))
							ui.Text(c, mysqlDataAnalysisDisplay(value[1])).SingleLine().MaxWidthPercent(64).MinWidth(0).Shrink(1).Tooltip(mysqlDataAnalysisDisplay(value[1])).FontSize(theme.Font(c, 12))
						})
					}
				})
			})
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(8).Children(func() {
			ui.Text(c, locales.Text("值分布（最多 10 项）")).FontSize(theme.Font(c, 12))
			ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() { s.analysisBars(c, *stats, 110, true) })
		})
	})
}

func mysqlDataAnalysisDisplay(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func mysqlDataAnalysisRounded(value string, places int) string {
	if number, ok := mysqlDataAnalysisNumber(value); ok {
		return number.FloatString(places)
	}
	return value
}

func mysqlDataAnalysisModes(stats mysqlDataColumnAnalysis) string {
	var values []string
	for _, f := range stats.Modes {
		value := f.Value
		if f.Null {
			value = "(NULL)"
		} else if value == "" {
			value = locales.Text("空字符串")
		}
		value = strings.ReplaceAll(strings.ReplaceAll(value, "\r", "\\r"), "\n", "\\n")
		if runes := []rune(value); len(runes) > 60 {
			value = string(runes[:60]) + "…"
		} else if f.Truncated {
			value += "…"
		}
		values = append(values, value)
	}
	if remaining := stats.ModeCount - int64(len(stats.Modes)); remaining > 0 {
		values = append(values, fmt.Sprintf(locales.Text("另有 %d 项"), remaining))
	}
	return strings.Join(values, ", ")
}
