package jsondocument

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const ImportMaxBytes = 8 << 20

// ParseImport preserves complete objects, including heterogeneous fields and
// exact numbers. JSON accepts one object or array; JSONL requires one object on
// each nonempty line. No field coercion or truncation occurs.
func ParseImport(r io.Reader, format string) ([]json.RawMessage, error) {
	raw, err := io.ReadAll(io.LimitReader(r, ImportMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > ImportMaxBytes {
		return nil, ErrBounds
	}
	var documents []json.RawMessage
	switch strings.ToLower(format) {
	case "json":
		var source json.RawMessage
		if err = Decode(raw, &source, ImportMaxBytes); err != nil {
			return nil, err
		}
		if len(source) > 0 && source[0] == '[' {
			if json.Unmarshal(source, &documents) != nil {
				return nil, ErrInvalid
			}
		} else {
			documents = []json.RawMessage{source}
		}
	case "jsonl":
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 4096), contract.DataEditMaxBytes+1)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			documents = append(documents, bytes.Clone(line))
			if len(documents) > contract.MaxDataImportRows {
				return nil, ErrBounds
			}
		}
		if scanner.Err() != nil {
			return nil, ErrBounds
		}
	default:
		return nil, contract.ErrUnsupported
	}
	if len(documents) == 0 || len(documents) > contract.MaxDataImportRows {
		return nil, ErrBounds
	}
	total := 2
	for i, raw := range documents {
		var object map[string]json.RawMessage
		if err := Decode(raw, &object, contract.DataEditMaxBytes); err != nil {
			return nil, err
		}
		if object == nil || bytes.TrimSpace(raw)[0] != '{' {
			return nil, ErrInvalid
		}
		var compact bytes.Buffer
		if json.Compact(&compact, raw) != nil {
			return nil, ErrInvalid
		}
		documents[i] = bytes.Clone(compact.Bytes())
		total += len(documents[i]) + 1
		if total > ImportMaxBytes {
			return nil, ErrBounds
		}
	}
	return documents, nil
}
