package dataworkspace

import (
	"encoding/base64"
	"encoding/json"
	"github.com/egoist/mygo/ui"
	"math"
	"time"
)

type engineLayout struct {
	Version int                `json:"version"`
	Order   []string           `json:"order,omitempty"`
	Widths  map[string]float32 `json:"widths,omitempty"`
}
type engineLayoutState struct {
	restored, failed bool
	saved, observed  string
	changedAt        time.Time
}

func (s *EngineState) SetLayoutPreferences(read func(string) (string, error), write func(string, string) error) {
	s.layoutRead, s.layoutWrite = read, write
}
func engineLayoutKey(d *EngineDocument) string {
	identity, _ := json.Marshal(d.Object)
	return "engine_data_layout:" + base64.RawURLEncoding.EncodeToString(identity)
}
func engineLayoutSnapshot(d *EngineDocument) string {
	b, e := json.Marshal(engineLayout{Version: 1, Order: d.grid.Columns.Order, Widths: d.grid.Columns.Widths})
	if e != nil {
		return ""
	}
	return string(b)
}
func (s *EngineState) restoreEngineLayout(d *EngineDocument) {
	if d.Page == nil {
		return
	}
	if !d.layout.restored {
		d.layout.restored = true
		if s.layoutRead != nil {
			raw, e := s.layoutRead(engineLayoutKey(d))
			if e == nil && raw != "" {
				var layout engineLayout
				if json.Unmarshal([]byte(raw), &layout) == nil && layout.Version == 1 {
					d.grid.Columns = ui.TableLayout{Order: layout.Order, Widths: layout.Widths}
				}
			}
		}
	}
	known := map[string]bool{}
	for _, f := range d.Page.Table.Columns {
		known[f.Name] = true
	}
	var order []string
	seen := map[string]bool{}
	for _, name := range d.grid.Columns.Order {
		if known[name] && !seen[name] {
			order = append(order, name)
			seen[name] = true
		}
	}
	for _, f := range d.Page.Table.Columns {
		if !seen[f.Name] {
			order = append(order, f.Name)
		}
	}
	d.grid.Columns.Order = order
	for name, width := range d.grid.Columns.Widths {
		if !known[name] || width < 30 || width > 3000 || math.IsNaN(float64(width)) || math.IsInf(float64(width), 0) {
			delete(d.grid.Columns.Widths, name)
		}
	}
	if d.layout.saved == "" {
		d.layout.saved = engineLayoutSnapshot(d)
		d.layout.observed = d.layout.saved
	}
}
func (s *EngineState) trackEngineLayout(c *ui.Context, d *EngineDocument) {
	if !d.layout.restored || s.layoutWrite == nil {
		return
	}
	current := engineLayoutSnapshot(d)
	if current != d.layout.observed {
		d.layout.observed = current
		d.layout.changedAt = c.Now()
		d.layout.failed = false
	}
	if current == d.layout.saved || current == "" || d.layout.failed {
		return
	}
	remaining := 300*time.Millisecond - c.Now().Sub(d.layout.changedAt)
	if remaining > 0 {
		c.After(remaining)
		return
	}
	s.flushEngineLayout(d)
}
func (s *EngineState) flushEngineLayout(d *EngineDocument) {
	if !d.layout.restored || s.layoutWrite == nil {
		return
	}
	current := engineLayoutSnapshot(d)
	if current == "" || current == d.layout.saved || d.layout.failed && current == d.layout.observed {
		return
	}
	if e := s.layoutWrite(engineLayoutKey(d), current); e != nil {
		d.layout.failed = true
		if s.deps.Status != nil {
			s.deps.Status("保存表格布局失败；本次调整仍然有效")
		}
		return
	}
	d.layout.saved = current
	d.layout.failed = false
}
