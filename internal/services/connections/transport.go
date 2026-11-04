package connections

import "fmt"

// TransportOptions describes a byte transport independently of SQL dialects.
// Secrets are supplied separately by the transport owner.
type TransportOptions struct {
	SSH  SSHOptions
	HTTP HTTPOptions
}

func NormalizeTransportOptions(v TransportOptions) (TransportOptions, error) {
	var err error
	if v.SSH, err = normalizeMySQLSSH(v.SSH); err != nil {
		return v, err
	}
	if v.HTTP, err = normalizeMySQLHTTP(v.HTTP); err != nil {
		return v, err
	}
	if v.SSH.Enabled && v.HTTP.Enabled {
		return v, fmt.Errorf("%w: SSH and HTTP tunnels are mutually exclusive", ErrInvalid)
	}
	return v, nil
}

func (s ActiveSettings) Transport() TransportOptions {
	return TransportOptions{SSH: s.MySQLAdvanced.SSH, HTTP: s.MySQLAdvanced.HTTP}
}
