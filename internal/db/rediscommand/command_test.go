package rediscommand

import (
	"reflect"
	"testing"
)

func TestBinaryQuotedCommandsAndCaret(t *testing.T) {
	for _, c := range []struct {
		text string
		want []string
	}{
		{`SET "a b" "x\x00\xff"`, []string{"SET", "a b", "x\x00\xff"}},
		{`["SET",{"base64":"AP8="},""]`, []string{"SET", "\x00\xff", ""}},
		{`SET '' 'a b'`, []string{"SET", "", "a b"}},
	} {
		got, e := Parse(c.text)
		if e != nil || !reflect.DeepEqual(got, c.want) {
			t.Fatalf("parse failed: %v", e)
		}
	}
	for _, bad := range []string{"", `[1]`, `["PING"] {}`, `SET "unfinished`, "PING\nSET x y", `["SET",{"base64":"!"}]`, `["SET",{}]`, `["SET",null]`, `["SET",{"base64":"","base64":"YQ=="}]`} {
		if _, e := Parse(bad); e == nil {
			t.Fatal("invalid command accepted")
		}
	}
	text := "PING\nGET x\n"
	if StatementAt(text, 7) != "GET x" || StatementAt(`["GET","x"]`, 2) != `["GET","x"]` {
		t.Fatal("caret did not select one command")
	}
}

func TestRedisFormatPreservesBinaryArguments(t *testing.T) {
	formatted, e := Format(`SET "\x00\xff" ""`)
	if e != nil {
		t.Fatal(e)
	}
	args, e := Parse(formatted)
	if e != nil || args[1] != "\x00\xff" || args[2] != "" {
		t.Fatal("format changed Redis bytes", e)
	}
}
