package gokb

import (
	"context"
	"crypto/tls"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Connector represents a fixed configuration for the gokb driver with a given
// name. Connector satisfies the database/sql/driver Connector interface and
// can be used to create any number of DB Conn's via the database/sql OpenDB
// function.
//
// See https://golang.org/pkg/database/sql/driver/#Connector.
// See https://golang.org/pkg/database/sql/#OpenDB.
type Connector struct {
	opts           values
	dialer         Dialer
	explicitTLS    bool
	tlsConfig      *tls.Config
	connectTimeout time.Duration
}

// Connect returns a connection to the database using the fixed configuration
// of this Connector. Context bounds dialing, TLS, authentication and startup.
func (c *Connector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.connectTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.connectTimeout)
		defer cancel()
	}
	return c.open(ctx)
}

// Config captures explicit native settings without environment, DSN, password
// file, OS user or home-directory certificate fallback. A nil TLSConfig means
// explicitly selected plaintext. RuntimeParams may only contain application_name,
// statement_timeout and search_path. The connector copies these inputs.
type Config struct {
	Host                     string
	Port                     uint16
	User, Database, Password string
	ConnectTimeout           time.Duration
	RuntimeParams            map[string]string
	TLSConfig                *tls.Config
	DialFunc                 func(context.Context, string, string) (net.Conn, error)
}

func NewConnectorConfig(config Config) (*Connector, error) {
	if config.Host == "" || config.Port == 0 || config.User == "" || config.Database == "" || config.ConnectTimeout <= 0 {
		return nil, errors.New("invalid explicit Kingbase connection settings")
	}
	o := values{"host": config.Host, "port": strconv.Itoa(int(config.Port)), "user": config.User, "dbname": config.Database, "password": config.Password, "client_encoding": "UTF8", "datestyle": "ISO, MDY", "extra_float_digits": "2"}
	for key, value := range config.RuntimeParams {
		switch key {
		case "application_name", "statement_timeout", "search_path":
			o[key] = value
		default:
			return nil, errors.New("unsupported explicit Kingbase runtime parameter")
		}
	}
	dialer := Dialer(defaultDialer{})
	if config.DialFunc != nil {
		dialer = explicitDialer{config.DialFunc}
	}
	var tlsConfig *tls.Config
	if config.TLSConfig != nil {
		tlsConfig = config.TLSConfig.Clone()
	}
	return &Connector{opts: o, dialer: dialer, explicitTLS: true, tlsConfig: tlsConfig, connectTimeout: config.ConnectTimeout}, nil
}

type explicitDialer struct {
	dial func(context.Context, string, string) (net.Conn, error)
}

func (d explicitDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.dial(ctx, network, address)
}

func (d explicitDialer) Dial(network, address string) (net.Conn, error) {
	return d.dial(context.Background(), network, address)
}

func (d explicitDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.dial(ctx, network, address)
}

// Driver returns the underlying driver of this Connector.
func (c *Connector) Driver() driver.Driver {
	return &Driver{}
}

// NewConnector returns a connector for the gokb driver in a fixed configuration
// with the given dsn. The returned connector can be used to create any number
// of equivalent Conn's. The returned connector is intended to be used with
// database/sql.OpenDB.
//
// See https://golang.org/pkg/database/sql/driver/#Connector.
// See https://golang.org/pkg/database/sql/#OpenDB.
func NewConnector(dsn string) (*Connector, error) {
	var err error
	o := make(values)

	// A number of defaults are applied here, in this order:
	//
	// * Very low precedence defaults applied in every situation
	// * Environment variables
	// * Explicitly passed connection information
	o["host"] = "localhost"
	o["port"] = "54321"
	// N.B.: Extra float digits should be set to 3, but that breaks
	// Postgres 8.4 and older, where the max is 2.
	o["extra_float_digits"] = "2"
	for k, v := range parseEnviron(os.Environ()) {
		o[k] = v
	}

	if strings.HasPrefix(dsn, "kingbase://") {
		dsn, err = ParseURL(dsn)
		if err != nil {
			return nil, err
		}
	}

	if err := parseOpts(dsn, o); err != nil {
		return nil, err
	}

	// Use the "fallback" application name if necessary
	if fallback, ok := o["fallback_application_name"]; ok {
		if _, ok := o["application_name"]; !ok {
			o["application_name"] = fallback
		}
	}

	// We can't work with any client_encoding other than UTF-8 currently.
	// However, we have historically allowed the user to set it to UTF-8
	// explicitly, and there's no reason to break such programs, so allow that.
	// Note that the "options" setting could also set client_encoding, but
	// parsing its value is not worth it.  Instead, we always explicitly send
	// client_encoding as a separate run-time parameter, which should override
	// anything set in options.
	if enc, ok := o["client_encoding"]; ok && !isUTF8(enc) {
		return nil, errors.New("client_encoding must be absent or 'UTF8'")
	}
	o["client_encoding"] = "UTF8"
	// DateStyle needs a similar treatment.
	if datestyle, ok := o["datestyle"]; ok {
		if datestyle != "ISO, MDY" {
			return nil, fmt.Errorf("setting datestyle must be absent or %v; got %v", "ISO, MDY", datestyle)
		}
	} else {
		o["datestyle"] = "ISO, MDY"
	}

	// If a user is not provided by any other means, the last
	// resort is to use the current operating system provided user
	// name.
	if _, ok := o["user"]; !ok {
		u, err := userCurrent()
		if err != nil {
			return nil, err
		}
		o["user"] = u
	}

	// SSL is not necessary or supported over UNIX domain sockets
	if network, _ := network(o); network == "unix" {
		o["sslmode"] = "disable"
	}

	return &Connector{opts: o, dialer: defaultDialer{}}, nil
}
