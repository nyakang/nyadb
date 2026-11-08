package sqlworkspace

import (
	"encoding/json"
	"github.com/nyakang/nyadb/internal/services/connections"
	"net/url"
	"os"
	"strconv"
	"testing"
)

func meilisearchPublicFixture(t *testing.T, configName string) connections.URIImport {
	t.Helper()
	if os.Getenv("NYADB_MEILISEARCH_PUBLIC_IT") != "1" {
		t.Skip("requires owned native Meilisearch public workbench fixture")
	}
	raw, err := os.ReadFile(os.Getenv(configName))
	if err != nil {
		t.Fatal("private native configuration unavailable")
	}
	var cfg struct {
		Endpoint, Auth string
		IndexUID       string `json:"index_uid"`
		CAFile         string `json:"ca_file"`
		ServerName     string `json:"server_name"`
		ClientCertFile string `json:"client_cert_file"`
		ClientKeyFile  string `json:"client_key_file"`
		KeyFile        string `json:"key_file"`
		ConnectTimeout int    `json:"connect_timeout_seconds"`
		RequestTimeout int    `json:"request_timeout_seconds"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("invalid private native configuration")
	}
	secret := ""
	if cfg.KeyFile != "" {
		raw, err = os.ReadFile(cfg.KeyFile)
		if err != nil {
			t.Fatal("private key unavailable")
		}
		secret = string(raw)
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal("invalid private endpoint")
	}
	uri := url.URL{Scheme: "meilisearch", Host: endpoint.Host, Path: endpoint.Path}
	options := url.Values{"auth": {cfg.Auth}, "sslmode": {"disable"}}
	if endpoint.Scheme == "https" {
		options.Set("sslmode", "verify-full")
	}
	for key, value := range map[string]string{"index_uid": cfg.IndexUID, "ca_file": cfg.CAFile, "server_name": cfg.ServerName, "client_cert_file": cfg.ClientCertFile, "client_key_file": cfg.ClientKeyFile} {
		if value != "" {
			options.Set(key, value)
		}
	}
	if cfg.ConnectTimeout != 0 {
		options.Set("connect_timeout", strconv.Itoa(cfg.ConnectTimeout))
	}
	if cfg.RequestTimeout != 0 {
		options.Set("request_timeout", strconv.Itoa(cfg.RequestTimeout))
	}
	if cfg.Auth == "bearer" {
		uri.User = url.UserPassword("", secret)
	}
	uri.RawQuery = options.Encode()
	parsed, err := connections.ParseURI(uri.String())
	if err != nil || parsed.Profile.Kind != connections.Meilisearch || parsed.Password != secret {
		t.Fatal("public URI changed original profile/key", err)
	}
	parsed.Profile.ID, parsed.Profile.Name = 96, "Native Meilisearch workbench"
	return parsed
}
