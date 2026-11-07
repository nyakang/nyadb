package connections_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestElasticsearchPublicSettingsKeepCredentialsSeparate(t *testing.T) {
	for _, c := range []connections.ElasticsearchConfig{
		{Endpoint: "https://localhost:9200/", Auth: "basic", Username: "reader"},
		{Endpoint: "https://localhost:9200", Auth: "api_key"},
		{Endpoint: "http://127.0.0.1:9200"},
	} {
		got, err := connections.NormalizeElasticsearchConfig(c)
		if err != nil || got.Endpoint[len(got.Endpoint)-1] == '/' || got.Auth == "" {
			t.Fatal("valid native configuration rejected", err)
		}
	}
	for _, c := range []connections.ElasticsearchConfig{
		{Endpoint: "http://localhost:9200", Auth: "basic", Username: "reader"},
		{Endpoint: "http://localhost:9200", Auth: "api_key"},
		{Endpoint: "https://reader:secret@localhost:9200"},
		{Endpoint: "https://localhost:9200/proxy"},
		{Endpoint: "https://localhost:9200?password=secret"},
		{Endpoint: "https://localhost:9200?"},
		{Endpoint: "https://localhost:9200#x"},
		{Endpoint: "https://localhost:9200", Auth: "api_key", Username: "reader"},
		{Endpoint: "https://localhost:9200", Auth: "basic", Username: "a:b"},
		{Endpoint: "https://localhost:9200", Auth: "basic"},
		{Endpoint: "http://localhost:9200", ServerName: "localhost"},
		{Endpoint: "https://localhost:9200", CAFile: "relative.pem"},
		{Endpoint: "https://localhost:9200", Auth: "bearer"},
	} {
		if _, err := connections.NormalizeElasticsearchConfig(c); err == nil {
			t.Fatal("invalid/mixed settings admitted")
		}
	}
}
