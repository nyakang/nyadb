package neo4j

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphschema"
)

func nativeSchemaManagementTest(t *testing.T, s *Session, id uint64) contract.GraphSchemaManagementLease {
	t.Helper()
	l, e := s.ReserveGraphSchemaManagement(schemaObjectTest(s), id)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := l.Close(); e != nil {
			t.Error(e)
		}
	})
	return l
}
func nativeSchemaPrepareManagementTest(t *testing.T, l contract.GraphSchemaManagementLease, draft contract.GraphSchemaDraft) contract.GraphSchemaPreparation {
	t.Helper()
	before := nativeSchemaReadTest(t, l)
	p, e := l.Prepare(before, draft)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func schemaNativeNameExistsTest(t *testing.T, c *Client, name, kind string) bool {
	t.Helper()
	q := "SHOW INDEXES YIELD name WHERE name = $name RETURN name"
	if kind == "constraint" {
		q = "SHOW CONSTRAINTS YIELD name WHERE name = $name RETURN name"
	}
	r := nativeRunTest(t, c, Request{Query: q, Mode: "read", Parameters: map[string]any{"name": name}})
	return len(r.Rows) == 1
}
func TestNeo4jNativeSchemaManagementOriginalReceiptsAndExplicitCatalogVerification(t *testing.T) {
	f := sessionFixtureTest(t)
	s := nativeSessionTest(t, f)
	admin := nativeOpenTest(t, f)
	l := nativeSchemaManagementTest(t, s, 1)
	for _, draft := range nativeSchemaDraftsTest(f) {
		t.Run(strings.ToLower(draft.EntityType+"_"+draft.Type+"_"+fmt.Sprint(len(draft.Properties))), func(t *testing.T) {
			p := nativeSchemaPrepareManagementTest(t, l, draft)
			r, e := l.Submit(context.Background(), p, contract.GraphSchemaConfirmation{})
			if e != nil || r.State != "completed" || !r.ReceiptValid || r.HasVerification || r.Receipt.Uncertain {
				t.Fatal("original receipt replaced by automatic verification", r.State, e)
			}
			kind := "index"
			if draft.Action == "create_constraint" {
				kind = "constraint"
			}
			removed := false
			t.Cleanup(func() {
				if !removed && schemaNativeNameExistsTest(t, admin, draft.Name, kind) {
					nativeRunTest(t, admin, Request{Query: "DROP " + strings.ToUpper(kind) + " `" + strings.ReplaceAll(draft.Name, "`", "``") + "`"})
				}
			})
			if _, e := l.Read(context.Background()); !errors.Is(e, contract.ErrGraphSchemaBusy) {
				t.Fatal("unverified original operation permitted new preparation", e)
			}
			if _, e := l.Submit(context.Background(), p, contract.GraphSchemaConfirmation{}); !errors.Is(e, contract.ErrGraphSchemaBusy) {
				t.Fatal("original native DDL replayable", e)
			}
			r, e = l.Verify(context.Background())
			if e != nil || r.State != "verified" || !r.Matched || !r.HasVerification || !r.ReceiptValid {
				ds, parseErr := graphschema.Definitions(r.Verification)
				t.Fatalf("explicit native catalog verification incomplete: state=%s error=%v draft=%+v definitions=%+v parse=%v", r.State, e, draft, ds, parseErr)
			}
			// Raw operational columns remain separate from completed CREATE.
			if !r.Verification.Indexes.Completed || len(r.Verification.Indexes.Columns) < 16 {
				t.Fatal("verification lost full population/state evidence")
			}
			drop := nativeSchemaPrepareManagementTest(t, l, contract.GraphSchemaDraft{Action: "drop_" + kind, Name: draft.Name})
			if _, e := l.Submit(context.Background(), drop, contract.GraphSchemaConfirmation{Name: draft.Name}); !errors.Is(e, contract.ErrGraphSchemaConfirmation) {
				t.Fatal("DROP omitted exact native-race acknowledgement", e)
			}
			r, e = l.Submit(context.Background(), drop, contract.GraphSchemaConfirmation{Name: draft.Name, AcceptNonAtomicDrop: true})
			if e != nil || r.State != "completed" || !r.ReceiptValid || !r.Preview.NonAtomicDrop || r.HasVerification {
				t.Fatal("DROP original completion missing", r.State, e)
			}
			removed = true
			r, e = l.Verify(context.Background())
			if e != nil || r.State != "verified" || !r.Matched {
				t.Fatal("DROP explicit absence verification missing", e)
			}
		})
	}
}

func TestNeo4jNativeSchemaManagementChangedPreflightAndRejectedNativeDDL(t *testing.T) {
	f := sessionFixtureTest(t)
	s := nativeSessionTest(t, f)
	admin := nativeOpenTest(t, f)
	t.Run("changed-original-definition", func(t *testing.T) {
		l := nativeSchemaManagementTest(t, s, 1)
		draft := schemaDraftTest()
		draft.Name = markerTest(f, "preflight_original")
		draft.LabelsOrTypes = []string{"SchemaPreflight_" + f.Nonce}
		p := nativeSchemaPrepareManagementTest(t, l, draft)
		executeSchemaPreviewTest(t, admin, p.Preview())
		drop := nativeSchemaPrepareManagementTest(t, l, contract.GraphSchemaDraft{Action: "drop_index", Name: draft.Name})
		nativeRunTest(t, admin, Request{Query: "DROP INDEX `" + draft.Name + "`"})
		draft.Properties = []string{"replacement"}
		next := nativeSchemaPrepareManagementTest(t, nativeSchemaManagementTest(t, s, 2), draft)
		executeSchemaPreviewTest(t, admin, next.Preview())
		t.Cleanup(func() { nativeRunTest(t, admin, Request{Query: "DROP INDEX `" + draft.Name + "`"}) })
		r, e := l.Submit(context.Background(), drop, contract.GraphSchemaConfirmation{Name: draft.Name, AcceptNonAtomicDrop: true})
		if !errors.Is(e, contract.ErrGraphSchemaChanged) || r.State != "not_submitted" || r.Receipt.Attempted || !r.Preflight.Indexes.Completed {
			t.Fatal("changed original definition submitted", e)
		}
		if !schemaNativeNameExistsTest(t, admin, draft.Name, "index") {
			t.Fatal("preflight rejection dropped replacement")
		}
	})
	t.Run("native-constraint-failure", func(t *testing.T) {
		l := nativeSchemaManagementTest(t, s, 3)
		label := "SchemaRejected_" + f.Nonce
		nativeRunTest(t, admin, Request{Query: "CREATE (:`" + label + "` {v:1}), (:`" + label + "` {v:1})"})
		t.Cleanup(func() { nativeRunTest(t, admin, Request{Query: "MATCH (n:`" + label + "`) DELETE n"}) })
		draft := contract.GraphSchemaDraft{Action: "create_constraint", Name: markerTest(f, "rejected_unique"), Type: "UNIQUE", EntityType: "NODE", LabelsOrTypes: []string{label}, Properties: []string{"v"}}
		p := nativeSchemaPrepareManagementTest(t, l, draft)
		r, e := l.Submit(context.Background(), p, contract.GraphSchemaConfirmation{})
		if e == nil || r.State != "uncertain" || !r.Receipt.Attempted || r.Receipt.Completed || r.Receipt.FailureCode == "" || r.ReceiptValid {
			t.Fatal("native schema error became completed/replayed", e)
		}
		observed, e := l.Verify(context.Background())
		if e != nil || !observed.HasVerification || observed.Matched || observed.State != "uncertain" || observed.Receipt.FailureCode != r.Receipt.FailureCode {
			t.Fatal("denied original native receipt discarded", e)
		}
		if schemaNativeNameExistsTest(t, admin, draft.Name, "constraint") {
			t.Fatal("failed native constraint appeared")
		}
		if _, e := l.Submit(context.Background(), p, contract.GraphSchemaConfirmation{}); !errors.Is(e, contract.ErrRevoked) {
			t.Fatal("unconfirmed original schema operation replayable", e)
		}
	})
}

func TestNeo4jNativeSchemaManagementActualDDLReplyLostCanceledAndOriginalClose(t *testing.T) {
	f := sessionFixtureTest(t)
	admin := nativeOpenTest(t, f)
	for _, action := range []string{"lost", "cancel", "lease-close", "session-close"} {
		t.Run(action, func(t *testing.T) {
			draft := schemaDraftTest()
			draft.Name = markerTest(f, "schema_fault_"+action)
			draft.LabelsOrTypes = []string{"SchemaFault_" + f.Nonce + "_" + action}
			relay := newNativeRelay(t, f, "CREATE RANGE INDEX `"+draft.Name+"`")
			original := f
			u, _ := url.Parse(f.Config.URI)
			u.Scheme, u.Host = "bolt", fmt.Sprintf("localhost:%d", relay.listener.Addr().(*net.TCPAddr).Port)
			original.Config.URI, original.Config.CAFile, original.Config.ClientCertFile, original.Config.ClientKeyFile = u.String(), "", "", ""
			s := nativeSessionTest(t, original)
			l := nativeSchemaManagementTest(t, s, 1)
			p := nativeSchemaPrepareManagementTest(t, l, draft)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type reply struct {
				r contract.GraphSchemaManagementResult
				e error
			}
			out := make(chan reply, 1)
			go func() { r, e := l.Submit(ctx, p, contract.GraphSchemaConfirmation{}); out <- reply{r, e} }()
			select {
			case <-relay.observed:
			case <-time.After(15 * time.Second):
				t.Fatal("original native DDL not dispatched")
			}
			committed := false
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				if schemaNativeNameExistsTest(t, admin, draft.Name, "index") {
					committed = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !committed {
				cancel()
				relay.Close()
				<-out
				t.Fatal("original native schema mutation not independently committed")
			}
			t.Cleanup(func() { nativeRunTest(t, admin, Request{Query: "DROP INDEX `" + draft.Name + "`"}) })
			closed := make(chan error, 1)
			switch action {
			case "lost":
				relay.mu.Lock()
				pairs := append([]net.Conn(nil), relay.pairs...)
				relay.mu.Unlock()
				for _, c := range pairs {
					c.Close()
				}
			case "cancel":
				cancel()
			case "lease-close":
				go func() { closed <- l.Close() }()
			case "session-close":
				go func() { closed <- s.Close() }()
			}
			var result reply
			select {
			case result = <-out:
			case <-time.After(15 * time.Second):
				relay.Close()
				t.Fatal("original schema executor did not acknowledge interruption")
			}
			if result.e == nil || result.r.State != "uncertain" || !result.r.Receipt.Uncertain || result.r.Receipt.Completed || result.r.ReceiptValid || relay.dispatches.Load() != 1 {
				t.Fatal("missing original schema response became successful/replayed", result.e)
			}
			if action == "lease-close" || action == "session-close" {
				select {
				case e := <-closed:
					if e != nil {
						t.Fatal(e)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("close did not wait for original executor")
				}
			}
			relay.hold.Store(false)
			if action == "lost" || action == "cancel" {
				observed, e := l.Verify(context.Background())
				if e != nil || !observed.HasVerification || !observed.Matched || observed.State != "uncertain" || observed.ReceiptValid || observed.Receipt.Completed {
					t.Fatal("matching catalog invented missing original completion", e)
				}
			}
			if _, e := l.Submit(context.Background(), p, contract.GraphSchemaConfirmation{}); !errors.Is(e, contract.ErrRevoked) {
				t.Fatal("original schema write replayable", e)
			}
			fresh := nativeSessionTest(t, f)
			next := nativeSchemaManagementTest(t, fresh, 1)
			_ = s.Close()
			_ = l.Close()
			if nativeSchemaReadTest(t, next).Snapshot().Object.ProfileID != s.profileID || !fresh.Alive() || !schemaNativeNameExistsTest(t, admin, draft.Name, "index") || relay.dispatches.Load() != 1 {
				t.Fatal("old cleanup/replay affected replacement")
			}
		})
	}
}

func TestNeo4jNativeSchemaManagementShowTransactionAllowsSameNameReplacement(t *testing.T) {
	f := sessionFixtureTest(t)
	original := nativeOpenTest(t, f)
	other := nativeOpenTest(t, f)
	name := markerTest(f, "schema_no_atomic_compare")
	label := "SchemaNoCAS_" + f.Nonce
	create := func(property string) {
		nativeRunTest(t, other, Request{Query: "CREATE RANGE INDEX `" + name + "` FOR (n:`" + label + "`) ON (n.`" + property + "`)"})
	}
	create("original")
	removed := false
	t.Cleanup(func() {
		if !removed {
			nativeRunTest(t, other, Request{Query: "DROP INDEX `" + name + "`"})
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	noRetry := true
	session := original.driver.NewSession(ctx, nd.SessionConfig{DatabaseName: "neo4j", DisableAutoCommitRetries: &noRetry})
	defer session.Close(context.Background())
	tx, e := session.BeginTransaction(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Close(context.Background())
	q := "SHOW INDEXES YIELD name,id,properties WHERE name=$name RETURN id,properties"
	r, e := tx.Run(ctx, q, map[string]any{"name": name})
	if e != nil || !r.Next(ctx) {
		t.Fatal("original transactional SHOW missing", e)
	}
	before := r.Record().Values[0]
	if _, e = r.Consume(ctx); e != nil {
		t.Fatal(e)
	}
	nativeRunTest(t, other, Request{Query: "DROP INDEX `" + name + "`"})
	create("replacement")
	native := nativeRunTest(t, other, Request{Query: q, Parameters: map[string]any{"name": name}, Mode: "read"})
	if fmt.Sprint(before) == native.Rows[0][0].Text || native.Rows[0][1].Values[0].Text != "replacement" {
		t.Fatal("external same-name replacement not observed")
	}
	r, e = tx.Run(ctx, "DROP INDEX `"+name+"`", nil)
	if e != nil {
		t.Fatal(e)
	}
	summary, e := r.Consume(ctx)
	if e != nil || summary.Counters().IndexesRemoved() != 1 {
		t.Fatal("original DROP completion missing", e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	removed = true
	if schemaNativeNameExistsTest(t, other, name, "index") {
		t.Fatal("native DROP did not affect same-name replacement")
	}
}
