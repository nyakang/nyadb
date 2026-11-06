package engines

import (
	"errors"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

// pg_sequence.seqcache reports 1 even for CACHE 7 on CockroachDB 25.2.
// Parse the server's native definition so no compatibility placeholder becomes
// an advertised sequence setting. The full definition remains unchanged.
func cockroachSequence(ddl string) (*contract.SequenceInfo, error) {
	tokens, err := sqltext.Tokens(ddl)
	if err != nil || len(tokens) < 4 || !sqltext.Word(tokens[0], "CREATE") || !sqltext.Word(tokens[1], "SEQUENCE") {
		return nil, errors.New("invalid native sequence definition")
	}
	i := 3
	for i+1 < len(tokens) && tokens[i].Text == "." {
		i += 2
	}
	out := &contract.SequenceInfo{DataType: "bigint", Cache: 1, DefinitionFromServer: true}
	seen := map[string]bool{}
	for i < len(tokens) {
		key := strings.ToUpper(tokens[i].Text)
		if tokens[i].Quoted || tokens[i].Comment {
			return nil, errors.New("unexpected sequence option")
		}
		i++
		if key == ";" && i == len(tokens) {
			break
		}
		if key == "VIRTUAL" {
			continue
		}
		if key == "CYCLE" {
			out.Cycle = true
			continue
		}
		if key == "NO" && i < len(tokens) && sqltext.Word(tokens[i], "CYCLE") {
			out.Cycle = false
			i++
			continue
		}
		var target *int64
		switch key {
		case "MINVALUE":
			target = &out.Min
		case "MAXVALUE":
			target = &out.Max
		case "INCREMENT":
			target = &out.Increment
		case "START":
			target = &out.Start
		case "CACHE":
			target = &out.Cache
		default:
			return nil, errors.New("unrecognized native sequence option")
		}
		if seen[key] {
			return nil, errors.New("duplicate native sequence option")
		}
		seen[key] = true
		if i < len(tokens) && (sqltext.Word(tokens[i], "BY") || sqltext.Word(tokens[i], "WITH")) {
			i++
		}
		negative := ""
		if i < len(tokens) && (tokens[i].Text == "-" || tokens[i].Text == "+") {
			negative = tokens[i].Text
			i++
		}
		if i >= len(tokens) || tokens[i].Quoted || tokens[i].Comment {
			return nil, errors.New("missing sequence integer")
		}
		n, e := strconv.ParseInt(negative+tokens[i].Text, 10, 64)
		if e != nil {
			return nil, e
		}
		*target = n
		i++
	}
	if !seen["START"] || !seen["INCREMENT"] || !seen["MINVALUE"] || !seen["MAXVALUE"] || out.Increment == 0 || out.Min > out.Max || out.Start < out.Min || out.Start > out.Max || out.Cache < 1 {
		return nil, errors.New("incomplete native sequence settings")
	}
	return out, nil
}
