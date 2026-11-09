package sqlworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type graphLease interface {
	ExecuteGraph(context.Context, contract.GraphQueryRequest) (contract.GraphResult, error)
}

func (d *Document) cypher() bool      { return d.commandDialect == "neo4j" }
func (d *Document) nativeQuery() bool { return d.nativeCommands || d.cql() || d.cypher() }

// Parameters remain a temporary document draft, separate from persisted query
// text and history. JSON numbers preserve native int64; duplicate keys and
// unsupported, unbounded or ambiguous values are rejected before dispatch.
func graphParameters(source string) (map[string]any, error) {
	if strings.TrimSpace(source) == "" {
		return nil, nil
	}
	if len(source) > 1<<20 || !utf8.ValidString(source) {
		return nil, errors.New(locales.Text("Cypher 参数必须是 1 MiB 以内的 JSON 对象"))
	}
	d := json.NewDecoder(strings.NewReader(source))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 32 {
			return nil, contract.ErrUnsupported
		}
		t, e := d.Token()
		if e != nil {
			return nil, e
		}
		switch v := t.(type) {
		case json.Delim:
			switch v {
			case '{':
				m := map[string]any{}
				for d.More() {
					key, e := d.Token()
					if e != nil {
						return nil, e
					}
					k, ok := key.(string)
					if !ok {
						return nil, contract.ErrUnsupported
					}
					if _, exists := m[k]; exists {
						return nil, contract.ErrUnsupported
					}
					value, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					m[k] = value
				}
				_, e = d.Token()
				return m, e
			case '[':
				values := []any{}
				for d.More() {
					v, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					values = append(values, v)
				}
				_, e = d.Token()
				return values, e
			}
		case json.Number:
			if !strings.ContainsAny(string(v), ".eE") {
				return strconv.ParseInt(string(v), 10, 64)
			}
			f, e := strconv.ParseFloat(string(v), 64)
			if e != nil || math.IsInf(f, 0) || math.IsNaN(f) {
				return nil, contract.ErrUnsupported
			}
			return f, nil
		case nil, string, bool:
			return t, nil
		}
		return nil, contract.ErrUnsupported
	}
	v, e := read(0)
	if e == nil {
		_, e = d.Token()
		if e == io.EOF {
			e = nil
		} else if e == nil {
			e = contract.ErrUnsupported
		}
	}
	m, ok := v.(map[string]any)
	if e != nil || !ok {
		return nil, errors.New(locales.Text("Cypher 参数必须是有效 JSON 对象，键不能重复，整数须在 int64 范围内"))
	}
	return m, nil
}

// Capture the draft and original lease on the UI thread before any worker is
// scheduled. Parameters are deeply constructed and never refer to live UI state.
func (s *State) graphExecutor(d *Document) (func(context.Context, string, string) (contract.SQLResult, error), error) {
	parameters, e := graphParameters(d.graphParameters)
	if e != nil {
		return nil, e
	}
	l, ok := d.lease.(graphLease)
	if !ok {
		return nil, contract.ErrUnsupported
	}
	if v, ok := d.lease.(interface{ SupportsGraph() bool }); ok && !v.SupportsGraph() {
		return nil, contract.ErrUnsupported
	}
	return func(ctx context.Context, database, source string) (contract.SQLResult, error) {
		start := time.Now()
		r, e := l.ExecuteGraph(ctx, contract.GraphQueryRequest{Query: source, Database: database, Parameters: parameters})
		result := graphresult.Project(source, r)
		result.Duration = time.Since(start)
		result.SetLegacyView()
		if e != nil && len(result.Steps) > 0 {
			result.Steps[0].Error = e.Error()
		}
		return result, e
	}, nil
}

func graphSummary(r *contract.GraphResult) string {
	if r == nil {
		return ""
	}
	state := locales.Text("尚未提交")
	if r.Uncertain {
		state = locales.Text("结果不确定")
	} else if r.Completed {
		state = locales.Text("原生执行已确认")
	} else if r.Attempted {
		state = locales.Text("未获得原生完成确认")
	}
	text := fmt.Sprintf("%s · %s · %d", state, r.Summary.Database, len(r.Rows))
	if r.Truncated {
		text += " · " + locales.Text("结果已截断")
	}
	return text
}

func (s *State) graphOptionsView(c *ui.Context, d *Document) {
	if !d.cypher() {
		return
	}
	if ui.Button(c, locales.Text("Cypher 参数")).Clicked() {
		d.graphOptionsOpen = !d.graphOptionsOpen
	}
}
func (s *State) graphParametersView(c *ui.Context, d *Document) {
	if !d.cypher() || !d.graphOptionsOpen {
		return
	}
	ui.Text(c, locales.Text("参数 JSON（仅当前标签，整数保留 int64 精度）")).FontSize(theme.Font(c, 11))
	ui.TextArea(c, &d.graphParameters).Label(locales.Text("Cypher 参数 JSON")).Height(100).ReadOnly(d.Loading())
}
func (s *State) graphResultView(c *ui.Context, d *Document) bool {
	if d.result == nil || d.result.Graph == nil {
		return false
	}
	r := d.result.Graph
	ui.Row(c).Padding(3, 6).Gap(6).Children(func() {
		ui.Text(c, graphSummary(r)).Grow(1).MinWidth(0).SingleLine().Tooltip(fmt.Sprintf("%s\n%s\n%v\n%v", r.Summary.QueryType, r.Summary.Server.Agent, r.Summary.Counters, r.Summary.Bookmarks))
		if ui.Button(c, locales.Text("原生图结果")).Clicked() {
			d.graphNative = !d.graphNative
			d.graphVisualOpen = false
		}
		if ui.Button(c, locales.Text("图形视图")).Clicked() {
			d.graphVisualOpen = !d.graphVisualOpen
			d.graphNative = false
		}
	})
	if d.graphVisualOpen {
		return s.graphVisualView(c, d)
	}
	if !d.graphNative {
		return false
	}
	if d.graphJSON == "" {
		raw, _ := json.MarshalIndent(r, "", "  ")
		if len(raw) > 256<<10 {
			raw = raw[:256<<10]
			for !utf8.Valid(raw) {
				raw = raw[:len(raw)-1]
			}
			raw = append(raw, []byte("\n"+locales.Text("展示限制 256 KiB；完整类型化结果保留在当前文档"))...)
		}
		d.graphJSON = string(raw)
	}
	ui.TextArea(c, &d.graphJSON).Label(locales.Text("Neo4j 原生图结果 JSON")).ReadOnly(true).Fill()
	return true
}
