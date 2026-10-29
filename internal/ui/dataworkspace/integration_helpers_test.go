package dataworkspace

import (
	"os"
	"strings"
	"testing"
)

func mysqlIntegrationPassword(t *testing.T) string {
	t.Helper()
	value := os.Getenv("NYADB_MYSQL_IT_PASSWORD")
	if value != "" {
		return value
	}
	file := os.Getenv("NYADB_MYSQL_IT_PASSWORD_FILE")
	if file == "" {
		t.Fatal("explicit test credential is required via NYADB_MYSQL_IT_PASSWORD or NYADB_MYSQL_IT_PASSWORD_FILE")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read isolated test credential: %v", err)
	}
	value = strings.TrimSpace(string(data))
	if value == "" {
		t.Fatal("isolated test credential is empty")
	}
	return value
}
