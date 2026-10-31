package sqltext

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// ScriptScanner recognizes MySQL client DELIMITER lines and SQL quotes and
// comments. It reads one bounded statement at a time, retaining its original
// SQL. DELIMITER is a client directive and is never sent to the database.
type ScriptScanner struct {
	reader            *bufio.Reader
	delimiter         string
	MaxStatementBytes int
	BackslashEscapes  bool
	lineStart         bool
}

func NewScriptScanner(r io.Reader) *ScriptScanner {
	return &ScriptScanner{reader: bufio.NewReader(r), delimiter: ";", MaxStatementBytes: 8 << 20, BackslashEscapes: true, lineStart: true}
}
func (s *ScriptScanner) Next() (string, error) {
	var b strings.Builder
	state := byte(0)
	code := false
	write := func(v byte) error {
		if b.Len() >= s.MaxStatementBytes {
			return errors.New("SQL statement exceeds configured byte limit")
		}
		b.WriteByte(v)
		return nil
	}
	for {
		peek, e := s.reader.Peek(1)
		if e == io.EOF {
			if state == '\'' || state == '"' || state == '`' || state == '*' {
				return "", errors.New("unterminated SQL quote or block comment")
			}
			if !code {
				return "", io.EOF
			}
			return strings.TrimSpace(b.String()), nil
		}
		if e != nil {
			return "", e
		}
		ch := peek[0]
		if state == 0 {
			if s.lineStart && !code {
				if ch == ' ' || ch == '\t' || ch == '\r' {
					s.reader.ReadByte()
					if e = write(ch); e != nil {
						return "", e
					}
					continue
				}
				word, _ := s.reader.Peek(10)
				if len(word) >= 10 && strings.EqualFold(string(word[:9]), "DELIMITER") && word[9] <= ' ' {
					line, e := s.reader.ReadSlice('\n')
					if e != nil && e != io.EOF {
						return "", e
					}
					if len(line) > 128 {
						return "", errors.New("DELIMITER directive exceeds 128 bytes")
					}
					parts := strings.Fields(string(line))
					if len(parts) != 2 || len(parts[1]) > 16 || strings.ContainsAny(parts[1], "'\"`\\") {
						return "", errors.New("invalid DELIMITER directive")
					}
					s.delimiter = parts[1]
					b.Reset()
					s.lineStart = true
					continue
				}
			}
			delim, _ := s.reader.Peek(len(s.delimiter))
			if string(delim) == s.delimiter {
				s.reader.Discard(len(s.delimiter))
				sql := strings.TrimSpace(b.String())
				b.Reset()
				s.lineStart = false
				if code {
					return sql, nil
				}
				continue
			}
			pair, _ := s.reader.Peek(3)
			if ch == '#' {
				state = 'L'
			} else if len(pair) >= 2 && pair[0] == '-' && pair[1] == '-' && (len(pair) == 2 || pair[2] <= ' ') {
				state = 'L'
			} else if len(pair) >= 2 && pair[0] == '/' && pair[1] == '*' {
				state = '*'
				if len(pair) >= 3 && pair[2] == '!' {
					code = true
				}
				s.reader.Discard(2)
				if e = write('/'); e != nil {
					return "", e
				}
				if e = write('*'); e != nil {
					return "", e
				}
				s.lineStart = false
				continue
			} else if ch == '\'' || ch == '"' || ch == '`' {
				state = ch
				code = true
			} else if ch > ' ' {
				code = true
			}
			s.reader.ReadByte()
			if e = write(ch); e != nil {
				return "", e
			}
			s.lineStart = ch == '\n'
			continue
		}
		s.reader.ReadByte()
		if e = write(ch); e != nil {
			return "", e
		}
		if state == 'L' {
			if ch == '\n' {
				state = 0
				s.lineStart = true
			}
			continue
		}
		if state == '*' {
			if ch == '*' {
				next, _ := s.reader.Peek(1)
				if len(next) > 0 && next[0] == '/' {
					s.reader.ReadByte()
					if e = write('/'); e != nil {
						return "", e
					}
					state = 0
				}
			}
			continue
		}
		if ch == '\\' && s.BackslashEscapes {
			next, e := s.reader.ReadByte()
			if e == io.EOF {
				return "", errors.New("unterminated SQL escape")
			}
			if e != nil {
				return "", e
			}
			if e = write(next); e != nil {
				return "", e
			}
			continue
		}
		if ch == state {
			next, _ := s.reader.Peek(1)
			if len(next) > 0 && next[0] == state {
				s.reader.ReadByte()
				if e = write(state); e != nil {
					return "", e
				}
			} else {
				state = 0
			}
		}
	}
}
