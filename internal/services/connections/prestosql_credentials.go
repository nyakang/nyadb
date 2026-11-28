package connections

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

// PrestoSQLCredentials is ephemeral. Only its explicit vault/worker encoding
// contains secrets; ordinary JSON and diagnostic formatting redact them.
type PrestoSQLCredentials struct {
	Password           string `json:"-"`
	TrustStorePassword string `json:"-"`
	KeyStorePassword   string `json:"-"`
	AccessToken        string `json:"-"`
	ExtraCredentials   string `json:"-"`
}

func (PrestoSQLCredentials) String() string   { return "PrestoSQL credentials (redacted)" }
func (PrestoSQLCredentials) GoString() string { return "PrestoSQL credentials (redacted)" }

func (c PrestoSQLCredentials) Parameters() map[string]string {
	out := map[string]string{}
	for key, value := range map[string]string{"SSLTrustStorePassword": c.TrustStorePassword, "SSLKeyStorePassword": c.KeyStorePassword, "accessToken": c.AccessToken, "extraCredentials": c.ExtraCredentials} {
		if value != "" {
			out[key] = value
		}
	}
	return out
}
func ValidatePrestoSQLCredentials(c PrestoSQLCredentials) error {
	if !utf8.ValidString(c.Password) || len(c.Password) > 65536 || strings.ContainsRune(c.Password, 0) {
		return ErrInvalid
	}
	for _, value := range c.Parameters() {
		if !utf8.ValidString(value) || len(value) > 65536 || strings.ContainsAny(value, "\x00&") {
			return ErrInvalid
		}
	}
	return nil
}

const prestoCredentialPrefix = "nyadb-prestosql-credentials-v1:"

func EncodePrestoSQLCredentials(c PrestoSQLCredentials) (string, error) {
	if err := ValidatePrestoSQLCredentials(c); err != nil {
		return "", err
	}
	if c == (PrestoSQLCredentials{}) {
		return "", nil
	}
	raw, err := json.Marshal(struct {
		Version            int    `json:"version"`
		Password           string `json:"password"`
		TrustStorePassword string `json:"trust_store_password"`
		KeyStorePassword   string `json:"key_store_password"`
		AccessToken        string `json:"access_token"`
		ExtraCredentials   string `json:"extra_credentials"`
	}{1, c.Password, c.TrustStorePassword, c.KeyStorePassword, c.AccessToken, c.ExtraCredentials})
	if err != nil {
		return "", ErrInvalid
	}
	encoded := prestoCredentialPrefix + base64.RawStdEncoding.EncodeToString(raw)
	if len(encoded) > 512<<10 {
		return "", ErrInvalid
	}
	return encoded, nil
}

func DecodePrestoSQLCredentials(encoded string) (PrestoSQLCredentials, error) {
	var c PrestoSQLCredentials
	if encoded == "" {
		return c, nil
	}
	if !strings.HasPrefix(encoded, prestoCredentialPrefix) || len(encoded) > 512<<10 {
		return c, ErrInvalid
	}
	raw, err := base64.RawStdEncoding.Strict().DecodeString(strings.TrimPrefix(encoded, prestoCredentialPrefix))
	if err != nil {
		return c, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return c, ErrInvalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil {
			return PrestoSQLCredentials{}, ErrInvalid
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return PrestoSQLCredentials{}, ErrInvalid
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || d.Decode(new(any)) != io.EOF || len(fields) != 6 || string(fields["version"]) != "1" {
		return c, ErrInvalid
	}
	for key, target := range map[string]*string{"password": &c.Password, "trust_store_password": &c.TrustStorePassword, "key_store_password": &c.KeyStorePassword, "access_token": &c.AccessToken, "extra_credentials": &c.ExtraCredentials} {
		raw := fields[key]
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, target) != nil {
			return PrestoSQLCredentials{}, ErrInvalid
		}
	}
	if ValidatePrestoSQLCredentials(c) != nil {
		return PrestoSQLCredentials{}, ErrInvalid
	}
	return c, nil
}
