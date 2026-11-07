package connections_test

import (
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestEasysearchNativeCredentialsAndTransportStayDistinct(t *testing.T) {
	valid := connections.EasysearchConfig{Endpoint: "https://localhost:9200/", Auth: "access_token"}
	c, err := connections.NormalizeEasysearchConfig(valid)
	if err != nil || c.Endpoint != "https://localhost:9200" {
		t.Fatal("native endpoint", err)
	}
	if err := connections.ValidateEasysearchCredential(c, "native-opaque-token", true); err != nil {
		t.Fatal("opaque native access token incorrectly requires Elasticsearch Base64", err)
	}
	for _, change := range []func(*connections.EasysearchConfig){
		func(c *connections.EasysearchConfig) { c.Auth = "api_key" },
		func(c *connections.EasysearchConfig) { c.Endpoint = "http://localhost:9200" },
		func(c *connections.EasysearchConfig) { c.Username = "admin" },
		func(c *connections.EasysearchConfig) { c.Endpoint += "?api_token=secret" },
		func(c *connections.EasysearchConfig) { c.Endpoint = "https://admin:secret@localhost:9200" },
		func(c *connections.EasysearchConfig) { c.CAFile = "relative-ca.pem" },
	} {
		candidate := valid
		change(&candidate)
		if _, err := connections.NormalizeEasysearchConfig(candidate); err == nil {
			t.Fatal("invalid or incompatible native settings accepted")
		}
	}
	for _, secret := range []string{"", "token\r\nX-API-TOKEN: other", "token with spaces", "令牌", string([]byte{0xff})} {
		if err := connections.ValidateEasysearchCredential(c, secret, true); err == nil {
			t.Fatal("invalid access token accepted")
		}
	}
	if err := connections.ValidateEasysearchCredential(c, "", false); err != nil {
		t.Fatal("optional temporary credential cannot be empty", err)
	}
	none := connections.EasysearchConfig{Endpoint: "http://localhost:9200", Auth: "none"}
	if _, err := connections.NormalizeEasysearchConfig(none); err != nil {
		t.Fatal("unauthenticated HTTP settings", err)
	}
	if err := connections.ValidateEasysearchCredential(none, "stray secret", false); err == nil {
		t.Fatal("none authentication retained a credential")
	}
}
