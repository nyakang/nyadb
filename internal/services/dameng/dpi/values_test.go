package dpi

import (
	"math"
	"strings"
	"testing"
)

func TestDecimalPreservesAllDigitsAndScale(t *testing.T) {
	for _, text := range []string{
		"123456789012345678.12345678901234567891",
		"-123456789012345678.12345678901234567891",
		"99999999999999999999999999999999999999",
		"0.00000000000000000000000000000000000001",
		"0.00000000000000000000000000000000000000",
		"-0.10000000000000000000", "42", "0",
	} {
		n, err := encodeDecimal(text)
		if err != nil {
			t.Fatal(text, err)
		}
		actual, err := decodeDecimal(n)
		if err != nil || actual != text {
			t.Fatalf("exact %s became %s: %v", text, actual, err)
		}
	}
	for _, text := range []string{"", " ", ".1", "1.", "1e3", "--1", "NaN", "1.2.3", strings.Repeat("9", 39), "0." + strings.Repeat("0", 38) + "1"} {
		if _, err := encodeDecimal(text); err == nil {
			t.Fatalf("unsupported decimal accepted: %q", text)
		}
	}
	for _, n := range []numeric{{Precision: 39, Sign: 1}, {Precision: 1, Sign: 2}, {Precision: 1, Sign: 1, Scale: 39}} {
		if _, err := decodeDecimal(n); err == nil {
			t.Fatal("invalid native decimal accepted")
		}
	}
}

func TestParametersRejectUnsupportedValuesAndOwnTheirBytes(t *testing.T) {
	source := []byte{0, 1, 128, 255}
	b, err := newBinding(source)
	if err != nil {
		t.Fatal(err)
	}
	defer b.release()
	source[0] = 42
	if b.bytes[0] != 0 || b.length != 4 {
		t.Fatal("native binary binding shares caller memory")
	}
	for _, v := range []any{nil, "", []byte{}} {
		b, err := newBinding(v)
		if err != nil {
			t.Fatal(err)
		}
		if v == nil && b.length != -1 || v != nil && b.length != 0 {
			t.Fatal("NULL and empty binding conflated")
		}
		b.release()
	}
	for _, v := range []any{math.NaN(), math.Inf(1), struct{}{}, []byte(strings.Repeat("x", (1<<20)+1)), Decimal("1e3")} {
		if b, err := newBinding(v); err == nil {
			b.release()
			t.Fatal("unsupported binding accepted")
		}
	}
}

func TestConnectorRejectsUnverifiedRemoteTransportAndInvalidIdentity(t *testing.T) {
	for _, o := range []Options{
		{Host: "192.0.2.1", Username: "user"}, {Host: "localhost", Username: "user"},
		{Host: "127.0.0.1:5236", Username: "user"}, {Host: "127.0.0.1", Username: "user\x00"},
		{Host: "127.0.0.1", Username: "user", ConnectTimeoutSeconds: -1},
		{Host: "127.0.0.1", Username: "user", OperationTimeoutSeconds: 121},
	} {
		if _, err := NewConnector(o, "temporary-password"); err == nil {
			t.Fatal("unverified transport/identity accepted")
		}
	}
}
