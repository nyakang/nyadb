package dataworkspace

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

// DBF imports follow the public dBASE/Visual FoxPro field descriptors. The
// self-contained writer emits VFP varchar/varbinary records and its standard
// _NullFlags bitmap, so trailing spaces, empty bytes and NULL remain distinct.
// LDID 0 denotes an explicitly selected UTF-8 text encoding; the wizard must
// retain that encoding when importing into clients without a UTF-8 code page.
type mysqlDataDBFEncoder struct {
	spool *mysqlDataCodecSpool
	w     io.Writer
}

func newMySQLDataDBFEncoder(w io.Writer, fields []string) (*mysqlDataDBFEncoder, error) {
	s, err := newMySQLDataCodecSpool(fields)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, name := range fields {
		upper := strings.ToUpper(name)
		if len(name) > 10 || strings.IndexFunc(name, func(r rune) bool { return r < 33 || r > 126 }) >= 0 || seen[upper] || upper == "_NULLFLAGS" {
			s.close()
			return nil, errors.New("DBF field names require distinct ASCII aliases of at most 10 bytes")
		}
		seen[upper] = true
	}
	return &mysqlDataDBFEncoder{spool: s, w: w}, nil
}
func (e *mysqlDataDBFEncoder) Row(row []mysqlsvc.MySQLDataValue) error {
	if e.spool.rows >= math.MaxUint32 {
		return mysqlsvc.ErrMySQLDataEditLimit
	}
	for _, v := range row {
		if len(v.Text)+len(v.Bytes) > 254 {
			return errors.New("self-contained DBF varchar/varbinary fields support at most 254 bytes; choose another format for memo values")
		}
	}
	return e.spool.Row(row)
}
func (e *mysqlDataDBFEncoder) Abort() error { return e.spool.close() }
func (e *mysqlDataDBFEncoder) Close() error { return e.CloseContext(mysqlDataCodecContext(e.w)) }
func (e *mysqlDataDBFEncoder) CloseContext(ctx context.Context) (err error) {
	if e.spool.closed {
		return nil
	}
	defer func() { err = errors.Join(err, e.spool.close()) }()
	fields := len(e.spool.fields)
	flagsWidth := (fields*2 + 7) / 8
	recordLength := 1 + flagsWidth
	for _, width := range e.spool.widths {
		recordLength += max(1, width)
	}
	headerLength := 32 + (fields+1)*32 + 1 + 263
	if recordLength > 65535 || headerLength > 65535 {
		return mysqlsvc.ErrMySQLDataEditLimit
	}
	header := make([]byte, headerLength)
	header[0] = 0x32
	now := time.Now().UTC()
	header[1], header[2], header[3] = byte(now.Year()-1900), byte(now.Month()), byte(now.Day())
	binary.LittleEndian.PutUint32(header[4:], uint32(e.spool.rows))
	binary.LittleEndian.PutUint16(header[8:], uint16(headerLength))
	binary.LittleEndian.PutUint16(header[10:], uint16(recordLength))
	offset := 1
	for i, name := range e.spool.fields {
		d := header[32+i*32:]
		copy(d, name)
		d[11] = 'V'
		if e.spool.kinds[i] == "binary" {
			d[11] = 'Q'
			d[18] |= 4
		}
		d[18] |= 2
		binary.LittleEndian.PutUint32(d[12:], uint32(offset))
		d[16] = byte(max(1, e.spool.widths[i]))
		offset += int(d[16])
	}
	d := header[32+fields*32:]
	copy(d, "_NullFlags")
	d[11] = '0'
	binary.LittleEndian.PutUint32(d[12:], uint32(offset))
	d[16] = byte(flagsWidth)
	d[18] = 5
	header[32+(fields+1)*32] = 0x0d
	if _, err = e.w.Write(header); err != nil {
		return err
	}
	err = e.spool.each(ctx, func(values []mysqlsvc.MySQLDataValue) error {
		row := make([]byte, recordLength)
		row[0] = ' '
		flags := row[recordLength-flagsWidth:]
		offset := 1
		for i, v := range values {
			width := max(1, e.spool.widths[i])
			value := []byte(v.Text)
			if v.Binary {
				value = v.Bytes
			}
			if v.Null {
				flags[(i*2+1)/8] |= 1 << uint((i*2+1)%8)
			} else {
				copy(row[offset:offset+width], value)
				if len(value) < width {
					flags[i*2/8] |= 1 << uint(i*2%8)
					row[offset+width-1] = byte(len(value))
				}
			}
			offset += width
		}
		_, err := e.w.Write(row)
		return err
	})
	if err != nil {
		return err
	}
	_, err = e.w.Write([]byte{0x1a})
	return err
}

type mysqlDataDBFField struct {
	name                                     string
	kind, flags                              byte
	offset, width, scale, lengthBit, nullBit int
}
type mysqlDataDBFRows struct {
	reader                io.Reader
	fields                []mysqlDataDBFField
	names                 []string
	recordLength          int
	remaining             uint32
	nullOffset, nullWidth int
	textEncoding          encoding.Encoding
	closed                bool
}

func mysqlDataOpenDBFRows(reader io.Reader) (*mysqlDataDBFRows, error) {
	return mysqlDataOpenDBFRowsWithEncoding(reader, "")
}
func mysqlDataOpenDBFRowsWithEncoding(reader io.Reader, name string) (*mysqlDataDBFRows, error) {
	h := make([]byte, 32)
	if _, err := io.ReadFull(reader, h); err != nil {
		return nil, err
	}
	switch h[0] {
	case 0x02, 0x03, 0x04, 0x05, 0x30, 0x31, 0x32, 0x43, 0x63, 0x83, 0x8b, 0xcb, 0xf5:
	default:
		return nil, errors.New("unsupported DBF version")
	}
	headerLength := int(binary.LittleEndian.Uint16(h[8:]))
	recordLength := int(binary.LittleEndian.Uint16(h[10:]))
	if headerLength < 33 || recordLength < 2 || recordLength > 65535 {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	block := make([]byte, headerLength-32)
	if _, err := io.ReadFull(reader, block); err != nil {
		return nil, err
	}
	r := &mysqlDataDBFRows{reader: reader, recordLength: recordLength, remaining: binary.LittleEndian.Uint32(h[4:]), nullOffset: -1}
	var err error
	r.textEncoding, err = mysqlDataDBFEncoding(h[29], name)
	if err != nil {
		return nil, err
	}
	offset, bit := 1, 0
	seen := map[string]bool{}
	ended := false
	for pos := 0; pos < len(block); pos += 32 {
		if block[pos] == 0x0d {
			ended = true
			break
		}
		if pos+32 > len(block) {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		d := block[pos : pos+32]
		name := string(bytes.TrimRight(d[:11], "\x00"))
		if n := strings.IndexByte(name, 0); n >= 0 {
			name = name[:n]
		}
		if name == "" || seen[strings.ToUpper(name)] {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		seen[strings.ToUpper(name)] = true
		width := int(d[16])
		if d[11] == 'C' && h[0] != 0x30 && h[0] != 0x31 && h[0] != 0x32 {
			width |= int(d[17]) << 8
		}
		if width == 0 || offset+width > recordLength {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		f := mysqlDataDBFField{name: name, kind: d[11], flags: d[18], offset: offset, width: width, scale: int(d[17]), lengthBit: -1, nullBit: -1}
		offset += width
		if f.kind == 'V' || f.kind == 'Q' {
			f.lengthBit = bit
			bit++
		}
		if f.flags&2 != 0 {
			f.nullBit = bit
			bit++
		}
		if f.kind == '0' && strings.EqualFold(name, "_NullFlags") {
			r.nullOffset, r.nullWidth = f.offset, f.width
			continue
		}
		if f.flags&1 != 0 {
			continue
		}
		if len(r.fields) >= 128 {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		r.fields = append(r.fields, f)
		r.names = append(r.names, name)
	}
	if !ended || len(r.fields) == 0 || offset != recordLength || bit > 0 && (r.nullOffset < 0 || r.nullWidth*8 < bit) {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	return r, nil
}
func mysqlDataDBFEncoding(code byte, name string) (encoding.Encoding, error) {
	switch strings.ToUpper(strings.ReplaceAll(name, "-", "")) {
	case "UTF8":
		return nil, nil
	case "GBK", "CP936":
		return simplifiedchinese.GBK, nil
	case "GB18030":
		return simplifiedchinese.GB18030, nil
	case "SHIFT_JIS", "SHIFTJIS", "CP932":
		return japanese.ShiftJIS, nil
	case "UTF16LE":
		return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM), nil
	case "UTF16BE":
		return unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM), nil
	case "WINDOWS1252", "CP1252":
		return charmap.Windows1252, nil
	case "ISO88591", "LATIN1":
		return charmap.ISO8859_1, nil
	case "":
	default:
		return nil, errors.New("unsupported DBF text encoding")
	}
	switch code {
	case 0, 0xf0:
		return nil, nil
	case 1:
		return charmap.CodePage437, nil
	case 2:
		return charmap.CodePage850, nil
	case 3, 0x57:
		return charmap.Windows1252, nil
	case 0x7a:
		return simplifiedchinese.GBK, nil
	case 0x64:
		return charmap.CodePage852, nil
	case 0x65:
		return charmap.CodePage866, nil
	case 0x66:
		return charmap.CodePage865, nil
	case 0xc8:
		return charmap.Windows1250, nil
	case 0xc9:
		return charmap.Windows1251, nil
	case 0xcb:
		return charmap.Windows1253, nil
	default:
		return nil, fmt.Errorf("DBF code page 0x%02x requires an explicit supported encoding", code)
	}
}
func (r *mysqlDataDBFRows) Fields() []string { return append([]string(nil), r.names...) }
func (r *mysqlDataDBFRows) Close() error     { r.closed = true; r.reader = nil; return nil }
func (r *mysqlDataDBFRows) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.closed || r.remaining == 0 {
			return nil, io.EOF
		}
		data := make([]byte, r.recordLength)
		if _, err := io.ReadFull(r.reader, data); err != nil {
			return nil, err
		}
		r.remaining--
		if data[0] == '*' {
			continue
		}
		if data[0] != ' ' {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		flag := func(bit int) bool { return bit >= 0 && data[r.nullOffset+bit/8]&(1<<uint(bit%8)) != 0 }
		row := make([]mysqlsvc.MySQLDataValue, len(r.fields))
		for i, f := range r.fields {
			if flag(f.nullBit) {
				row[i].Null = true
				continue
			}
			value := data[f.offset : f.offset+f.width]
			if f.lengthBit >= 0 && flag(f.lengthBit) {
				length := int(value[len(value)-1])
				if length >= len(value) {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				value = value[:length]
			}
			v, err := r.value(f, value)
			if err != nil {
				return nil, fmt.Errorf("DBF field %s: %w", f.name, err)
			}
			row[i] = v
		}
		return row, nil
	}
}
func (r *mysqlDataDBFRows) value(f mysqlDataDBFField, data []byte) (mysqlsvc.MySQLDataValue, error) {
	v := mysqlsvc.MySQLDataValue{}
	switch f.kind {
	case 'Q':
		v.Binary = true
		v.Bytes = bytes.Clone(data)
		if v.Bytes == nil {
			v.Bytes = []byte{}
		}
	case 'V', 'C':
		if f.flags&4 != 0 {
			v.Binary = true
			v.Bytes = bytes.Clone(data)
			if v.Bytes == nil {
				v.Bytes = []byte{}
			}
		} else {
			if r.textEncoding != nil {
				original := data
				var err error
				data, err = r.textEncoding.NewDecoder().Bytes(data)
				if err != nil {
					return v, err
				}
				encoded, err := r.textEncoding.NewEncoder().Bytes(data)
				if err != nil || !bytes.Equal(encoded, original) {
					return v, errors.New("DBF text contains invalid or ambiguous bytes for the selected encoding")
				}
			}
			if !utf8.Valid(data) {
				return v, errors.New("DBF text encoding does not match the file")
			}
			v.Text = string(data)
			if f.kind == 'C' {
				v.Text = strings.TrimRight(v.Text, " \x00")
			}
		}
	case 'N', 'F':
		v.Text = strings.TrimSpace(string(data))
		if v.Text == "" {
			v.Null = true
		} else if !mysqlDataImportNumber.MatchString(v.Text) {
			return v, mysqlsvc.ErrMySQLDataPage
		}
	case 'L':
		if len(data) != 1 {
			return v, mysqlsvc.ErrMySQLDataPage
		}
		switch data[0] {
		case 'T', 't', 'Y', 'y':
			v.Text = "1"
		case 'F', 'f', 'N', 'n':
			v.Text = "0"
		case '?', ' ', '\x00':
			v.Null = true
		default:
			return v, mysqlsvc.ErrMySQLDataPage
		}
	case 'D':
		text := strings.TrimSpace(string(data))
		if text == "" || text == "00000000" {
			v.Null = true
		} else {
			d, err := time.Parse("20060102", text)
			if err != nil {
				return v, err
			}
			v.Text = d.Format("2006-01-02")
		}
	case 'I', '+':
		if len(data) != 4 {
			return v, mysqlsvc.ErrMySQLDataPage
		}
		v.Text = strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(data))), 10)
	case 'B', 'O':
		if len(data) != 8 {
			return v, errors.New("DBF memo columns require the companion memo file")
		}
		n := math.Float64frombits(binary.LittleEndian.Uint64(data))
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return v, mysqlsvc.ErrMySQLDataPage
		}
		v.Text = strconv.FormatFloat(n, 'g', -1, 64)
	case 'Y':
		if len(data) != 8 {
			return v, mysqlsvc.ErrMySQLDataPage
		}
		n := int64(binary.LittleEndian.Uint64(data))
		negative := n < 0
		digits := strconv.FormatUint(uint64(n), 10)
		if negative {
			digits = strconv.FormatUint(uint64(-(n+1))+1, 10)
		}
		for len(digits) < 5 {
			digits = "0" + digits
		}
		v.Text = digits[:len(digits)-4] + "." + digits[len(digits)-4:]
		if negative {
			v.Text = "-" + v.Text
		}
	case 'T', '@':
		if len(data) != 8 {
			return v, mysqlsvc.ErrMySQLDataPage
		}
		days := int64(binary.LittleEndian.Uint32(data))
		millis := int64(binary.LittleEndian.Uint32(data[4:]))
		if days == 0 && millis == 0 {
			v.Null = true
		} else {
			if millis >= 86400000 {
				return v, mysqlsvc.ErrMySQLDataPage
			}
			d := time.Unix((days-2440588)*86400, millis*int64(time.Millisecond)).UTC()
			if d.Year() < 1 || d.Year() > 9999 {
				return v, mysqlsvc.ErrMySQLDataPage
			}
			v.Text = d.Format("2006-01-02 15:04:05.999")
		}
	case 'M', 'G', 'P', 'W':
		return v, errors.New("DBF memo fields require a companion-file reader; select a self-contained format")
	default:
		return v, errors.New("unsupported DBF field type")
	}
	return v, nil
}
func mysqlDataParseDBFImport(data []byte) (*mysqlDataImportInput, error) {
	r, err := mysqlDataOpenDBFRows(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	input := &mysqlDataImportInput{Fields: r.Fields()}
	for {
		row, err := r.Next(context.Background())
		if err == io.EOF {
			return input, nil
		}
		if err != nil {
			return nil, err
		}
		input.Rows = append(input.Rows, row)
	}
}
