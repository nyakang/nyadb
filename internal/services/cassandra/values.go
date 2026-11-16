package cassandra

import (
	"bytes"
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func escapeOption(v string) string { return url.QueryEscape(v) }

var cqlInteger = regexp.MustCompile(`^[+-]?[0-9]+$`)

// DecodeValue uses the original column type to interpret the native legacy
// string/NULL contract. Complex CQL literals stay complete text, with their
// database type retained on the result column; no structured edit is inferred.
func DecodeValue(typ string, raw json.RawMessage) (contract.Cell, error) {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return contract.Cell{Kind: contract.Null}, nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return contract.Cell{}, ErrValue
	}
	typ = strings.ToLower(strings.TrimSpace(typ))
	switch typ {
	case "varint", "counter":
		if !cqlInteger.MatchString(text) {
			return contract.Cell{}, ErrValue
		}
		return contract.Cell{Kind: contract.Integer, Text: text}, nil
	case "ascii", "varchar", "timeuuid":
		return contract.Cell{Kind: contract.Text, Text: text}, nil
	case "inet":
		if net.ParseIP(text) == nil {
			return contract.Cell{}, ErrValue
		}
		return contract.Cell{Kind: contract.Text, Text: text}, nil
	case "duration":
		return contract.Cell{Kind: contract.Text, Text: text}, nil
	}
	for _, prefix := range []string{"list<", "set<", "map<", "tuple<", "frozen<", "vector<"} {
		if strings.HasPrefix(typ, prefix) && strings.HasSuffix(typ, ">") {
			return contract.Cell{Kind: contract.Text, Text: text}, nil
		}
	}
	cell, err := agentbridge.DecodeStandardValue(typ, raw)
	if err != nil {
		return contract.Cell{}, ErrValue
	}
	return cell, nil
}
