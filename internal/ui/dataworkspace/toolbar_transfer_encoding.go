package dataworkspace

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding"
)

// Decode one source character at a time so malformed source sequences fail
// instead of becoming replacement characters in a later immutable preview.
// Buffered input keeps memory independent of the source length.
type mysqlDataStrictTextReader struct {
	input       *bufio.Reader
	name        string
	decoder     *encoding.Decoder
	replacement []byte
	pending     []byte
	buffer      [utf8.UTFMax]byte
	first       bool
	little      bool
	err         error
}

func newMySQLDataStrictTextReader(input io.Reader, name string) (io.Reader, error) {
	enc, err := mysqlDataTextEncoding(name)
	if err != nil {
		return nil, err
	}
	r := &mysqlDataStrictTextReader{input: bufio.NewReader(input), name: name, first: true, little: name == "UTF-16LE"}
	if enc != nil {
		r.decoder = enc.NewDecoder()
		r.replacement, _ = enc.NewEncoder().Bytes([]byte("\ufffd"))
	}
	return r, nil
}
func (r *mysqlDataStrictTextReader) Read(out []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n := 0
	for n < len(out) {
		if len(r.pending) > 0 {
			size := copy(out[n:], r.pending)
			n += size
			r.pending = r.pending[size:]
			continue
		}
		value, err := r.character()
		if err != nil {
			r.err = err
			return n, err
		}
		if r.first {
			r.first = false
			if value == '\ufeff' {
				continue
			}
		}
		r.pending = utf8.AppendRune(r.buffer[:0], value)
	}
	return n, nil
}
func (r *mysqlDataStrictTextReader) character() (rune, error) {
	invalid := errors.New("invalid source character encoding")
	if r.name == "" || r.name == "UTF-8" {
		value, size, err := r.input.ReadRune()
		if err != nil {
			return 0, err
		}
		if value == utf8.RuneError && size == 1 {
			return 0, invalid
		}
		return value, nil
	}
	if r.name == "UTF-16LE" || r.name == "UTF-16BE" {
		var unit [2]byte
		if _, err := io.ReadFull(r.input, unit[:]); err != nil {
			return 0, err
		}
		read := func() uint16 {
			if r.little {
				return binary.LittleEndian.Uint16(unit[:])
			}
			return binary.BigEndian.Uint16(unit[:])
		}
		value := read()
		if r.first && value == 0xfffe {
			r.little = !r.little
			value = 0xfeff
		}
		if value >= 0xdc00 && value <= 0xdfff {
			return 0, invalid
		}
		if value >= 0xd800 && value <= 0xdbff {
			if _, err := io.ReadFull(r.input, unit[:]); err != nil {
				return 0, err
			}
			second := read()
			if second < 0xdc00 || second > 0xdfff {
				return 0, invalid
			}
			return utf16.DecodeRune(rune(value), rune(second)), nil
		}
		return rune(value), nil
	}
	first, err := r.input.ReadByte()
	if err != nil {
		return 0, err
	}
	var unit [4]byte
	unit[0] = first
	size := 1
	if r.name == "GB18030" && first >= 0x81 && first <= 0xfe {
		second, err := r.input.ReadByte()
		if err != nil {
			return 0, io.ErrUnexpectedEOF
		}
		unit[1] = second
		size = 2
		if second >= 0x30 && second <= 0x39 {
			if _, err = io.ReadFull(r.input, unit[2:]); err != nil {
				return 0, err
			}
			size = 4
			if unit[2] < 0x81 || unit[2] > 0xfe || unit[3] < 0x30 || unit[3] > 0x39 {
				return 0, invalid
			}
		} else if second < 0x40 || second > 0xfe || second == 0x7f {
			return 0, invalid
		}
	} else if r.name == "Shift_JIS" && (first >= 0x81 && first <= 0x9f || first >= 0xe0 && first <= 0xfc) {
		second, err := r.input.ReadByte()
		if err != nil {
			return 0, io.ErrUnexpectedEOF
		}
		unit[1] = second
		size = 2
		if second < 0x40 || second > 0xfc || second == 0x7f {
			return 0, invalid
		}
	}
	var output [utf8.UTFMax]byte
	r.decoder.Reset()
	written, consumed, err := r.decoder.Transform(output[:], unit[:size], true)
	if err != nil {
		return 0, err
	}
	if consumed != size {
		return 0, invalid
	}
	decoded := output[:written]
	value, count := utf8.DecodeRune(decoded)
	if count != len(decoded) || value == utf8.RuneError && !bytes.Equal(unit[:size], r.replacement) {
		return 0, invalid
	}
	return value, nil
}
