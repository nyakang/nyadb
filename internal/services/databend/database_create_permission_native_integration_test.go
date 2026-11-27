package databend

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func TestDatabendDatabaseCreateNativePrivilegeRefusal(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	// No credential material is generated or recorded. This disposable account
	// has native NO_PASSWORD authentication and SELECT, without CREATE DATABASE.
	localNativeQuery(t, f.localTransportFixture, "CREATE USER 'nyadb_create_limited61' IDENTIFIED WITH NO_PASSWORD")
	localNativeQuery(t, f.localTransportFixture, "GRANT SELECT ON *.* TO 'nyadb_create_limited61'")
	p := nativeProviderProfile(t, f, 904, filepath.Join(f.Directory, t.Name(), "store"))
	p.Username = "nyadb_create_limited61"
	p.Database = "default"
	provider := Provider{Started: func(pid int) error { return recordManagedNativeProcess(t, f, pid) }, Observe: func(method string, r agentbridge.Receipt) {
		recordTransportReceipt(t, f.localTransportFixture, "limited-"+method, r)
	}}
	raw, err := provider.Open(context.Background(), p, "")
	if err != nil {
		t.Fatal("native limited account connection", err)
	}
	s := raw.(*Session)
	defer s.Close()
	d := nativeDatabaseCreateDocument(t, s, "limited-create", "create_denied61")
	before, err := d.Snapshot(context.Background())
	if err != nil || before.Exists {
		t.Fatal("limited absence", before, err)
	}
	out := d.Create(context.Background(), before)
	if out.Verified || out.State == "committed" || out.Error == nil || out.Receipt == nil {
		t.Fatal("unprivileged create admitted", out)
	}
	native := strings.ToLower(out.Receipt.NativeJSON)
	if !strings.Contains(native, "privilege") && !strings.Contains(native, "permission") {
		t.Fatal("failure was not native authorization refusal")
	}
	rows := localNativeQuery(t, f.localTransportFixture, "SELECT name FROM system.databases WHERE name='create_denied61' AND dropped_on IS NULL")
	if len(rows) != 0 {
		t.Fatal("denied namespace exists", rows)
	}
	original := d.OriginalOutcome()
	_, _ = d.Snapshot(context.Background())
	if !reflect.DeepEqual(original, d.OriginalOutcome()) {
		t.Fatal("read replaced privilege refusal")
	}
	writeDataEvidence(t, f, "database-create_denied61.json", map[string]any{"original": original, "independent_rows": rows, "native_unprivileged_user_denied": true, "read_did_not_settle": true})
}
