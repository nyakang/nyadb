package manticore

import (
	"errors"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestWireValuesOwnBytesAndPreserveLargeIntegers(t *testing.T) {
	raw := []byte{0, 255, 1}
	c, e := cell(raw, "BLOB")
	if e != nil {
		t.Fatal(e)
	}
	raw[0] = 99
	if c.Bytes[0] != 0 {
		t.Fatal("borrowed bytes")
	}
	c, e = cell([]byte("9223372036854775807"), "BIGINT")
	if e != nil || c.Kind != contract.Integer || c.Text != "9223372036854775807" {
		t.Fatal(c, e)
	}
	n, _ := cell(nil, "CHAR")
	empty, _ := cell([]byte{}, "CHAR")
	if n.Kind != contract.Null || empty.Kind != contract.Text || empty.Text != "" {
		t.Fatal(n, empty)
	}
}
func TestScriptsValidateBeforeDispatchAndPreserveQuotedSemicolons(t *testing.T) {
	p, e := statements("# original\n SELECT '猫;abc'; INSERT INTO t (id) VALUES (1)")
	if e != nil || len(p) != 2 || !strings.Contains(p[0], "猫;abc") {
		t.Fatal(p, e)
	}
	if _, e = statements("INSERT INTO t (id) VALUES (1); SELECT 'unterminated"); e == nil {
		t.Fatal("partial script accepted")
	}
}
func TestServerErrorsRedactSecretsAndPreserveKnownErrors(t *testing.T) {
	err := safeError(&mysqldriver.MySQLError{Number: 1064, Message: "bad secret-value SQL"}, "secret-value")
	if strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "1064") {
		t.Fatal(err)
	}
	if !errors.Is(safeError(contract.ErrRevoked, ""), contract.ErrRevoked) {
		t.Fatal("lost error identity")
	}
	if strings.Contains(safeError(errors.New("transport secret-value"), "secret-value").Error(), "secret-value") {
		t.Fatal("leaked transport error")
	}
}
