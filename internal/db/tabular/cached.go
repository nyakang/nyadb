package tabular

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
)

// EncodeAdditional encodes already cached values. It never queries a database.
func EncodeAdditional(set contract.ResultSet, format string) ([]byte, error) {
	var b bytes.Buffer
	if format == "XML" {
		type field struct {
			Name     string `xml:"name,attr"`
			Type     string `xml:"type,attr"`
			Nullable bool   `xml:"nullable,attr"`
		}
		type value struct {
			Kind      contract.CellKind `xml:"kind,attr"`
			Truncated bool              `xml:"truncated,attr,omitempty"`
			Text      string            `xml:",chardata"`
		}
		type row struct {
			Values []value `xml:"cell"`
		}
		out := struct {
			XMLName    xml.Name `xml:"result"`
			Fields     []field  `xml:"columns>column"`
			Rows       []row    `xml:"rows>row"`
			Truncation []string `xml:"truncation>reason,omitempty"`
		}{}
		for _, c := range set.Columns {
			if !XMLTextValid(c.Name) || !XMLTextValid(c.DatabaseType) {
				return nil, fmt.Errorf("invalid XML column")
			}
			out.Fields = append(out.Fields, field{c.Name, c.DatabaseType, c.Nullable})
		}
		for _, r := range set.Rows {
			rr := row{}
			for _, c := range r {
				text := c.Text
				if c.Kind == contract.Binary {
					text = base64.StdEncoding.EncodeToString(c.Bytes)
				}
				if !XMLTextValid(text) {
					return nil, fmt.Errorf("invalid XML value")
				}
				rr.Values = append(rr.Values, value{c.Kind, c.Truncated, text})
			}
			out.Rows = append(out.Rows, rr)
		}
		out.Truncation = append([]string(nil), set.Truncation...)
		raw, err := xml.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, err
		}
		return append([]byte(xml.Header), raw...), nil
	}
	var e interface {
		Row([]contract.DataValue) error
		Close() error
	}
	var err error
	fields := make([]string, len(set.Columns))
	for i, c := range set.Columns {
		fields[i] = c.Name
	}
	switch format {
	case "HTML":
		e, err = NewHTML(&b, fields)
	case "XLSX":
		e, err = NewXLSX(&b, fields)
	default:
		return nil, fmt.Errorf("unsupported cached export format: %s", format)
	}
	if err != nil {
		return nil, err
	}
	for _, row := range set.Rows {
		values := make([]contract.DataValue, len(row))
		for i, c := range row {
			values[i] = contract.DataValue{Kind: c.Kind, Text: c.Text, Bytes: c.Bytes, Null: c.Kind == contract.Null, Binary: c.Kind == contract.Binary}
		}
		if err = e.Row(values); err != nil {
			return nil, err
		}
	}
	if err = e.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
