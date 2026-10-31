package mysqlddl

import (
	"errors"
	"strings"

	"github.com/nyakang/nyadb/internal/db/sqltext"
)

type option struct {
	name, value string
	start, end  int
}

func options(raw string) ([]option, error) {
	ts, e := sqltext.Tokens(raw)
	if e != nil {
		return nil, e
	}
	var out []option
	for i := 0; i < len(ts); i++ {
		if ts[i].Quoted || ts[i].Comment {
			continue
		}
		start := i
		name := strings.ToUpper(ts[i].Text)
		if name == "PARTITION" {
			break
		}
		if name == "DEFAULT" && i+1 < len(ts) && sqltext.Word(ts[i+1], "CHARSET") {
			i++
			name = "CHARSET"
		}
		if name != "ENGINE" && name != "CHARSET" && name != "COLLATE" && name != "COMMENT" && name != "AUTO_INCREMENT" {
			continue
		}
		if i+2 >= len(ts) || ts[i+1].Text != "=" {
			return nil, errors.New("unsupported table option syntax")
		}
		i += 2
		if name == "COMMENT" && !strings.HasPrefix(ts[i].Text, "'") {
			return nil, errors.New("table comment must be a string literal")
		}
		out = append(out, option{name, ts[i].Text, ts[start].Start, ts[i].End})
	}
	return out, nil
}
func Option(raw, name string) string {
	xs, _ := options(raw)
	for _, x := range xs {
		if x.name == name {
			if name == "COMMENT" {
				return decodeLiteral(x.value)
			}
			return x.value
		}
	}
	return ""
}
func SetOption(raw, name, value string) (string, error) {
	if name != "ENGINE" && name != "CHARSET" && name != "COLLATE" && name != "COMMENT" && name != "AUTO_INCREMENT" {
		return "", errors.New("unsupported table option")
	}
	if name == "COMMENT" {
		value = Literal(value)
	} else {
		ts, e := sqltext.Tokens(value)
		if e != nil || len(ts) != 1 || ts[0].Quoted || ts[0].Comment {
			return "", errors.New("invalid table option value")
		}
	}
	xs, e := options(raw)
	if e != nil {
		return "", e
	}
	for _, x := range xs {
		if x.name == name {
			return raw[:x.start] + name + "=" + value + raw[x.end:], nil
		}
	}
	// Append before an unknown suffix such as PARTITION BY, rather than
	// modifying or dropping that suffix.
	ts, _ := sqltext.Tokens(raw)
	at := len(raw)
	for _, t := range ts {
		if sqltext.Word(t, "PARTITION") {
			at = t.Start
			break
		}
	}
	return strings.TrimSpace(raw[:at]) + " " + name + "=" + value + " " + raw[at:], nil
}
func optionDiff(old, now string) (string, error) {
	if old == now {
		return "", nil
	}
	a, e := options(old)
	if e != nil {
		return "", e
	}
	b, e := options(now)
	if e != nil {
		return "", e
	}
	strip := func(raw string, xs []option) string {
		for i := len(xs) - 1; i >= 0; i-- {
			raw = raw[:xs[i].start] + raw[xs[i].end:]
		}
		return Canonical(raw)
	}
	if strip(old, a) != strip(now, b) {
		return "", errors.New("unrecognized table options and partition definitions must remain unchanged")
	}
	original := map[string]string{}
	for _, x := range a {
		original[x.name] = x.value
	}
	var changed []string
	for _, x := range b {
		if Canonical(original[x.name]) != Canonical(x.value) {
			changed = append(changed, x.name+"="+x.value)
		}
		delete(original, x.name)
	}
	if len(original) > 0 {
		return "", errors.New("table options cannot be removed implicitly")
	}
	return strings.Join(changed, " "), nil
}
func verifyOptions(want, got string, allowDefaults bool) error {
	xs, e := options(want)
	if e != nil {
		return e
	}
	for _, x := range xs {
		if x.name == "AUTO_INCREMENT" {
			continue
		}
		if x.name == "COMMENT" {
			if decodeLiteral(x.value) != Option(got, x.name) {
				return errors.New("reloaded table comment differs")
			}
		} else if !strings.EqualFold(x.value, Option(got, x.name)) {
			return errors.New("reloaded table option differs: " + x.name)
		}
	}
	// Ignore the server's advancing auto-increment counter only.
	names := map[string]bool{}
	for _, x := range xs {
		names[x.name] = true
	}
	normalize := func(raw string) string {
		opts, _ := options(raw)
		for i := len(opts) - 1; i >= 0; i-- {
			if opts[i].name == "AUTO_INCREMENT" || allowDefaults && !names[opts[i].name] {
				raw = raw[:opts[i].start] + raw[opts[i].end:]
			}
		}
		return Canonical(raw)
	}
	if normalize(want) != normalize(got) {
		return errors.New("reloaded table options contain a definition difference")
	}
	return nil
}
