package jsondocument

import (
	"strings"
	"testing"
)

func TestImportPreservesObjectsExactNumbersAndRejectsMalformedSource(t *testing.T) {
	for _, source := range []struct {
		format, raw string
		count       int
	}{
		{"JSON", `[{"id":9007199254740993,"nested":{"x":[null,true,1.25]},"猫":"🐱"},{"id":"two","other":{}}]`, 2},
		{"JSON", ` {"id":9007199254740993} `, 1},
		{"JSONL", "\n{\"id\":9007199254740993}\r\n\n{\"id\":\"two\"}\n", 2},
	} {
		docs, err := ParseImport(strings.NewReader(source.raw), source.format)
		if err != nil || len(docs) != source.count || !strings.Contains(string(docs[0]), "9007199254740993") {
			t.Fatal(docs, err)
		}
	}
	for _, source := range []string{`[]`, `null`, `[null]`, `[1]`, `[{"id":1,"id":2}]`, `[{"id":1,"x":{"a":1,"a":2}}]`, `{"id":1,"x":"\ud800"}`, `{"id":1} {"id":2}`, "{\"id\":\"\xff\"}"} {
		if docs, err := ParseImport(strings.NewReader(source), "JSON"); err == nil {
			t.Fatal("invalid accepted", docs)
		}
	}
	for _, source := range []string{"[{\"id\":1}]\n", "{\"id\":1}\n{bad}\n", "{\"id\":1} {\"id\":2}\n"} {
		if _, err := ParseImport(strings.NewReader(source), "JSONL"); err == nil {
			t.Fatal("invalid line accepted")
		}
	}
	for _, source := range []string{strings.Repeat(" ", ImportMaxBytes+1), `{"id":1,"x":"` + strings.Repeat("a", 1<<20) + `"}`, "[" + strings.Repeat(`{"id":1},`, 10000) + `{"id":2}]`, strings.Repeat("[", 66) + "1" + strings.Repeat("]", 66)} {
		if _, err := ParseImport(strings.NewReader(source), "JSON"); err == nil {
			t.Fatal("bounds ignored")
		}
	}
}
