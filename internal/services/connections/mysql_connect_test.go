package connections

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDirectMySQLConfigDefaultsAndTimeouts(t *testing.T) {
	advanced := DefaultMySQLAdvanced()
	s := ActiveSettings{ConnectionName: "unit", Host: "127.0.0.1", Port: 3306,
		Username: "reader", Database: "sample", MySQLAdvanced: advanced}
	cfg, err := DirectMySQLConfig(s, "secret-should-not-be-logged")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:3306" || cfg.Net != "tcp" || cfg.User != "reader" ||
		cfg.Passwd != "secret-should-not-be-logged" || cfg.DBName != "sample" ||
		cfg.Timeout != 30*time.Second || cfg.ReadTimeout != 0 || cfg.WriteTimeout != 30*time.Second ||
		cfg.AllowCleartextPasswords || cfg.AllowOldPasswords || cfg.AllowAllFiles ||
		cfg.MultiStatements || cfg.AllowFallbackToPlaintext {
		t.Fatal("TCP configuration defaults or security options incorrect")
	}
	advanced.ConnectTimeout = false
	advanced.WriteTimeout = false
	advanced.ReadTimeout = true
	advanced.ReadTimeoutSeconds = 13
	s.MySQLAdvanced = advanced
	cfg, err = DirectMySQLConfig(s, "")
	if err != nil || cfg.Timeout != 10*time.Second || cfg.ReadTimeout != 13*time.Second ||
		cfg.WriteTimeout != 0 {
		t.Fatalf("timeout selection failed: %v", err)
	}
	s.Host = "::1"
	cfg, err = DirectMySQLConfig(s, "")
	if err != nil || cfg.Addr != "[::1]:3306" {
		t.Fatalf("IPv6 address: %v", err)
	}
}

func TestDirectMySQLConfigAllowsRemoteWithoutTLS(t *testing.T) {
	for _, host := range []string{"mysql.example.test", "198.51.100.8", "2001:db8::1"} {
		t.Run(host, func(t *testing.T) {
			s := ActiveSettings{Host: host, Port: 3306, MySQLAdvanced: DefaultMySQLAdvanced()}
			cfg, err := DirectMySQLConfig(s, "temporary")
			if err != nil {
				t.Fatalf("remote plain TCP must be supported: %v", err)
			}
			if cfg.TLS != nil || cfg.Addr != net.JoinHostPort(host, "3306") {
				t.Fatalf("unexpected remote TCP config: TLS=%v Addr=%q", cfg.TLS, cfg.Addr)
			}
		})
	}
}

func TestDirectMySQLConfigFailsClosedOnUnsupportedFeatures(t *testing.T) {
	base := ActiveSettings{Host: "localhost", Port: 3306, MySQLAdvanced: DefaultMySQLAdvanced()}
	cases := []struct {
		name   string
		mutate func(*ActiveSettings)
	}{
		{"ssh", func(s *ActiveSettings) { s.MySQLAdvanced.SSH.Enabled = true }},
		{"http", func(s *ActiveSettings) { s.MySQLAdvanced.HTTP.Enabled = true }},
		{"pipe", func(s *ActiveSettings) { s.MySQLAdvanced.NamedPipe = true }},
		{"compression", func(s *ActiveSettings) { s.MySQLAdvanced.Compression = true }},
		{"charset", func(s *ActiveSettings) { s.MySQLAdvanced.Charset = "utf8mb4" }},
		{"compatibility", func(s *ActiveSettings) { s.MySQLAdvanced.Compatibility.ForceSQLMode = true }},
		{"auto", func(s *ActiveSettings) { s.MySQLAdvanced.AutoConnect = true }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := base
			tt.mutate(&s)
			if _, err := DirectMySQLConfig(s, ""); !errors.Is(err, ErrMySQLUnsupported) {
				t.Fatalf("unsupported setting was silently ignored: %v", err)
			}
		})
	}
	for _, host := range []string{"", "root:pw@localhost", "mysql://localhost", "hello\nworld"} {
		s := base
		s.Host = host
		if _, err := DirectMySQLConfig(s, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("host %q accepted: %v", host, err)
		}
	}
}

func TestDirectMySQLConnectRefusesWithoutDialOnInvalidConfig(t *testing.T) {
	s := ActiveSettings{Host: "invalid.example.test", Port: 3306, MySQLAdvanced: DefaultMySQLAdvanced()}
	s.MySQLAdvanced.SSH.Enabled = true
	start := time.Now()
	err := TestDirectMySQLConnection(context.Background(), s, "do-not-save")
	if !errors.Is(err, ErrMySQLUnsupported) || time.Since(start) > time.Second {
		t.Fatalf("unsupported tunnel attempted network: %v", err)
	}
	s.MySQLAdvanced.SSH.Enabled = false
	s.Host = "127.0.0.1"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = TestDirectMySQLConnection(ctx, s, "")
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("cancelled context not propagated: %v", err)
	}
}
