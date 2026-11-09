package contract

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const GraphPropertyListMaxItems = 100000

// GraphPropertyScalarValue validates the bounded scalar representation shared
// by property drafts and adapters. Null denotes absence, not a stored value.
func GraphPropertyScalarValue(v GraphValue) (any, error) {
	if len(v.Values) != 0 || len(v.Properties) != 0 || v.Node != nil || v.Relationship != nil || v.Path != nil || !utf8.ValidString(v.Text) || len(v.Text) > 1<<20 {
		return nil, ErrUnsupported
	}
	switch v.Kind {
	case "null":
		if v.Text == "" {
			return nil, nil
		}
	case "string":
		return v.Text, nil
	case "boolean":
		if v.Text == "true" || v.Text == "false" {
			return v.Text == "true", nil
		}
	case "integer":
		if n, err := strconv.ParseInt(v.Text, 10, 64); err == nil && strconv.FormatInt(n, 10) == v.Text {
			return n, nil
		}
	case "float":
		if n, err := strconv.ParseFloat(v.Text, 64); err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
			return n, nil
		}
	}
	return nil, ErrUnsupported
}

// GraphPropertyListElementKind returns the common supported element kind.
// Empty lists have no element kind. Mixed, nested and null elements are rejected.
func GraphPropertyListElementKind(v GraphValue) (string, error) {
	if v.Kind != "list" || v.Text != "" || len(v.Values) > GraphPropertyListMaxItems || len(v.Properties) != 0 || v.Node != nil || v.Relationship != nil || v.Path != nil {
		return "", ErrUnsupported
	}
	empty, _ := json.Marshal(GraphValue{Kind: "list", Values: []GraphValue{}})
	kind, size := "", len(empty)
	var spatialRef uint32
	for _, item := range v.Values {
		if item.Kind == "null" || kind != "" && item.Kind != kind {
			return "", ErrUnsupported
		}
		if _, err := GraphPropertyScalarValue(item); err != nil {
			value, err := GraphPropertyExtendedValue(item)
			if err != nil {
				return "", err
			}
			if point, ok := value.(GraphPropertyPointValue); ok {
				if spatialRef != 0 && spatialRef != point.SRID {
					return "", ErrUnsupported
				}
				spatialRef = point.SRID
			}
		}
		kind = item.Kind
		raw, err := json.Marshal(item)
		if err != nil {
			return "", ErrUnsupported
		}
		size += len(raw) + 1
		if size > 1<<20 {
			return "", ErrUnsupported
		}
	}
	return kind, nil
}

// ParseGraphPropertyList uses the explicitly selected kind, keeping integer
// tokens out of float64 and retaining a list of typed values rather than text.
func ParseGraphPropertyList(kind, text string) (GraphValue, error) {
	if !graphPropertyListKind(kind) || !utf8.ValidString(text) || len(text) > 1<<20 {
		return GraphValue{}, ErrUnsupported
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(text), &items); err != nil || items == nil || len(items) > GraphPropertyListMaxItems {
		return GraphValue{}, ErrUnsupported
	}
	v := GraphValue{Kind: "list", Values: make([]GraphValue, len(items))}
	for index, raw := range items {
		item := GraphValue{Kind: kind, Text: strings.TrimSpace(string(raw))}
		if kind == "string" || kind == "date" || kind == "local_time" || kind == "local_datetime" {
			if len(item.Text) == 0 || item.Text[0] != '"' || json.Unmarshal(raw, &item.Text) != nil {
				return GraphValue{}, ErrUnsupported
			}
		}
		if _, err := GraphPropertyScalarValue(item); err != nil {
			input := string(raw)
			if kind == "date" || kind == "local_time" || kind == "local_datetime" {
				input = item.Text
			}
			item, err = ParseGraphPropertyExtended(kind, input)
			if err != nil {
				return GraphValue{}, err
			}
		}
		v.Values[index] = item
	}
	if _, err := GraphPropertyListElementKind(v); err != nil {
		return GraphValue{}, err
	}
	return v, nil
}

func graphPropertyListKind(kind string) bool {
	switch kind {
	case "string", "boolean", "integer", "float", "date", "local_time", "local_datetime", "time", "datetime", "duration", "point2d", "point3d":
		return true
	}
	return false
}

// GraphPropertyListJSON formats the editing text without converting exact
// integers to floating point or quoting numeric elements as strings.
func GraphPropertyListJSON(v GraphValue) (string, error) {
	if _, err := GraphPropertyListElementKind(v); err != nil {
		return "", err
	}
	var text strings.Builder
	text.WriteByte('[')
	for index, item := range v.Values {
		if index > 0 {
			text.WriteByte(',')
		}
		switch item.Kind {
		case "string", "date", "local_time", "local_datetime":
			raw, _ := json.Marshal(item.Text)
			text.Write(raw)
		case "float":
			n, _ := strconv.ParseFloat(item.Text, 64)
			text.WriteString(strconv.FormatFloat(n, 'g', -1, 64))
		case "boolean", "integer":
			text.WriteString(item.Text)
		default:
			raw, err := GraphPropertyExtendedText(item)
			if err != nil {
				return "", err
			}
			text.WriteString(raw)
		}
		if text.Len() > (1<<20)-1 {
			return "", ErrUnsupported
		}
	}
	text.WriteByte(']')
	return text.String(), nil
}
