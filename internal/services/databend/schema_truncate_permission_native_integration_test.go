package databend

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func TestDatabendSchemaTruncateNativePrivilegeRefusal(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	name := "truncate_denied62"
	qualified := quoteName(f.Database) + "." + quoteName(name)
	localNativeQuery(t, f.localTransportFixture, "CREATE TABLE "+qualified+" (id UInt64)")
	localNativeQuery(t, f.localTransportFixture, "INSERT INTO "+qualified+" VALUES(18446744073709551615)")
	localNativeQuery(t, f.localTransportFixture, "CREATE USER 'nyadb_truncate_limited62' IDENTIFIED WITH NO_PASSWORD")
	localNativeQuery(t, f.localTransportFixture, "GRANT SELECT ON *.* TO 'nyadb_truncate_limited62'")
	p := nativeProviderProfile(t, f, 864, filepath.Join(f.Directory, t.Name(), "store"))
	p.Username = "nyadb_truncate_limited62"
	provider := Provider{Started: func(pid int) error { return recordManagedNativeProcess(t, f, pid) }, Observe: func(method string, r agentbridge.Receipt) {
		recordTransportReceipt(t, f.localTransportFixture, "limited-"+method, r)
	}}
	raw, err := provider.Open(context.Background(), p, "")
	if err != nil {
		t.Fatal("native limited account connection", err)
	}
	s := raw.(*Session)
	defer s.Close()
	lease, before := nativeSchemaDocument(t, s, "limited-truncate62", name)
	out := lease.Operations().Truncate(context.Background(), before)
	if out.Verified || out.State == "committed" || out.Error == nil || out.Receipt == nil {
		t.Fatal("unprivileged truncate admitted", out)
	}
	native := strings.ToLower(out.Receipt.NativeJSON)
	if !strings.Contains(native, "privilege") && !strings.Contains(native, "permission") {
		t.Fatal("failure was not native authorization refusal")
	}
	rows := localNativeQuery(t, f.localTransportFixture, "SELECT id FROM "+qualified)
	if !reflect.DeepEqual(rows, [][]string{{"18446744073709551615"}}) {
		t.Fatal("denied truncate changed original rows", rows)
	}
	original := lease.OriginalOutcome()
	_, _ = lease.Operations().Snapshot(context.Background(), before.Target)
	if !reflect.DeepEqual(original, lease.OriginalOutcome()) {
		t.Fatal("read replaced privilege refusal")
	}
	writeDataEvidence(t, f, "schema-truncate_denied62.json", map[string]any{"original": original, "independent_rows": rows, "native_unprivileged_user_denied": true, "read_did_not_settle": true})
}
