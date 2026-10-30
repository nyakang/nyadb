package dataworkspace

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/locales"
)

const mysqlDataXMLSchemaInstance = "http://www.w3.org/2001/XMLSchema-instance"

// XML follows the observed Navicat RECORDS/RECORD envelope. xsi:nil and an
// explicit binary encoding extend that envelope to preserve otherwise ambiguous
// NULL/empty values and bytes. Ordinary empty elements always mean empty text.
type mysqlDataXMLEncoder struct {
	encoder *xml.Encoder
	root    xml.StartElement
	closed  bool
	fields  []string
}

func newMySQLDataXMLEncoder(w io.Writer, fields []string) (*mysqlDataXMLEncoder, error) {
	for _, field := range fields {
		if field == "" || !mysqlDataXMLTextValid(field) {
			return nil, mysqlDataXMLError()
		}
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return nil, err
	}
	e := &mysqlDataXMLEncoder{encoder: xml.NewEncoder(w), fields: append([]string(nil), fields...), root: xml.StartElement{Name: xml.Name{Local: "RECORDS"}, Attr: []xml.Attr{{Name: xml.Name{Local: "xmlns:xsi"}, Value: mysqlDataXMLSchemaInstance}}}}
	e.encoder.Indent("", "  ")
	if err := e.encoder.EncodeToken(e.root); err != nil {
		return nil, err
	}
	return e, nil
}
func (e *mysqlDataXMLEncoder) Row(values []mysqlsvc.MySQLDataValue) error {
	if e.closed || len(values) != len(e.fields) {
		return mysqlsvc.ErrMySQLDataPage
	}
	// encoding/xml can replace unsupported characters; reject them before writing
	// any part of a row rather than exporting silently changed text.
	for _, value := range values {
		if !value.Null && !value.Binary && !mysqlDataXMLTextValid(value.Text) {
			return mysqlDataXMLError()
		}
	}
	row := xml.StartElement{Name: xml.Name{Local: "RECORD"}}
	if err := e.encoder.EncodeToken(row); err != nil {
		return err
	}
	for i, field := range e.fields {
		value := values[i]
		element := xml.StartElement{Name: xml.Name{Local: field}}
		if !mysqlDataXMLNameValid(field) {
			element.Name.Local = "FIELD"
			element.Attr = append(element.Attr, xml.Attr{Name: xml.Name{Local: "name"}, Value: field})
		}
		text := value.Text
		if value.Null {
			element.Attr = append(element.Attr, xml.Attr{Name: xml.Name{Local: "xsi:nil"}, Value: "true"})
			text = ""
		} else if value.Binary {
			element.Attr = append(element.Attr, xml.Attr{Name: xml.Name{Local: "encoding"}, Value: "base64"})
			text = base64.StdEncoding.EncodeToString(value.Bytes)
		}
		if err := e.encoder.EncodeToken(element); err != nil {
			return err
		}
		if text != "" {
			if err := e.encoder.EncodeToken(xml.CharData(text)); err != nil {
				return err
			}
		}
		if err := e.encoder.EncodeToken(element.End()); err != nil {
			return err
		}
	}
	return e.encoder.EncodeToken(row.End())
}
func (e *mysqlDataXMLEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	if err := e.encoder.EncodeToken(e.root.End()); err != nil {
		return err
	}
	return e.encoder.Flush()
}
func mysqlDataXMLError() error {
	return errors.New(locales.Text("XML 格式无效或包含不支持的内容"))
}
func mysqlDataXMLTextValid(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if !(r == 9 || r == 10 || r == 13 || r >= 0x20 && r <= 0xd7ff || r >= 0xe000 && r <= 0xfffd || r >= 0x10000 && r <= 0x10ffff) {
			return false
		}
	}
	return true
}

// Names outside this conservative Unicode NCName subset use FIELD/name, so
// qualified names, spaces and punctuation in database field names remain exact.
func mysqlDataXMLNameValid(name string) bool {
	for i, r := range name {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
		} else if r != '_' && r != '-' && r != '.' && !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) {
			return false
		}
	}
	return name != ""
}

// The outer importer already limits the complete UTF-8 file to 1 MiB. This
// token parser accepts only a flat record envelope, never SQL or a recursive
// tree. DTDs, processing instructions, external entities and nested fields are
// rejected; encoding/xml performs no external entity lookup.
func mysqlDataParseXMLImport(data []byte) (*mysqlDataImportInput, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	input := &mysqlDataImportInput{}
	opened, ended := false, false
	for {
		token, err := dec.Token()
		if err == io.EOF {
			if !ended {
				return nil, mysqlDataXMLError()
			}
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.ProcInst:
			if opened || t.Target != "xml" {
				return nil, mysqlDataXMLError()
			}
		case xml.Directive:
			return nil, mysqlDataXMLError()
		case xml.Comment:
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return nil, mysqlDataXMLError()
			}
		case xml.StartElement:
			if ended {
				return nil, mysqlDataXMLError()
			}
			if !opened {
				if t.Name.Space != "" || t.Name.Local != "RECORDS" || !mysqlDataXMLNamespacesOnly(t.Attr) {
					return nil, mysqlDataXMLError()
				}
				opened = true
				continue
			}
			if t.Name.Space != "" || t.Name.Local != "RECORD" {
				return nil, mysqlDataXMLError()
			}
			names, values, err := mysqlDataReadXMLRecord(dec, t)
			if err != nil {
				return nil, err
			}
			if len(names) == 0 || len(names) > 128 {
				return nil, mysqlDataXMLError()
			}
			if len(input.Fields) == 0 {
				input.Fields = names
			}
			if len(names) != len(input.Fields) {
				return nil, errors.New(locales.Text("XML 每行必须包含相同字段"))
			}
			byName := make(map[string]mysqlsvc.MySQLDataValue, len(names))
			for i, name := range names {
				byName[name] = values[i]
			}
			ordered := make([]mysqlsvc.MySQLDataValue, len(input.Fields))
			for i, name := range input.Fields {
				value, ok := byName[name]
				if !ok {
					return nil, errors.New(locales.Text("XML 每行必须包含相同字段"))
				}
				ordered[i] = value
			}
			input.Rows = append(input.Rows, ordered)
			if len(input.Rows) > mysqlsvc.MySQLDataMaxPageSize {
				return nil, errors.New(locales.Text("导入文件超过 200 行限制"))
			}
		case xml.EndElement:
			if !opened || ended || t.Name.Space != "" || t.Name.Local != "RECORDS" {
				return nil, mysqlDataXMLError()
			}
			ended = true
		default:
			return nil, mysqlDataXMLError()
		}
	}
	return input, nil
}
func mysqlDataXMLNamespacesOnly(attrs []xml.Attr) bool {
	for _, attr := range attrs {
		if attr.Name.Space != "xmlns" && !(attr.Name.Space == "" && attr.Name.Local == "xmlns") {
			return false
		}
	}
	return true
}
func mysqlDataReadXMLRecord(dec *xml.Decoder, start xml.StartElement) ([]string, []mysqlsvc.MySQLDataValue, error) {
	var fields []string
	var values []mysqlsvc.MySQLDataValue
	seen := map[string]bool{}
	attributes := false
	add := func(name string, value mysqlsvc.MySQLDataValue) error {
		if name == "" || seen[name] || len(fields) >= 128 {
			return errors.New(locales.Text("导入字段名不能为空或重复"))
		}
		seen[name] = true
		fields = append(fields, name)
		values = append(values, value)
		return nil
	}
	for _, attr := range start.Attr {
		if attr.Name.Space == "xmlns" || attr.Name.Space == "" && attr.Name.Local == "xmlns" {
			continue
		}
		if attr.Name.Space != "" {
			return nil, nil, mysqlDataXMLError()
		}
		attributes = true
		if err := add(attr.Name.Local, mysqlsvc.MySQLDataValue{Text: attr.Value}); err != nil {
			return nil, nil, err
		}
	}
	for {
		token, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		switch t := token.(type) {
		case xml.Comment:
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return nil, nil, mysqlDataXMLError()
			}
		case xml.StartElement:
			if attributes {
				return nil, nil, mysqlDataXMLError()
			}
			field, value, err := mysqlDataReadXMLField(dec, t)
			if err != nil {
				return nil, nil, err
			}
			if err = add(field, value); err != nil {
				return nil, nil, err
			}
		case xml.EndElement:
			if t.Name != start.Name {
				return nil, nil, mysqlDataXMLError()
			}
			return fields, values, nil
		default:
			return nil, nil, mysqlDataXMLError()
		}
	}
}
func mysqlDataReadXMLField(dec *xml.Decoder, start xml.StartElement) (string, mysqlsvc.MySQLDataValue, error) {
	if start.Name.Space != "" {
		return "", mysqlsvc.MySQLDataValue{}, mysqlDataXMLError()
	}
	name := start.Name.Local
	nilValue, encoding := false, ""
	seen := map[xml.Name]bool{}
	for _, attr := range start.Attr {
		if seen[attr.Name] {
			return "", mysqlsvc.MySQLDataValue{}, mysqlDataXMLError()
		}
		seen[attr.Name] = true
		switch {
		case attr.Name.Space == "xmlns" || attr.Name.Space == "" && attr.Name.Local == "xmlns":
		case attr.Name.Space == mysqlDataXMLSchemaInstance && attr.Name.Local == "nil":
			switch attr.Value {
			case "true", "1":
				nilValue = true
			case "false", "0":
			default:
				return "", mysqlsvc.MySQLDataValue{}, mysqlDataXMLError()
			}
		case attr.Name.Space == "" && attr.Name.Local == "encoding":
			encoding = attr.Value
		case attr.Name.Space == "" && attr.Name.Local == "name" && start.Name.Local == "FIELD":
			name = attr.Value
		default:
			return "", mysqlsvc.MySQLDataValue{}, mysqlDataXMLError()
		}
	}
	var text strings.Builder
	for {
		token, err := dec.Token()
		if err != nil {
			return "", mysqlsvc.MySQLDataValue{}, err
		}
		switch t := token.(type) {
		case xml.Comment:
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if t.Name != start.Name {
				return "", mysqlsvc.MySQLDataValue{}, mysqlDataXMLError()
			}
			value := mysqlsvc.MySQLDataValue{Text: text.String()}
			if nilValue {
				if text.Len() != 0 || encoding != "" {
					return "", value, mysqlDataXMLError()
				}
				return name, mysqlsvc.MySQLDataValue{Null: true}, nil
			}
			if encoding != "" {
				var raw []byte
				switch encoding {
				case "base64":
					raw, err = base64.StdEncoding.Strict().DecodeString(text.String())
				case "hex":
					raw, err = hex.DecodeString(text.String())
				default:
					return "", value, mysqlDataXMLError()
				}
				if err != nil {
					return "", value, fmt.Errorf("%w: %v", mysqlDataXMLError(), err)
				}
				value = mysqlsvc.MySQLDataValue{Binary: true, Bytes: raw}
			}
			return name, value, nil
		default:
			return "", mysqlsvc.MySQLDataValue{}, mysqlDataXMLError()
		}
	}
}
