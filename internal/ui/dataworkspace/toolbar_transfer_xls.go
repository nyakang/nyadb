package dataworkspace

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

// BIFF8/CFB are documented in Microsoft's public [MS-XLS] and [MS-CFB]
// specifications. NyaDB emits strings for database numbers to preserve their
// exact spelling. A hidden worksheet records NULL/binary kinds without changing
// the displayed data sheet or executing spreadsheet formulas.
type mysqlDataXLSEncoder struct {
	spool *mysqlDataCodecSpool
	w     io.Writer
}

func newMySQLDataXLSEncoder(w io.Writer, fields []string) (*mysqlDataXLSEncoder, error) {
	s, err := newMySQLDataCodecSpool(fields)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		if len(utf16.Encode([]rune(field))) > 32767 {
			s.close()
			return nil, mysqlsvc.ErrMySQLDataEditLimit
		}
	}
	return &mysqlDataXLSEncoder{spool: s, w: w}, nil
}
func (e *mysqlDataXLSEncoder) Row(values []mysqlsvc.MySQLDataValue) error {
	if e.spool.rows >= 65535 {
		return mysqlsvc.ErrMySQLDataEditLimit
	}
	for _, v := range values {
		text := v.Text
		if v.Binary {
			text = "0x" + hex.EncodeToString(v.Bytes)
		}
		if len(utf16.Encode([]rune(text))) > 32767 {
			return errors.New("XLS cells support at most 32767 UTF-16 code units")
		}
		if !utf8.ValidString(text) {
			return mysqlsvc.ErrMySQLDataPage
		}
	}
	return e.spool.Row(values)
}

func mysqlDataBIFFRecord(w io.Writer, id uint16, data []byte) error {
	if len(data) > 8224 {
		return mysqlsvc.ErrMySQLDataEditLimit
	}
	h := make([]byte, 4)
	binary.LittleEndian.PutUint16(h, id)
	binary.LittleEndian.PutUint16(h[2:], uint16(len(data)))
	if _, err := w.Write(h); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func mysqlDataBIFFBOF(w io.Writer, kind uint16) error {
	data := make([]byte, 16)
	binary.LittleEndian.PutUint16(data, 0x0600)
	binary.LittleEndian.PutUint16(data[2:], kind)
	binary.LittleEndian.PutUint16(data[4:], 0x0dbb)
	binary.LittleEndian.PutUint16(data[6:], 0x07cc)
	binary.LittleEndian.PutUint32(data[8:], 0x41)
	binary.LittleEndian.PutUint32(data[12:], 6)
	return mysqlDataBIFFRecord(w, 0x0809, data)
}

type mysqlDataBIFFSSTWriter struct {
	w     io.Writer
	data  []byte
	first bool
}

func newMySQLDataBIFFSSTWriter(w io.Writer, count uint32) *mysqlDataBIFFSSTWriter {
	data := make([]byte, 8, 8224)
	binary.LittleEndian.PutUint32(data, count)
	binary.LittleEndian.PutUint32(data[4:], count)
	return &mysqlDataBIFFSSTWriter{w: w, data: data, first: true}
}
func (s *mysqlDataBIFFSSTWriter) flush() error {
	id := uint16(0x003c)
	if s.first {
		id = 0x00fc
	}
	if err := mysqlDataBIFFRecord(s.w, id, s.data); err != nil {
		return err
	}
	s.first = false
	s.data = s.data[:0]
	return nil
}
func (s *mysqlDataBIFFSSTWriter) text(text string) error {
	u := utf16.Encode([]rune(text))
	if len(u) > 32767 {
		return mysqlsvc.ErrMySQLDataEditLimit
	}
	if len(s.data)+3 > 8224 {
		if err := s.flush(); err != nil {
			return err
		}
	}
	s.data = binary.LittleEndian.AppendUint16(s.data, uint16(len(u)))
	s.data = append(s.data, 1)
	for _, ch := range u {
		if len(s.data)+2 > 8224 {
			if err := s.flush(); err != nil {
				return err
			}
			s.data = append(s.data, 1)
		}
		s.data = binary.LittleEndian.AppendUint16(s.data, ch)
	}
	return nil
}

func (e *mysqlDataXLSEncoder) Abort() error { return e.spool.close() }
func (e *mysqlDataXLSEncoder) Close() error { return e.CloseContext(mysqlDataCodecContext(e.w)) }
func (e *mysqlDataXLSEncoder) CloseContext(ctx context.Context) (err error) {
	if e.spool.closed {
		return nil
	}
	defer func() { err = errors.Join(err, e.spool.close()) }()
	book, err := os.CreateTemp("", "nyadb-transfer-biff-*")
	if err != nil {
		return err
	}
	defer func() { name := book.Name(); err = errors.Join(err, book.Close(), os.Remove(name)) }()
	if err = mysqlDataBIFFBOF(book, 5); err != nil {
		return err
	}
	if err = mysqlDataBIFFRecord(book, 0x0042, []byte{0xb0, 0x04}); err != nil {
		return err
	}
	if err = mysqlDataBIFFRecord(book, 0x0022, []byte{0, 0}); err != nil {
		return err
	}
	font := make([]byte, 16)
	binary.LittleEndian.PutUint16(font, 200)
	binary.LittleEndian.PutUint16(font[4:], 0x7fff)
	binary.LittleEndian.PutUint16(font[6:], 400)
	font[12] = 1
	font[14] = 5
	font = append(font, []byte("Arial")...)
	if err = mysqlDataBIFFRecord(book, 0x0031, font); err != nil {
		return err
	}
	xf := make([]byte, 20)
	if err = mysqlDataBIFFRecord(book, 0x00e0, xf); err != nil {
		return err
	}
	var bounds [2]int64
	for i, name := range []string{"Data", "_NyaDBKinds"} {
		pos, _ := book.Seek(0, io.SeekCurrent)
		bounds[i] = pos + 4
		data := make([]byte, 8)
		data[4] = byte(i)
		data[6] = byte(len(name))
		data = append(data, []byte(name)...)
		if err = mysqlDataBIFFRecord(book, 0x0085, data); err != nil {
			return err
		}
	}
	nonNull := int64(len(e.spool.fields))
	err = e.spool.each(ctx, func(row []mysqlsvc.MySQLDataValue) error {
		for _, v := range row {
			if !v.Null {
				nonNull++
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	count := nonNull + int64(len(e.spool.fields))*(e.spool.rows+1)
	if count > math.MaxUint32 {
		return mysqlsvc.ErrMySQLDataEditLimit
	}
	sst := newMySQLDataBIFFSSTWriter(book, uint32(count))
	for _, field := range e.spool.fields {
		if err = sst.text(field); err != nil {
			return err
		}
	}
	err = e.spool.each(ctx, func(row []mysqlsvc.MySQLDataValue) error {
		for _, v := range row {
			if v.Null {
				continue
			}
			text := v.Text
			if v.Binary {
				text = "0x" + hex.EncodeToString(v.Bytes)
			}
			if err := sst.text(text); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, field := range e.spool.fields {
		if err = sst.text(field); err != nil {
			return err
		}
	}
	err = e.spool.each(ctx, func(row []mysqlsvc.MySQLDataValue) error {
		for _, v := range row {
			kind := "t"
			if v.Null {
				kind = "n"
			} else if v.Binary {
				kind = "b"
			}
			if err := sst.text(kind); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = sst.flush(); err != nil {
		return err
	}
	if err = mysqlDataBIFFRecord(book, 0x000a, nil); err != nil {
		return err
	}
	var offsets [2]int64
	for kind := 0; kind < 2; kind++ {
		offsets[kind], _ = book.Seek(0, io.SeekCurrent)
		if err = mysqlDataBIFFBOF(book, 0x10); err != nil {
			return err
		}
		dim := make([]byte, 14)
		binary.LittleEndian.PutUint32(dim[4:], uint32(e.spool.rows+1))
		binary.LittleEndian.PutUint16(dim[10:], uint16(len(e.spool.fields)))
		if err = mysqlDataBIFFRecord(book, 0x0200, dim); err != nil {
			return err
		}
		index := uint32(0)
		if kind == 1 {
			index = uint32(nonNull)
		}
		cell := func(row, column int, textIndex uint32) error {
			data := make([]byte, 10)
			binary.LittleEndian.PutUint16(data, uint16(row))
			binary.LittleEndian.PutUint16(data[2:], uint16(column))
			binary.LittleEndian.PutUint32(data[6:], textIndex)
			return mysqlDataBIFFRecord(book, 0x00fd, data)
		}
		for column := range e.spool.fields {
			if err = cell(0, column, index); err != nil {
				return err
			}
			index++
		}
		rowNum := 1
		err = e.spool.each(ctx, func(row []mysqlsvc.MySQLDataValue) error {
			for column, v := range row {
				if kind == 0 && v.Null {
					continue
				}
				if err := cell(rowNum, column, index); err != nil {
					return err
				}
				index++
			}
			rowNum++
			return nil
		})
		if err != nil {
			return err
		}
		if err = mysqlDataBIFFRecord(book, 0x000a, nil); err != nil {
			return err
		}
	}
	size, err := book.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	for i, offset := range offsets {
		if offset > math.MaxUint32 {
			return mysqlsvc.ErrMySQLDataEditLimit
		}
		data := make([]byte, 4)
		binary.LittleEndian.PutUint32(data, uint32(offset))
		if _, err = book.WriteAt(data, bounds[i]); err != nil {
			return err
		}
	}
	return mysqlDataWriteCFB(e.w, book, size)
}

const mysqlDataCFBEnd = uint32(0xfffffffe)
const mysqlDataCFBFree = uint32(0xffffffff)

func mysqlDataWriteCFB(w io.Writer, book *os.File, bookSize int64) error {
	streamSize := max(int64(4096), bookSize)
	if streamSize > math.MaxUint32 {
		return mysqlsvc.ErrMySQLDataEditLimit
	}
	dataSectors := int((streamSize + 511) / 512)
	fatSectors, difatSectors := 1, 0
	for {
		n := (dataSectors + 1 + fatSectors + difatSectors + 127) / 128
		d := (max(0, n-109) + 126) / 127
		if n == fatSectors && d == difatSectors {
			break
		}
		fatSectors, difatSectors = n, d
	}
	header := make([]byte, 512)
	copy(header, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	binary.LittleEndian.PutUint16(header[24:], 0x3e)
	binary.LittleEndian.PutUint16(header[26:], 3)
	binary.LittleEndian.PutUint16(header[28:], 0xfffe)
	binary.LittleEndian.PutUint16(header[30:], 9)
	binary.LittleEndian.PutUint16(header[32:], 6)
	binary.LittleEndian.PutUint32(header[44:], uint32(fatSectors))
	binary.LittleEndian.PutUint32(header[48:], uint32(dataSectors))
	binary.LittleEndian.PutUint32(header[56:], 4096)
	binary.LittleEndian.PutUint32(header[60:], mysqlDataCFBEnd)
	firstDIFAT := mysqlDataCFBEnd
	if difatSectors > 0 {
		firstDIFAT = uint32(dataSectors + 1 + fatSectors)
	}
	binary.LittleEndian.PutUint32(header[68:], firstDIFAT)
	binary.LittleEndian.PutUint32(header[72:], uint32(difatSectors))
	for i := 0; i < 109; i++ {
		v := mysqlDataCFBFree
		if i < fatSectors {
			v = uint32(dataSectors + 1 + i)
		}
		binary.LittleEndian.PutUint32(header[76+4*i:], v)
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := book.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.CopyN(w, book, bookSize); err != nil {
		return err
	}
	padding := int64(dataSectors)*512 - bookSize
	if padding > 0 {
		if _, err := io.CopyN(w, mysqlDataZeroReader{}, padding); err != nil {
			return err
		}
	}
	directory := make([]byte, 512)
	entry := func(dst []byte, name string, kind byte, child, start uint32, size uint64) {
		u := utf16.Encode([]rune(name + "\x00"))
		for i, ch := range u {
			binary.LittleEndian.PutUint16(dst[i*2:], ch)
		}
		binary.LittleEndian.PutUint16(dst[64:], uint16(len(u)*2))
		dst[66], dst[67] = kind, 1
		binary.LittleEndian.PutUint32(dst[68:], mysqlDataCFBFree)
		binary.LittleEndian.PutUint32(dst[72:], mysqlDataCFBFree)
		binary.LittleEndian.PutUint32(dst[76:], child)
		binary.LittleEndian.PutUint32(dst[116:], start)
		binary.LittleEndian.PutUint64(dst[120:], size)
	}
	entry(directory[:128], "Root Entry", 5, 1, mysqlDataCFBEnd, 0)
	entry(directory[128:256], "Workbook", 2, mysqlDataCFBFree, 0, uint64(streamSize))
	if _, err := w.Write(directory); err != nil {
		return err
	}
	for sector := 0; sector < fatSectors; sector++ {
		data := make([]byte, 512)
		for j := 0; j < 128; j++ {
			index := sector*128 + j
			v := mysqlDataCFBFree
			switch {
			case index < dataSectors-1:
				v = uint32(index + 1)
			case index == dataSectors-1 || index == dataSectors:
				v = mysqlDataCFBEnd
			case index < dataSectors+1+fatSectors:
				v = 0xfffffffd
			case index < dataSectors+1+fatSectors+difatSectors:
				v = 0xfffffffc
			}
			binary.LittleEndian.PutUint32(data[j*4:], v)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	for sector := 0; sector < difatSectors; sector++ {
		data := make([]byte, 512)
		for j := 0; j < 127; j++ {
			index := 109 + sector*127 + j
			v := mysqlDataCFBFree
			if index < fatSectors {
				v = uint32(dataSectors + 1 + index)
			}
			binary.LittleEndian.PutUint32(data[j*4:], v)
		}
		next := mysqlDataCFBEnd
		if sector+1 < difatSectors {
			next = firstDIFAT + uint32(sector+1)
		}
		binary.LittleEndian.PutUint32(data[508:], next)
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return nil
}

type mysqlDataZeroReader struct{}

func (mysqlDataZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type mysqlDataCFB struct {
	input      io.ReaderAt
	size       int64
	sector     int
	fat        []uint32
	miniFat    []uint32
	miniStream *mysqlDataCFBStream
}
type mysqlDataCFBStream struct {
	cfb   *mysqlDataCFB
	chain []uint32
	size  int64
	mini  bool
}

func (s *mysqlDataCFBStream) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, mysqlsvc.ErrMySQLDataPage
	}
	if off >= s.size {
		return 0, io.EOF
	}
	want := len(p)
	p = p[:min(int64(len(p)), s.size-off)]
	n := 0
	unit := s.cfb.sector
	if s.mini {
		unit = 64
	}
	for len(p) > 0 {
		index := int(off / int64(unit))
		if index >= len(s.chain) {
			return n, io.ErrUnexpectedEOF
		}
		inside := int(off % int64(unit))
		part := min(len(p), unit-inside)
		position := int64(s.chain[index])*int64(unit) + int64(inside)
		var read int
		var err error
		if s.mini {
			read, err = s.cfb.miniStream.ReadAt(p[:part], position)
		} else {
			read, err = s.cfb.input.ReadAt(p[:part], int64(s.cfb.sector)+position)
		}
		n += read
		off += int64(read)
		p = p[read:]
		if err != nil {
			return n, err
		}
		if read != part {
			return n, io.ErrUnexpectedEOF
		}
	}
	if n < want {
		return n, io.EOF
	}
	return n, nil
}
func (c *mysqlDataCFB) chain(start uint32, fat []uint32, limit int) ([]uint32, error) {
	if start == mysqlDataCFBEnd {
		return nil, nil
	}
	chain := []uint32{}
	seen := map[uint32]bool{}
	for start != mysqlDataCFBEnd {
		if int(start) >= len(fat) || seen[start] || len(chain) >= limit {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		seen[start] = true
		chain = append(chain, start)
		start = fat[start]
	}
	return chain, nil
}
func mysqlDataOpenCFB(input io.ReaderAt, size int64) (*mysqlDataCFBStream, error) {
	h := make([]byte, 512)
	if _, err := input.ReadAt(h, 0); err != nil {
		return nil, err
	}
	if !bytes.Equal(h[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) || binary.LittleEndian.Uint16(h[28:]) != 0xfffe {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	major := binary.LittleEndian.Uint16(h[26:])
	shift := binary.LittleEndian.Uint16(h[30:])
	if major != 3 && major != 4 || major == 3 && shift != 9 || major == 4 && shift != 12 || binary.LittleEndian.Uint16(h[32:]) != 6 {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	unit := 1 << shift
	c := &mysqlDataCFB{input: input, size: size, sector: unit}
	maxSectors := int(size/int64(unit)) - 1
	if maxSectors <= 0 {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	nfat := int(binary.LittleEndian.Uint32(h[44:]))
	ndifat := int(binary.LittleEndian.Uint32(h[72:]))
	if nfat <= 0 || nfat > maxSectors || ndifat > maxSectors {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	ids := []uint32{}
	for i := 0; i < 109 && len(ids) < nfat; i++ {
		id := binary.LittleEndian.Uint32(h[76+i*4:])
		if id != mysqlDataCFBFree {
			ids = append(ids, id)
		}
	}
	difat := binary.LittleEndian.Uint32(h[68:])
	seenD := map[uint32]bool{}
	for count := 0; count < ndifat; count++ {
		if int(difat) >= maxSectors || seenD[difat] {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		seenD[difat] = true
		data := make([]byte, unit)
		if _, err := input.ReadAt(data, int64(unit)*(1+int64(difat))); err != nil {
			return nil, err
		}
		for i := 0; i < unit/4-1 && len(ids) < nfat; i++ {
			id := binary.LittleEndian.Uint32(data[i*4:])
			if id != mysqlDataCFBFree {
				ids = append(ids, id)
			}
		}
		difat = binary.LittleEndian.Uint32(data[unit-4:])
	}
	if len(ids) != nfat {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	seenF := map[uint32]bool{}
	for _, id := range ids {
		if int(id) >= maxSectors || seenF[id] {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		seenF[id] = true
		data := make([]byte, unit)
		if _, err := input.ReadAt(data, int64(unit)*(1+int64(id))); err != nil {
			return nil, err
		}
		for i := 0; i < unit; i += 4 {
			c.fat = append(c.fat, binary.LittleEndian.Uint32(data[i:]))
		}
	}
	dirs, err := c.chain(binary.LittleEndian.Uint32(h[48:]), c.fat, maxSectors)
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 || len(dirs) > 1024 {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	var rootStart, bookStart uint32
	var rootSize, bookSize int64
	found := false
	for _, id := range dirs {
		data := make([]byte, unit)
		if _, err := input.ReadAt(data, int64(unit)*(1+int64(id))); err != nil {
			return nil, err
		}
		for i := 0; i < unit; i += 128 {
			entry := data[i : i+128]
			n := int(binary.LittleEndian.Uint16(entry[64:]))
			if n < 2 || n > 64 || n%2 != 0 {
				continue
			}
			u := make([]uint16, n/2-1)
			for j := range u {
				u[j] = binary.LittleEndian.Uint16(entry[j*2:])
			}
			name := string(utf16.Decode(u))
			streamSize := binary.LittleEndian.Uint64(entry[120:])
			if major == 3 {
				streamSize &= math.MaxUint32
			}
			if streamSize > uint64(size) {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			if entry[66] == 5 {
				rootStart, rootSize = binary.LittleEndian.Uint32(entry[116:]), int64(streamSize)
			}
			if entry[66] == 2 && (name == "Workbook" || name == "Book") {
				if found {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				found = true
				bookStart, bookSize = binary.LittleEndian.Uint32(entry[116:]), int64(streamSize)
			}
		}
	}
	if !found || bookSize == 0 {
		return nil, errors.New("XLS workbook stream not found")
	}
	mini := bookSize < int64(binary.LittleEndian.Uint32(h[56:]))
	var chain []uint32
	if mini {
		rootChain, e := c.chain(rootStart, c.fat, maxSectors)
		if e != nil {
			return nil, e
		}
		c.miniStream = &mysqlDataCFBStream{cfb: c, chain: rootChain, size: rootSize}
		count := int(binary.LittleEndian.Uint32(h[64:]))
		if count > maxSectors {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		mchain, e := c.chain(binary.LittleEndian.Uint32(h[60:]), c.fat, maxSectors)
		if e != nil || len(mchain) != count {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		for _, id := range mchain {
			data := make([]byte, unit)
			if _, e = input.ReadAt(data, int64(unit)*(1+int64(id))); e != nil {
				return nil, e
			}
			for i := 0; i < unit; i += 4 {
				c.miniFat = append(c.miniFat, binary.LittleEndian.Uint32(data[i:]))
			}
		}
		chain, err = c.chain(bookStart, c.miniFat, int(rootSize/64)+1)
	} else {
		chain, err = c.chain(bookStart, c.fat, maxSectors)
	}
	if err != nil {
		return nil, err
	}
	s := &mysqlDataCFBStream{cfb: c, chain: chain, size: bookSize, mini: mini}
	if int64(len(chain))*int64(unit) < bookSize && !mini || int64(len(chain))*64 < bookSize && mini {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	return s, nil
}

type mysqlDataBIFFRecordReader struct {
	input     io.ReaderAt
	off, size int64
	ended     bool
	cells     []*mysqlDataXLSCell
}

func (r *mysqlDataBIFFRecordReader) next() (uint16, []byte, error) {
	if r.off >= r.size {
		return 0, nil, io.EOF
	}
	h := make([]byte, 4)
	if _, err := r.input.ReadAt(h, r.off); err != nil {
		return 0, nil, err
	}
	r.off += 4
	n := int64(binary.LittleEndian.Uint16(h[2:]))
	if n > 8224 || r.off+n > r.size {
		return 0, nil, mysqlsvc.ErrMySQLDataPage
	}
	data := make([]byte, n)
	if _, err := r.input.ReadAt(data, r.off); err != nil {
		return 0, nil, err
	}
	r.off += n
	return binary.LittleEndian.Uint16(h), data, nil
}

// Shared strings are indexed in a private disk file. Loading an external XLS
// does not retain its potentially large SST or all worksheet cells in memory.
type mysqlDataXLSSST struct {
	strings *mysqlDataDiskStrings
}

func (s *mysqlDataXLSSST) close() error {
	if s == nil {
		return nil
	}
	return s.strings.close()
}
func (s *mysqlDataXLSSST) text(index uint32) (string, error) {
	return s.strings.text(int64(index))
}

type mysqlDataBIFFStringSegments struct {
	data []byte
	pos  int
	next func() ([]byte, error)
}

func (s *mysqlDataBIFFStringSegments) advance() error {
	data, err := s.next()
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return io.ErrUnexpectedEOF
	}
	s.data, s.pos = data, 0
	return nil
}

func (s *mysqlDataBIFFStringSegments) raw(n int) ([]byte, error) {
	out := make([]byte, 0, n)
	for n > 0 {
		if s.pos == len(s.data) {
			if err := s.advance(); err != nil {
				return nil, err
			}
		}
		p := s.data
		k := min(n, len(p)-s.pos)
		out = append(out, p[s.pos:s.pos+k]...)
		s.pos += k
		n -= k
	}
	return out, nil
}
func (s *mysqlDataBIFFStringSegments) text() (string, error) {
	header, err := s.raw(3)
	if err != nil {
		return "", err
	}
	chars := int(binary.LittleEndian.Uint16(header))
	if chars > 32767 {
		return "", mysqlsvc.ErrMySQLDataEditLimit
	}
	flags := header[2]
	runs, ext := 0, 0
	if flags&8 != 0 {
		b, e := s.raw(2)
		if e != nil {
			return "", e
		}
		runs = int(binary.LittleEndian.Uint16(b))
	}
	if flags&4 != 0 {
		b, e := s.raw(4)
		if e != nil {
			return "", e
		}
		ext = int(binary.LittleEndian.Uint32(b))
		if ext > mysqlsvc.MySQLDataExportFieldMaxBytes {
			return "", mysqlsvc.ErrMySQLDataEditLimit
		}
	}
	u := make([]uint16, 0, chars)
	wide := flags&1 != 0
	for chars > 0 {
		if s.pos == len(s.data) {
			if err := s.advance(); err != nil {
				return "", err
			}
			wide = s.data[0]&1 != 0
			s.pos = 1
		}
		width := 1
		if wide {
			width = 2
		}
		if len(s.data)-s.pos < width {
			return "", mysqlsvc.ErrMySQLDataPage
		}
		ch := uint16(s.data[s.pos])
		if wide {
			ch = binary.LittleEndian.Uint16(s.data[s.pos:])
		}
		u = append(u, ch)
		s.pos += width
		chars--
	}
	if _, err = s.raw(runs*4 + ext); err != nil {
		return "", err
	}
	if !mysqlDataValidUTF16(u) {
		return "", errors.New("XLS contains invalid Unicode")
	}
	return string(utf16.Decode(u)), nil
}

func mysqlDataReadXLSSST(ctx context.Context, first []byte, records *mysqlDataBIFFRecordReader) (*mysqlDataXLSSST, error) {
	cursor := &mysqlDataBIFFStringSegments{data: first, next: func() ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, data, err := records.next()
		if err == nil && id != 0x003c {
			return nil, errors.New("XLS SST continuation is absent")
		}
		return data, err
	}}
	h, err := cursor.raw(8)
	if err != nil {
		return nil, err
	}
	count := binary.LittleEndian.Uint32(h[4:])
	if count > binary.LittleEndian.Uint32(h) {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	strings, err := newMySQLDataDiskStrings()
	if err != nil {
		return nil, err
	}
	s := &mysqlDataXLSSST{strings: strings}
	for i := uint32(0); i < count; i++ {
		if e := ctx.Err(); e != nil {
			s.close()
			return nil, e
		}
		text, e := cursor.text()
		if e != nil {
			s.close()
			return nil, e
		}
		e = s.strings.append(text)
		if e != nil {
			s.close()
			return nil, e
		}
	}
	return s, nil
}

type mysqlDataXLSRows struct {
	stream      *mysqlDataCFBStream
	reader      mysqlDataBIFFRecordReader
	kinds       *mysqlDataBIFFRecordReader
	sst         *mysqlDataXLSSST
	fields      []string
	date1904    bool
	formats     map[int]string
	xfs         []int
	pending     *mysqlDataXLSCell
	kindPending *mysqlDataXLSCell
	row         int
	lastRow     int64
	ready       []mysqlsvc.MySQLDataValue
	closed      bool
}
type mysqlDataXLSCell struct {
	row, column int
	value       mysqlsvc.MySQLDataValue
	xf          int
}

func mysqlDataOpenXLSRows(input io.ReaderAt, size int64) (r *mysqlDataXLSRows, err error) {
	return mysqlDataOpenXLSRowsOptions(context.Background(), input, size, mysqlDataSourceDefaults("XLS"))
}

func mysqlDataOpenXLSRowsOptions(ctx context.Context, input io.ReaderAt, size int64, options mysqlDataSourceOptions) (r *mysqlDataXLSRows, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stream, err := mysqlDataOpenCFB(input, size)
	if err != nil {
		return nil, err
	}
	r = &mysqlDataXLSRows{stream: stream, formats: map[int]string{}}
	owned := r
	defer func() {
		if err != nil && owned.sst != nil {
			owned.sst.close()
		}
	}()
	records := mysqlDataBIFFRecordReader{input: stream, size: stream.size}
	var sheets []int64
	kindOffset := int64(-1)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, data, e := records.next()
		if e != nil {
			return nil, e
		}
		switch id {
		case 0x0809:
			if len(data) < 4 || binary.LittleEndian.Uint16(data) != 0x0600 {
				return nil, errors.New("only BIFF8 XLS workbooks are supported")
			}
		case 0x002f:
			return nil, errors.New("encrypted XLS files are not supported")
		case 0x0022:
			if len(data) >= 2 {
				r.date1904 = binary.LittleEndian.Uint16(data) != 0
			}
		case 0x0085:
			if len(data) < 8 {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			offset := int64(binary.LittleEndian.Uint32(data))
			n := int(data[6])
			wide := data[7]&1 != 0
			name, e := mysqlDataBIFFPlainString(data[8:], n, wide)
			if e != nil {
				return nil, e
			}
			if data[5] == 0 {
				if name == "_NyaDBKinds" && data[4] == 1 {
					kindOffset = offset
				} else {
					sheets = append(sheets, offset)
				}
			}
		case 0x041e:
			if len(data) < 5 {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			n := int(binary.LittleEndian.Uint16(data[2:]))
			name, e := mysqlDataBIFFPlainString(data[5:], n, data[4]&1 != 0)
			if e != nil {
				return nil, e
			}
			r.formats[int(binary.LittleEndian.Uint16(data))] = name
		case 0x00e0:
			if len(data) < 4 {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			r.xfs = append(r.xfs, int(binary.LittleEndian.Uint16(data[2:])))
		case 0x00fc:
			if r.sst != nil {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			r.sst, e = mysqlDataReadXLSSST(ctx, data, &records)
			if e != nil {
				return nil, e
			}
		case 0x000a:
			goto globalsDone
		}
	}
globalsDone:
	if len(sheets) == 0 {
		return nil, errors.New("XLS contains no data worksheet")
	}
	if options.Sheet < 0 || options.Sheet >= len(sheets) {
		return nil, errors.New("XLS selected worksheet is absent")
	}
	if sheets[options.Sheet] < 0 || sheets[options.Sheet] >= stream.size {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	r.reader = mysqlDataBIFFRecordReader{input: stream, off: sheets[options.Sheet], size: stream.size}
	if kindOffset >= 0 && options.Sheet == 0 {
		r.kinds = &mysqlDataBIFFRecordReader{input: stream, off: kindOffset, size: stream.size}
	}
	headerIndex := max(int64(1), options.HeaderRow) - 1
	if headerIndex >= 65536 {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	header, err := r.readRow(ctx, int(headerIndex), false)
	if err != nil {
		return nil, err
	}
	for i, value := range header {
		if options.HeaderRow == 0 {
			r.fields = append(r.fields, fmt.Sprintf("Column%d", i+1))
			continue
		}
		if value.Null || value.Binary || value.Text == "" {
			return nil, errors.New("XLS first row must contain field names")
		}
		r.fields = append(r.fields, value.Text)
	}
	if err = mysqlDataRecordValuesValid(r.fields, make([]mysqlsvc.MySQLDataValue, len(r.fields))); err != nil {
		return nil, err
	}
	r.row = int(max(options.FirstRow, headerIndex+2)) - 1
	r.lastRow = options.LastRow
	if options.HeaderRow == 0 && options.FirstRow <= headerIndex+1 {
		r.ready = header
		r.row = int(headerIndex)
	}
	if r.kinds != nil {
		if options.HeaderRow == 1 {
			h, e := r.readRow(ctx, 0, true)
			if e != nil || len(h) != len(r.fields) {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			for i, v := range h {
				if v.Text != r.fields[i] {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
			}
		}
	}
	return r, nil
}
func mysqlDataBIFFPlainString(data []byte, chars int, wide bool) (string, error) {
	width := 1
	if wide {
		width = 2
	}
	if chars < 0 || chars*width > len(data) {
		return "", mysqlsvc.ErrMySQLDataPage
	}
	u := make([]uint16, chars)
	for i := range u {
		u[i] = uint16(data[i])
		if wide {
			u[i] = binary.LittleEndian.Uint16(data[i*2:])
		}
	}
	if !mysqlDataValidUTF16(u) {
		return "", errors.New("XLS contains invalid Unicode")
	}
	return string(utf16.Decode(u)), nil
}

func mysqlDataValidUTF16(units []uint16) bool {
	for i := 0; i < len(units); i++ {
		ch := units[i]
		if ch >= 0xdc00 && ch <= 0xdfff {
			return false
		}
		if ch < 0xd800 || ch > 0xdbff {
			continue
		}
		i++
		if i == len(units) || units[i] < 0xdc00 || units[i] > 0xdfff {
			return false
		}
	}
	return true
}
func (r *mysqlDataXLSRows) Fields() []string { return append([]string(nil), r.fields...) }
func (r *mysqlDataXLSRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.sst.close()
	r.stream, r.reader.input, r.kinds, r.sst, r.pending, r.kindPending, r.ready = nil, nil, nil, nil, nil, nil, nil
	return err
}
func (r *mysqlDataXLSRows) Next(ctx context.Context) ([]mysqlsvc.MySQLDataValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.closed || r.lastRow > 0 && int64(r.row+1) > r.lastRow {
		return nil, io.EOF
	}
	row := r.ready
	r.ready = nil
	if row == nil {
		var err error
		row, err = r.readRow(ctx, r.row, false)
		if err != nil {
			return nil, err
		}
	}
	if len(row) > len(r.fields) {
		return nil, mysqlsvc.ErrMySQLDataPage
	}
	for len(row) < len(r.fields) {
		row = append(row, mysqlsvc.MySQLDataValue{Null: true})
	}
	// Row zero of the companion stores field names. Even a headerless import
	// retains its binary/NULL markers for all subsequent physical rows.
	if r.kinds != nil && r.row > 0 {
		kinds, e := r.readRow(ctx, r.row, true)
		if e != nil || len(kinds) != len(row) {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		for i, k := range kinds {
			switch k.Text {
			case "n":
				row[i] = mysqlsvc.MySQLDataValue{Null: true}
			case "t":
				if row[i].Null {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
			case "b":
				if !strings.HasPrefix(row[i].Text, "0x") {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				b, e := hex.DecodeString(row[i].Text[2:])
				if e != nil {
					return nil, e
				}
				row[i] = mysqlsvc.MySQLDataValue{Binary: true, Bytes: b}
			default:
				return nil, mysqlsvc.ErrMySQLDataPage
			}
		}
	}
	if err := mysqlDataRecordValuesValid(r.fields, row); err != nil {
		return nil, err
	}
	r.row++
	return row, nil
}
func (r *mysqlDataXLSRows) readRow(ctx context.Context, want int, kind bool) ([]mysqlsvc.MySQLDataValue, error) {
	reader := &r.reader
	pending := &r.pending
	if kind {
		reader = r.kinds
		pending = &r.kindPending
	}
	var row []mysqlsvc.MySQLDataValue
	seen := map[int]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cell := *pending
		*pending = nil
		if cell == nil {
			c, err := r.nextCell(reader)
			if err == io.EOF {
				if len(row) == 0 {
					return nil, io.EOF
				}
				return row, nil
			}
			if err != nil {
				return nil, err
			}
			cell = c
		}
		if cell.row > want {
			*pending = cell
			if len(row) == 0 {
				if len(r.fields) == 0 {
					return nil, errors.New("XLS selected header row is absent")
				}
				row = make([]mysqlsvc.MySQLDataValue, len(r.fields))
				for i := range row {
					row[i].Null = true
				}
			}
			return row, nil
		}
		if cell.row < want {
			continue
		}
		if cell.column < 0 || cell.column >= 128 || seen[cell.column] {
			return nil, mysqlsvc.ErrMySQLDataPage
		}
		seen[cell.column] = true
		for len(row) <= cell.column {
			row = append(row, mysqlsvc.MySQLDataValue{Null: true})
		}
		row[cell.column] = cell.value
	}
}
func (r *mysqlDataXLSRows) nextCell(records *mysqlDataBIFFRecordReader) (*mysqlDataXLSCell, error) {
	for {
		if len(records.cells) > 0 {
			c := records.cells[0]
			records.cells = records.cells[1:]
			return c, nil
		}
		if records.ended {
			return nil, io.EOF
		}
		id, data, err := records.next()
		if err != nil {
			return nil, err
		}
		if id == 0x000a {
			records.ended = true
			return nil, io.EOF
		}
		if id == 0x0006 || id == 0x0206 || id == 0x0406 {
			return nil, errors.New("XLS formula cells require an explicit value-only export")
		}
		switch id {
		case 0x00fd, 0x0204, 0x0203, 0x027e, 0x0205, 0x0201:
			if len(data) < 6 {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			cell := &mysqlDataXLSCell{row: int(binary.LittleEndian.Uint16(data)), column: int(binary.LittleEndian.Uint16(data[2:])), xf: int(binary.LittleEndian.Uint16(data[4:]))}
			var number *float64
			switch id {
			case 0x00fd:
				if len(data) < 10 || r.sst == nil {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				text, e := r.sst.text(binary.LittleEndian.Uint32(data[6:]))
				if e != nil {
					return nil, e
				}
				cell.value.Text = text
			case 0x0204:
				if len(data) < 9 {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				text, e := mysqlDataBIFFPlainString(data[9:], int(binary.LittleEndian.Uint16(data[6:])), data[8]&1 != 0)
				if e != nil {
					return nil, e
				}
				cell.value.Text = text
			case 0x0203:
				if len(data) < 14 {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				v := math.Float64frombits(binary.LittleEndian.Uint64(data[6:]))
				number = &v
			case 0x027e:
				if len(data) < 10 {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				v := mysqlDataBIFFRK(binary.LittleEndian.Uint32(data[6:]))
				number = &v
			case 0x0205:
				if len(data) < 8 {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				if data[7] != 0 {
					return nil, errors.New("XLS error cell")
				}
				cell.value.Text = strconv.Itoa(int(data[6]))
			case 0x0201:
				cell.value.Null = true
			}
			if number != nil {
				if math.IsNaN(*number) || math.IsInf(*number, 0) {
					return nil, mysqlsvc.ErrMySQLDataPage
				}
				cell.value.Text = strconv.FormatFloat(*number, 'g', -1, 64)
				if cell.xf >= 0 && cell.xf < len(r.xfs) && mysqlDataXLSXDateFormat(r.xfs[cell.xf], r.formats[r.xfs[cell.xf]]) {
					text, e := mysqlDataXLSXDate(cell.value.Text, r.date1904)
					if e != nil {
						return nil, e
					}
					cell.value.Text = text
				}
			}
			return cell, nil
		case 0x00bd, 0x00be:
			if len(data) < 8 {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			first, last := int(binary.LittleEndian.Uint16(data[2:])), int(binary.LittleEndian.Uint16(data[len(data)-2:]))
			if last < first || last >= 128 {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			count := last - first + 1
			stride := 6
			if id == 0x00be {
				stride = 2
			}
			if len(data) != 6+stride*count {
				return nil, mysqlsvc.ErrMySQLDataPage
			}
			for i := 0; i < count; i++ {
				xf := int(binary.LittleEndian.Uint16(data[4+i*stride:]))
				cell := &mysqlDataXLSCell{row: int(binary.LittleEndian.Uint16(data)), column: first + i, xf: xf}
				if id == 0x00be {
					cell.value.Null = true
				} else {
					n := mysqlDataBIFFRK(binary.LittleEndian.Uint32(data[6+i*stride:]))
					cell.value.Text = strconv.FormatFloat(n, 'g', -1, 64)
					if xf < len(r.xfs) && mysqlDataXLSXDateFormat(r.xfs[xf], r.formats[r.xfs[xf]]) {
						text, e := mysqlDataXLSXDate(cell.value.Text, r.date1904)
						if e != nil {
							return nil, e
						}
						cell.value.Text = text
					}
				}
				records.cells = append(records.cells, cell)
			}
		}
	}
}
func mysqlDataBIFFRK(raw uint32) float64 {
	var value float64
	if raw&2 != 0 {
		value = float64(int32(raw) >> 2)
	} else {
		value = math.Float64frombits(uint64(raw&0xfffffffc) << 32)
	}
	if raw&1 != 0 {
		value /= 100
	}
	return value
}
func mysqlDataParseXLSImport(data []byte) (*mysqlDataImportInput, error) {
	r, err := mysqlDataOpenXLSRows(bytes.NewReader(data), int64(len(data)))
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
