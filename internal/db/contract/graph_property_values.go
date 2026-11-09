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
	kind, size := "", 16
	for _, item := range v.Values {
		if item.Kind == "null" || kind != "" && item.Kind != kind {
			return "", ErrUnsupported
		}
		if _, err := GraphPropertyScalarValue(item); err != nil {
			return "", err
		}
		kind = item.Kind
		size += len(item.Text) + 16
		if size > 1<<20 {
			return "", ErrUnsupported
		}
	}
	return kind, nil
}

// ParseGraphPropertyList uses the explicitly selected kind, keeping integer
// tokens out of float64 and retaining a list of typed values rather than text.
func ParseGraphPropertyList(kind, text string) (GraphValue, error) {
	if kind != "string" && kind != "boolean" && kind != "integer" && kind != "float" || !utf8.ValidString(text) || len(text) > 1<<20 {
		return GraphValue{}, ErrUnsupported
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(text), &items); err != nil || items == nil || len(items) > GraphPropertyListMaxItems {
		return GraphValue{}, ErrUnsupported
	}
	v := GraphValue{Kind: "list", Values: make([]GraphValue, len(items))}
	for index, raw := range items {
		item := GraphValue{Kind: kind, Text: strings.TrimSpace(string(raw))}
		if kind == "string" {
			if len(item.Text) == 0 || item.Text[0] != '"' || json.Unmarshal(raw, &item.Text) != nil {
				return GraphValue{}, ErrUnsupported
			}
		}
		if _, err := GraphPropertyScalarValue(item); err != nil {
			return GraphValue{}, err
		}
		v.Values[index] = item
	}
	if _, err := GraphPropertyListElementKind(v); err != nil {
		return GraphValue{}, err
	}
	return v, nil
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
		case "string":
			raw, _ := json.Marshal(item.Text)
			text.Write(raw)
		case "float":
			n, _ := strconv.ParseFloat(item.Text, 64)
			text.WriteString(strconv.FormatFloat(n, 'g', -1, 64))
		default:
			text.WriteString(item.Text)
		}
		if text.Len() > (1<<20)-1 {
			return "", ErrUnsupported
		}
	}
	text.WriteByte(']')
	return text.String(), nil
}
