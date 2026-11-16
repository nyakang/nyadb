package databend

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

// DecodeJDBCValue retains the pinned original agent's exact scalar/JSON text.
// Databend's native binary type is emitted as uppercase hex without a 0x
// prefix. Generic ARRAY metadata does not supply editable element types; its
// JSON projection must not be used to invent such a schema.
func DecodeJDBCValue(declaration string, raw json.RawMessage) (contract.Cell, error) {
	raw = bytes.TrimSpace(raw)
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return contract.Cell{}, agentbridge.ErrNativeResult
	}
	if bytes.Equal(raw, []byte("null")) {
		return contract.Cell{Kind: contract.Null}, nil
	}
	base := strings.ToLower(strings.TrimSpace(declaration))
	if strings.HasPrefix(base, "uint") || strings.HasPrefix(base, "int") {
		unsigned := strings.HasPrefix(base, "uint")
		sizeText := strings.TrimPrefix(base, "int")
		if unsigned {
			sizeText = strings.TrimPrefix(base, "uint")
		}
		size, err := strconv.Atoi(sizeText)
		if err != nil || size != 8 && size != 16 && size != 32 && size != 64 {
			return contract.Cell{}, agentbridge.ErrNativeResult
		}
		// The original JDBC native integers are JSON numbers, never rounded
		// floating point values or strings reconstructed from a display grid.
		text := string(raw)
		if unsigned {
			_, err = strconv.ParseUint(text, 10, size)
		} else {
			_, err = strconv.ParseInt(text, 10, size)
		}
		if err != nil {
			return contract.Cell{}, agentbridge.ErrNativeResult
		}
		return contract.Cell{Kind: contract.Integer, Text: text}, nil
	}
	switch base {
	case "decimal":
		if len(raw) == 0 || raw[0] == '"' {
			return contract.Cell{}, agentbridge.ErrNativeResult
		}
		return agentbridge.DecodeStandardValue("decimal", raw)
	case "boolean":
		if !bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false")) {
			return contract.Cell{}, agentbridge.ErrNativeResult
		}
		return agentbridge.DecodeStandardValue("boolean", raw)
	case "float32", "float64":
		return agentbridge.DecodeStandardValue("double", raw)
	case "string", "binary", "array", "variant", "date", "timestamp":
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return contract.Cell{}, agentbridge.ErrNativeResult
		}
		switch base {
		case "binary":
			value, err := hex.DecodeString(text)
			if err != nil {
				return contract.Cell{}, agentbridge.ErrNativeResult
			}
			return contract.Cell{Kind: contract.Binary, Bytes: value}, nil
		case "array", "variant":
			value := []byte(text)
			if !utf8.Valid(value) || !json.Valid(value) || base == "array" && (len(bytes.TrimSpace(value)) == 0 || bytes.TrimSpace(value)[0] != '[') {
				return contract.Cell{}, agentbridge.ErrNativeResult
			}
			return contract.Cell{Kind: contract.JSON, Text: text}, nil
		case "date", "timestamp":
			return agentbridge.DecodeStandardValue(base, raw)
		default:
			return contract.Cell{Kind: contract.Text, Text: text}, nil
		}
	default:
		return contract.Cell{}, agentbridge.ErrNativeResult
	}
}
