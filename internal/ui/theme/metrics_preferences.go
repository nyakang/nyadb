package theme

import (
	"encoding/json"
	"sync/atomic"
)

const MetricsPreferenceKey = "appearance_metrics"

type TableDensity string

const (
	DensityCompact  TableDensity = "compact"
	DensityStandard TableDensity = "standard"
	DensityRelaxed  TableDensity = "relaxed"
)

// MetricsPreferences contains logical sizes. Desktop text scaling and display
// DPI are applied separately; no window or document state is stored here.
type MetricsPreferences struct {
	UIFontSize   int          `json:"ui_font_size"`
	SQLFontSize  int          `json:"sql_font_size"`
	TableDensity TableDensity `json:"table_density"`
}

func DefaultMetrics() MetricsPreferences {
	return MetricsPreferences{12, 13, DensityCompact}
}

func NormalizeMetrics(p MetricsPreferences) MetricsPreferences {
	if p.UIFontSize < 11 || p.UIFontSize > 16 {
		p.UIFontSize = 12
	}
	if p.SQLFontSize < 10 || p.SQLFontSize > 24 {
		p.SQLFontSize = 13
	}
	switch p.TableDensity {
	case DensityCompact, DensityStandard, DensityRelaxed:
	default:
		p.TableDensity = DensityCompact
	}
	return p
}

// DecodeMetrics salvages each valid field independently. Unknown versions or
// malformed documents use defaults, without altering the separate color keys.
func DecodeMetrics(value string) MetricsPreferences {
	p := DefaultMetrics()
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(value), &raw) != nil {
		return p
	}
	var version int
	if json.Unmarshal(raw["version"], &version) != nil || version != 1 {
		return p
	}
	_ = json.Unmarshal(raw["ui_font_size"], &p.UIFontSize)
	_ = json.Unmarshal(raw["sql_font_size"], &p.SQLFontSize)
	_ = json.Unmarshal(raw["table_density"], &p.TableDensity)
	return NormalizeMetrics(p)
}

func EncodeMetrics(p MetricsPreferences) string {
	value, _ := json.Marshal(struct {
		Version int `json:"version"`
		MetricsPreferences
	}{1, NormalizeMetrics(p)})
	return string(value)
}

var selectedMetrics atomic.Pointer[MetricsPreferences]

func CurrentMetrics() MetricsPreferences {
	if p := selectedMetrics.Load(); p != nil {
		return *p
	}
	return DefaultMetrics()
}

func SetMetrics(p MetricsPreferences) {
	p = NormalizeMetrics(p)
	selectedMetrics.Store(&p)
}
