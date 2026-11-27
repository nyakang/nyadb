package prestosql

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
	"net/url"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestNativeConnectionKeepsSecretsEphemeralAndSnapshotsDriverProperties(t *testing.T) {
	c, err := connections.NormalizePrestoSQLManagedConfig(connections.PrestoSQLManagedConfig{
		Endpoint: "https://[::1]:8443", Username: "original", Catalog: "目录", Schema: "schema with space",
		DriverVersion: "original", DriverSHA256: strings.Repeat("a", 64), JDBCParameters: "source=nyadb&SSLTrustStorePath=%2Ftmp%2Ftrust.p12",
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials := Credentials{Password: "private-password", Parameters: map[string]string{"SSLTrustStorePassword": "private=store +"}}
	native, err := nativeConnection(c, credentials)
	if err != nil {
		t.Fatal(err)
	}
	credentials.Parameters["SSLTrustStorePassword"] = "changed"
	u, err := url.Parse(strings.TrimPrefix(native["connection_string"].(string), "jdbc:"))
	if err != nil || u.Host != "[::1]:8443" || u.Path != "/目录/schema with space" || u.Query().Get("SSL") != "true" || u.Query().Get("SSLTrustStorePassword") != "private=store +" || native["password"] != "private-password" {
		t.Fatal("immutable original native settings lost")
	}
	public, _ := json.Marshal(c)
	secretJSON, _ := json.Marshal(credentials)
	if strings.Contains(string(public), "private") || string(secretJSON) != "{}" {
		t.Fatal("credentials escaped into ordinary configuration serialization")
	}
	credentials.Parameters = map[string]string{"SSLTrustStorePassword": "ambiguous&source=other"}
	if _, err := nativeConnection(c, credentials); err == nil {
		t.Fatal("unrepresentable original JDBC property accepted")
	}
	credentials.Parameters = map[string]string{"jdbc_driver_paths": "/foreign.jar"}
	if _, err := nativeConnection(c, credentials); err == nil {
		t.Fatal("foreign graph admitted through secret properties")
	}
}

func TestPrestoSQLMetadataKeepsDecimalScaleBinaryNullAndDefaults(t *testing.T) {
	raw := `{"name":"amount","data_type":"decimal(38,20)","is_nullable":true,"column_default":null,"is_primary_key":false,"extra":null,"comment":null,"numeric_precision":38,"numeric_scale":20,"character_maximum_length":null}`
	column, err := decodeColumn(json.RawMessage(raw), 2)
	if err != nil || column.Default != nil || column.NumericPrecision == nil || *column.NumericPrecision != 38 || *column.NumericScale != 20 || column.ValueKind != contract.Decimal || column.Position != 2 {
		t.Fatal("original metadata lost", err)
	}
	for _, change := range [][2]string{{`"is_nullable":true`, `"is_nullable":null`}, {`"numeric_scale":20`, `"numeric_scale":"20"`}, {`"extra":null`, `"extra":null,"foreign":true`}, {`"name":"amount"`, `"name":"amount","name":"other"`}} {
		if _, err := decodeColumn(json.RawMessage(strings.Replace(raw, change[0], change[1], 1)), 1); err == nil {
			t.Fatal("ambiguous column metadata accepted")
		}
	}
	if fieldKind("varbinary") != contract.Binary {
		t.Fatal("native varbinary metadata lost")
	}
	cell, err := queryCell(json.RawMessage(`"0x00ff80"`))
	if err != nil || cell.Kind != contract.Text {
		t.Fatal("unknown SQL result type guessed from text")
	}
}

// A caller cancellation is not evidence that a successful write ACK vanished.
// Native suites separately cover actual cancellation and lost write replies.
func TestSuccessfulExecutorAcknowledgementSurvivesCallerCancellation(t *testing.T) {
	raw := json.RawMessage(`{"columns":[],"rows":[],"large_value_cells":[],"affected_rows":1,"execution_time_ms":4,"truncated":false}`)
	receipt := jdbcplugin.Receipt{Dispatched: true, ExecutorAcknowledged: true, CancellationRequested: true, Result: raw}
	result, err := acknowledgedResult(receipt, context.Canceled)
	if !errors.Is(err, context.Canceled) || !result.completed || result.affectedRows != 1 {
		t.Fatal("successful original write acknowledgement lost", err, result)
	}
	receipt.RemoteFailure = true
	result, err = acknowledgedResult(receipt, context.Canceled)
	if !errors.Is(err, context.Canceled) || result.completed {
		t.Fatal("failed ACK fabricated write completion", err)
	}
	receipt.RemoteFailure = false
	receipt.Result = json.RawMessage(`{}`)
	if _, err = acknowledgedResult(receipt, context.Canceled); !errors.Is(err, contract.ErrSQLSessionLost) || !errors.Is(err, ErrResponse) {
		t.Fatal("unrecognized ACK allowed continued execution", err)
	}
}
