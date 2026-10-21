package connections

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MySQLSSL is non-secret SSL configuration metadata. Certificate/key paths
// are stored; key passphrases and file contents must never be part of Profile.
// The Go MySQL driver now enforces certificate and hostname checks when TLS is enabled.
type MySQLSSL struct {
	Enabled           bool   `json:"enabled,omitempty"`
	UseVerification   bool   `json:"useVerification,omitempty"`
	ClientKey         string `json:"clientKey,omitempty"`
	ClientCertificate string `json:"clientCertificate,omitempty"`
	CACertificate     string `json:"caCertificate,omitempty"`
	VerifyServerCA    bool   `json:"verifyServerCA,omitempty"`
}

func validateMySQLSSL(ssl MySQLSSL) error {
	for _, entry := range []struct {
		name string
		path string
	}{
		{"client key", ssl.ClientKey},
		{"client certificate", ssl.ClientCertificate},
		{"CA certificate", ssl.CACertificate},
	} {
		if strings.ContainsRune(entry.path, 0) ||
			utf8.RuneCountInString(entry.path) > 4096 ||
			strings.ContainsAny(entry.path, "\r\n") {
			return fmt.Errorf("%w: invalid MySQL SSL %s path", ErrInvalid, entry.name)
		}
	}
	return nil
}
