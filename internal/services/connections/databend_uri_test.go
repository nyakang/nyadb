package connections_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDatabendURIPreservesNativeAndJDBCTransportDefaults(t *testing.T) {
	for _, test := range []struct {
		uri      string
		port     uint16
		tls      string
		database string
	}{
		{"databend://host", 443, "verify-full", "default"},
		{"jdbc:databend://host", 8000, "disable", "default"},
		{"databend://host/db?ssl=false", 80, "disable", "db"},
		{"databend://host/db?sslmode=disable", 80, "disable", "db"},
		{"jdbc:databend://host/db?ssl=false", 8000, "disable", "db"},
		{"jdbc:databend://host/db?sslmode=disable", 8000, "disable", "db"},
		{"jdbc:databend://host/db?ssl=true", 443, "verify-full", "db"},
		{"databend://host/db?sslmode=enable", 443, "verify-full", "db"},
		{"jdbc:databend://host/db?sslmode=require", 443, "verify-full", "db"},
		{"DATABEND://host/db?SSL=%20TRUE%20&sslMODE=require", 443, "verify-full", "db"},
		{"JDBC:DATABEND://host:8001/db?ssl=true", 8001, "verify-full", "db"},
		{"databend://[::1]:8002/%E7%8C%AB?ssl=false&sslmode=disable", 8002, "disable", "猫"},
	} {
		t.Run(test.uri, func(t *testing.T) {
			got, err := connections.ParseURI(test.uri)
			if err != nil || got.Profile.Kind != connections.Databend || got.Profile.Port != test.port ||
				got.Profile.TLSMode != test.tls || got.Profile.Database != test.database || got.Profile.DatabendOptions.DriverVersion != "" {
				t.Fatal("Databend URI changed target, TLS or selected an implicit driver", got.Profile, err)
			}
			transport, err := connections.DatabendProfileTransport(got.Profile)
			if err != nil || (strings.HasPrefix(transport.Endpoint, "https://")) != (test.tls == "verify-full") {
				t.Fatal("URI and executed transport disagree", transport, err)
			}
		})
	}
}

func TestDatabendURIKeepsCredentialsTemporaryAndDriverPinExplicit(t *testing.T) {
	secret := "temporary password 猫"
	for _, uri := range []string{
		"databend://alice:temporary%20password%20%E7%8C%AB@host/db",
		"jdbc:databend://host/db?user=alice&password=temporary%20password%20%E7%8C%AB",
	} {
		got, err := connections.ParseURI(uri)
		if err != nil || got.Profile.Username != "alice" || got.Password != secret {
			t.Fatal("Databend URI did not retain temporary credentials", err)
		}
		raw, _ := json.Marshal(got)
		for _, text := range []string{string(raw), fmt.Sprintf("%v", got), fmt.Sprintf("%#v", got)} {
			if strings.Contains(text, secret) {
				t.Fatal("temporary Databend secret leaked into persistence or formatting")
			}
		}
	}
	digest := strings.Repeat("a", 64)
	got, err := connections.ParseURI("databend://host/db?driver_version=jdbc-0.4.8-agent-0.1.82&driver_sha256=" + digest)
	if err != nil || got.Profile.DatabendOptions.DriverVersion != "jdbc-0.4.8-agent-0.1.82" || got.Profile.DatabendOptions.DriverSHA256 != digest {
		t.Fatal("URI lost explicit immutable driver pin", err)
	}
	got, err = connections.ParseURI("databend://host/db?auth=bearer&password=temporary-token")
	if err != nil || got.Profile.DatabendOptions.Auth != "bearer" || got.Profile.Username != "" || got.Password != "temporary-token" {
		t.Fatal("bearer URI substituted basic credentials", err)
	}
	got, err = connections.ParseURI("databend://host/db?auth=none")
	if err != nil || got.Profile.DatabendOptions.Auth != "none" || got.Profile.Username != "" || got.Password != "" {
		t.Fatal("anonymous URI inherited default credentials", err)
	}
}

func TestDatabendURIRejectsConflictsAmbiguousTargetsAndUnsupportedProperties(t *testing.T) {
	for _, uri := range []string{
		"databend://host?ssl=true&SSL=true", "databend://host?ssl=true&ssl=true",
		"databend://host?sslmode=enable&SSLMODE=require", "databend://host?ssl=1",
		"databend://host?ssl=", "databend://host?sslmode=verify-full",
		"databend://host?ssl=true&sslmode=disable", "jdbc:databend://host?ssl=false&sslmode=enable",
		"databend://host?warehouse=original", "databend://host?tenant=original", "databend://host?route=original",
		"databend://host?ssl=false&server_name=original", "databend://host?client_cert_file=/cert.pem",
		"databend://host?driver_version=jdbc-0.4.8", "databend://host?auth=none&password=secret",
		"databend://alice:secret@host?user=alice", "databend://alice:secret@host?password=secret",
		"databend://host?user=alice&user=alice", "databend://host?password=one&password=two",
		"databend://host?auth=bearer&user=alice", "databend://host:0", "databend://host:65536",
		"databend://host:", "databend://::1", "databend://host/db/schema", "databend://host#",
		"jdbc:databend://host#", "databend://host?password=%0A", "databend://host?password=%FF",
	} {
		t.Run(uri, func(t *testing.T) {
			got, err := connections.ParseURI(uri)
			if err == nil || got.Profile.Kind != "" || got.Password != "" {
				t.Fatal("ambiguous or unsupported Databend URI was accepted")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), uri) {
				t.Fatal("URI error exposed original credentials")
			}
		})
	}
}
