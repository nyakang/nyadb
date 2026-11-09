package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"
)

// These bounded native components are neutral: adapters select driver types.
type GraphPropertyDurationValue struct {
	Months, Days, Seconds int64
	Nanoseconds           int
}
type GraphPropertyPointValue struct {
	SRID       uint32
	Dimensions int
	X, Y, Z    float64
}

var graphCivilDate = regexp.MustCompile(`^(-?[0-9]{4,9})-([0-9]{2})-([0-9]{2})$`)
var graphCivilClock = regexp.MustCompile(`^([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?$`)

func graphDateValue(text string) (time.Time, error) {
	m := graphCivilDate.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, ErrUnsupported
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if t.Year() != year || int(t.Month()) != month || t.Day() != day || t.Format("2006-01-02") != text {
		return time.Time{}, ErrUnsupported
	}
	return t, nil
}
func graphClockValue(text string) (time.Time, error) {
	m := graphCivilClock.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, ErrUnsupported
	}
	hour, _ := strconv.Atoi(m[1])
	minute, _ := strconv.Atoi(m[2])
	second, _ := strconv.Atoi(m[3])
	nano := 0
	if m[4] != "" {
		nano, _ = strconv.Atoi(m[4] + strings.Repeat("0", 9-len(m[4])))
	}
	if hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, ErrUnsupported
	}
	return time.Date(0, 1, 1, hour, minute, second, nano, time.UTC), nil
}
func graphLocalDateTime(text string) (time.Time, error) {
	date, clock, ok := strings.Cut(text, "T")
	d, err := graphDateValue(date)
	c, clockErr := graphClockValue(clock)
	if !ok || err != nil || clockErr != nil {
		return time.Time{}, ErrUnsupported
	}
	return time.Date(d.Year(), d.Month(), d.Day(), c.Hour(), c.Minute(), c.Second(), c.Nanosecond(), time.UTC), nil
}

func graphExtendedProperties(v GraphValue, names ...string) (map[string]GraphValue, error) {
	if len(v.Properties) != len(names) {
		return nil, ErrUnsupported
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	out := make(map[string]GraphValue, len(names))
	for _, property := range v.Properties {
		if !allowed[property.Name] {
			return nil, ErrUnsupported
		}
		delete(allowed, property.Name)
		if _, err := GraphPropertyScalarValue(property.Value); err != nil {
			return nil, err
		}
		out[property.Name] = property.Value
	}
	return out, nil
}
func graphExtendedInteger(v GraphValue) (int64, error) {
	x, err := GraphPropertyScalarValue(v)
	n, ok := x.(int64)
	if err != nil || !ok {
		return 0, ErrUnsupported
	}
	return n, nil
}
func graphExtendedFloat(v GraphValue) (float64, error) {
	x, err := GraphPropertyScalarValue(v)
	n, ok := x.(float64)
	if err != nil || !ok {
		return 0, ErrUnsupported
	}
	return n, nil
}

// GraphPropertyExtendedValue validates every native component, including
// hidden offset seconds, duration parts and spatial reference/dimensionality.
// Canonical Text must match those components; it is never a second value source.
func GraphPropertyExtendedValue(v GraphValue) (any, error) {
	if len(v.Values) != 0 || v.Node != nil || v.Relationship != nil || v.Path != nil || !utf8.ValidString(v.Text) || len(v.Text) > 1<<20 {
		return nil, ErrUnsupported
	}
	switch v.Kind {
	case "date", "local_time", "local_datetime":
		if len(v.Properties) != 0 {
			return nil, ErrUnsupported
		}
		var t time.Time
		var err error
		layout := "2006-01-02"
		switch v.Kind {
		case "date":
			t, err = graphDateValue(v.Text)
		case "local_time":
			t, err = graphClockValue(v.Text)
			layout = "15:04:05.999999999"
		case "local_datetime":
			t, err = graphLocalDateTime(v.Text)
			layout = "2006-01-02T15:04:05.999999999"
		}
		if err != nil || t.Format(layout) != v.Text {
			return nil, ErrUnsupported
		}
		return t, nil
	case "time", "datetime":
		names := []string{"offset_seconds"}
		if v.Kind == "datetime" {
			names = append(names, "zone")
		}
		p, err := graphExtendedProperties(v, names...)
		if err != nil {
			return nil, err
		}
		offset, err := graphExtendedInteger(p["offset_seconds"])
		if err != nil || offset < -18*3600 || offset > 18*3600 {
			return nil, ErrUnsupported
		}
		// Driver displays offsets to minutes; the separate offset_seconds is
		// authoritative for offsets such as +00:20:34 and historic named zones.
		clockEnd := len(v.Text) - 6
		if strings.HasSuffix(v.Text, "Z") {
			clockEnd = len(v.Text) - 1
		}
		if clockEnd <= 0 {
			return nil, ErrUnsupported
		}
		local := v.Text[:clockEnd]
		var civil time.Time
		if v.Kind == "datetime" {
			civil, err = graphLocalDateTime(local)
		} else {
			civil, err = graphClockValue(local)
		}
		if err != nil {
			return nil, err
		}
		t := civil.In(time.FixedZone("Offset", int(offset)))
		t = time.Date(civil.Year(), civil.Month(), civil.Day(), civil.Hour(), civil.Minute(), civil.Second(), civil.Nanosecond(), t.Location())
		layout := "15:04:05.999999999Z07:00"
		if v.Kind == "datetime" {
			if p["zone"].Kind != "string" || p["zone"].Text == "" || p["zone"].Text == "Local" || len(p["zone"].Text) > 4096 {
				return nil, ErrUnsupported
			}
			zone := p["zone"].Text
			if zone != "Offset" {
				location, err := time.LoadLocation(zone)
				if err != nil {
					return nil, ErrUnsupported
				}
				t = t.In(location)
				_, actual := t.Zone()
				if actual != int(offset) {
					return nil, ErrUnsupported
				}
			}
			layout = time.RFC3339Nano
		}
		if t.Format(layout) != v.Text {
			return nil, ErrUnsupported
		}
		return t, nil
	case "duration":
		p, err := graphExtendedProperties(v, "months", "days", "seconds", "nanoseconds")
		if err != nil {
			return nil, err
		}
		n := make([]int64, 4)
		for i, name := range []string{"months", "days", "seconds", "nanoseconds"} {
			n[i], err = graphExtendedInteger(p[name])
			if err != nil {
				return nil, err
			}
		}
		if n[3] < 0 || n[3] >= 1000000000 {
			return nil, ErrUnsupported
		}
		d := GraphPropertyDurationValue{Months: n[0], Days: n[1], Seconds: n[2], Nanoseconds: int(n[3])}
		if graphDurationText(d) != v.Text {
			return nil, ErrUnsupported
		}
		return d, nil
	case "point2d", "point3d":
		names := []string{"srid", "x", "y"}
		if v.Kind == "point3d" {
			names = append(names, "z")
		}
		p, err := graphExtendedProperties(v, names...)
		if err != nil || v.Text != "" {
			return nil, ErrUnsupported
		}
		srid, err := graphExtendedInteger(p["srid"])
		if err != nil || v.Kind == "point2d" && srid != 7203 && srid != 4326 || v.Kind == "point3d" && srid != 9157 && srid != 4979 {
			return nil, ErrUnsupported
		}
		point := GraphPropertyPointValue{SRID: uint32(srid), Dimensions: len(names) - 1}
		for name, dest := range map[string]*float64{"x": &point.X, "y": &point.Y, "z": &point.Z} {
			if name == "z" && point.Dimensions == 2 {
				continue
			}
			*dest, err = graphExtendedFloat(p[name])
			if err != nil {
				return nil, err
			}
		}
		if (srid == 4326 || srid == 4979) && (point.X < -180 || point.X > 180 || point.Y < -90 || point.Y > 90) {
			return nil, ErrUnsupported
		}
		return point, nil
	}
	return nil, ErrUnsupported
}

func graphDurationText(d GraphPropertyDurationValue) string {
	var b strings.Builder
	b.WriteByte('P')
	if years := d.Months / 12; years != 0 {
		fmt.Fprintf(&b, "%dY", years)
	}
	if months := d.Months % 12; months != 0 {
		fmt.Fprintf(&b, "%dM", months)
	}
	if d.Days != 0 {
		fmt.Fprintf(&b, "%dD", d.Days)
	}
	if d.Seconds != 0 || d.Nanoseconds != 0 {
		b.WriteByte('T')
		if hours := d.Seconds / 3600; hours != 0 {
			fmt.Fprintf(&b, "%dH", hours)
		}
		if minutes := d.Seconds % 3600 / 60; minutes != 0 {
			fmt.Fprintf(&b, "%dM", minutes)
		}
		seconds, nano := d.Seconds%60, d.Nanoseconds
		if nano > 0 {
			negativeZero := seconds == -1
			if seconds < 0 {
				seconds++
				nano = 1000000000 - nano
			}
			if negativeZero {
				b.WriteString("-0")
			} else {
				fmt.Fprintf(&b, "%d", seconds)
			}
			b.WriteString(strings.TrimRight(fmt.Sprintf(".%09d", nano), "0"))
			b.WriteByte('S')
		} else if seconds != 0 {
			fmt.Fprintf(&b, "%dS", seconds)
		}
	}
	if b.Len() == 1 {
		return "PT0S"
	}
	return b.String()
}

func graphExtendedObject(text string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrUnsupported
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || len(fields) > 4 {
			return nil, ErrUnsupported
		}
		if _, exists := fields[name]; exists {
			return nil, ErrUnsupported
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return nil, ErrUnsupported
		}
		fields[name] = raw
	}
	if _, err = decoder.Token(); err != nil {
		return nil, ErrUnsupported
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, ErrUnsupported
	}
	return fields, nil
}

// ParseGraphPropertyExtended builds one canonical typed value. JSON fields
// retain integer tokens; normalized duration and longitude are visible in the
// immutable preview before an explicit write is dispatched.
func ParseGraphPropertyExtended(kind, text string) (GraphValue, error) {
	if !utf8.ValidString(text) || len(text) > 1<<20 {
		return GraphValue{}, ErrUnsupported
	}
	v := GraphValue{Kind: kind, Text: strings.TrimSpace(text)}
	if kind == "date" || kind == "local_time" || kind == "local_datetime" {
		var t time.Time
		var err error
		layout := "2006-01-02"
		switch kind {
		case "date":
			t, err = graphDateValue(v.Text)
		case "local_time":
			t, err = graphClockValue(v.Text)
			layout = "15:04:05.999999999"
		case "local_datetime":
			t, err = graphLocalDateTime(v.Text)
			layout = "2006-01-02T15:04:05.999999999"
		}
		if err != nil {
			return GraphValue{}, err
		}
		v.Text = t.Format(layout)
	} else {
		fields, err := graphExtendedObject(v.Text)
		if err != nil {
			return GraphValue{}, err
		}
		names := []string{}
		switch kind {
		case "time":
			names = []string{"offset_seconds"}
		case "datetime":
			names = []string{"offset_seconds", "zone"}
		case "duration":
			names = []string{"days", "months", "nanoseconds", "seconds"}
		case "point2d":
			names = []string{"srid", "x", "y"}
		case "point3d":
			names = []string{"srid", "x", "y", "z"}
		default:
			return GraphValue{}, ErrUnsupported
		}
		v.Text = ""
		if kind == "time" || kind == "datetime" {
			if len(fields["value"]) == 0 || fields["value"][0] != '"' || json.Unmarshal(fields["value"], &v.Text) != nil {
				return GraphValue{}, ErrUnsupported
			}
			delete(fields, "value")
		}
		if len(fields) != len(names) {
			return GraphValue{}, ErrUnsupported
		}
		for _, name := range names {
			raw := fields[name]
			value := GraphValue{Kind: "integer", Text: string(bytes.TrimSpace(raw))}
			if name == "zone" {
				value.Kind = "string"
				if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value.Text) != nil {
					return GraphValue{}, ErrUnsupported
				}
			} else if name == "x" || name == "y" || name == "z" {
				value.Kind = "float"
				if len(raw) == 0 || raw[0] == '"' {
					return GraphValue{}, ErrUnsupported
				}
				n, err := graphExtendedFloat(value)
				if err != nil {
					return GraphValue{}, err
				}
				value.Text = strconv.FormatFloat(n, 'g', -1, 64)
			}
			if _, err := GraphPropertyScalarValue(value); err != nil {
				return GraphValue{}, err
			}
			v.Properties = append(v.Properties, GraphProperty{Name: name, Value: value})
		}
		if kind == "time" || kind == "datetime" {
			offset, err := graphExtendedInteger(v.Properties[0].Value)
			end := len(v.Text) - 6
			if strings.HasSuffix(v.Text, "Z") {
				end = len(v.Text) - 1
			}
			if err != nil || offset < -64800 || offset > 64800 || end <= 0 {
				return GraphValue{}, ErrUnsupported
			}
			suffix := time.Date(0, 1, 1, 0, 0, 0, 0, time.FixedZone("Offset", int(offset))).Format("Z07:00")
			if v.Text[end:] != suffix && !(offset == 0 && (v.Text[end:] == "+00:00" || v.Text[end:] == "-00:00")) {
				return GraphValue{}, ErrUnsupported
			}
			var t time.Time
			layout := "15:04:05.999999999"
			if kind == "datetime" {
				t, err = graphLocalDateTime(v.Text[:end])
				layout = "2006-01-02T15:04:05.999999999"
			} else {
				t, err = graphClockValue(v.Text[:end])
			}
			if err != nil {
				return GraphValue{}, err
			}
			v.Text = t.Format(layout) + suffix
		}
		if kind == "duration" {
			nano, _ := graphExtendedInteger(v.Properties[2].Value)
			seconds, _ := graphExtendedInteger(v.Properties[3].Value)
			quotient, remainder := nano/1000000000, nano%1000000000
			if remainder < 0 {
				quotient--
				remainder += 1000000000
			}
			total := new(big.Int).Add(big.NewInt(seconds), big.NewInt(quotient))
			if !total.IsInt64() {
				return GraphValue{}, ErrUnsupported
			}
			v.Properties[2].Value.Text = strconv.FormatInt(remainder, 10)
			v.Properties[3].Value.Text = total.String()
			months, _ := graphExtendedInteger(v.Properties[1].Value)
			days, _ := graphExtendedInteger(v.Properties[0].Value)
			v.Text = graphDurationText(GraphPropertyDurationValue{Months: months, Days: days, Seconds: total.Int64(), Nanoseconds: int(remainder)})
		}
		if kind == "point2d" || kind == "point3d" {
			srid, _ := graphExtendedInteger(v.Properties[0].Value)
			if srid == 4326 || srid == 4979 {
				x, _ := graphExtendedFloat(v.Properties[1].Value)
				if x < -180 || x > 180 {
					x = math.Mod(x, 360)
					if x < -180 {
						x += 360
					} else if x > 180 {
						x -= 360
					}
					v.Properties[1].Value.Text = strconv.FormatFloat(x, 'g', -1, 64)
				}
			}
		}
	}
	if _, err := GraphPropertyExtendedValue(v); err != nil {
		return GraphValue{}, err
	}
	return v, nil
}

func GraphPropertyExtendedText(v GraphValue) (string, error) {
	if _, err := GraphPropertyExtendedValue(v); err != nil {
		return "", err
	}
	if len(v.Properties) == 0 {
		return v.Text, nil
	}
	fields := make(map[string]any, len(v.Properties)+1)
	if v.Kind == "time" || v.Kind == "datetime" {
		fields["value"] = v.Text
	}
	for _, p := range v.Properties {
		value, _ := GraphPropertyScalarValue(p.Value)
		fields[p.Name] = value
	}
	raw, err := json.Marshal(fields)
	return string(raw), err
}
