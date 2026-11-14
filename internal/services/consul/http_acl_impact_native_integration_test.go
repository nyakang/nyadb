package consul

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConsulNativeACLImpactAuthMethodActualIssuedTokensAndDeletion(t *testing.T) {
	base, cfg, secret := nativeClient(t)
	_, l := nativeACLDocumentSession(t, cfg, secret)
	if cfg.Auth != "token" {
		o, err := l.ObserveHTTPACLImpact(context.Background(), impactInput("auth-method", "owned-disabled"))
		if err == nil || o.Complete || len(o.Reads) != 1 || o.Reads[0].Result.Receipt.HTTP.Status != 401 {
			t.Fatal("disabled auth method impact lost refusal")
		}
		return
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("owned JWT key unavailable")
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal("owned validation key unavailable")
	}
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	method := "owned-impact-issued-method"
	nativeACLFixtureCall(t, base, "PUT", "/v1/acl/auth-method", map[string]any{"Name": method, "Type": "jwt", "Config": map[string]any{"JWTValidationPubKeys": []string{public}, "BoundAudiences": []string{"owned-audience"}}})
	alive := true
	t.Cleanup(func() {
		if alive {
			nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/auth-method/"+method, nil)
		}
	})
	binding := nativeACLFixtureCall(t, base, "PUT", "/v1/acl/binding-rule", map[string]string{"AuthMethod": method, "BindType": "service", "BindName": "owned-impact-login"})
	bindingID := nativeACLString(t, binding, "ID")
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"sub": "owned-subject", "aud": "owned-audience", "iat": time.Now().Unix(), "exp": time.Now().Add(10 * time.Minute).Unix()})
	signing := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal("owned JWT signature unavailable")
	}
	login := nativeACLFixtureCall(t, base, "POST", "/v1/acl/login", map[string]string{"AuthMethod": method, "BearerToken": signing + "." + base64.RawURLEncoding.EncodeToString(signature)})
	accessor := nativeACLString(t, login, "AccessorID")
	unrelated := nativeACLFixtureCall(t, base, "PUT", "/v1/acl/token", map[string]string{"Description": "owned unrelated token"})
	unrelatedID := nativeACLString(t, unrelated, "AccessorID")
	t.Cleanup(func() { nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/token/"+unrelatedID, nil) })
	o, err := l.ObserveHTTPACLImpact(context.Background(), impactInput("auth-method", method))
	if err != nil || !o.Complete || len(o.Reads) != 3 || len(o.References) != 2 {
		t.Fatal("actual issued token missing from native impact")
	}
	seen := map[string]bool{}
	for _, ref := range o.References {
		seen[ref.Resource+"/"+ref.Identity] = true
	}
	if !seen["binding-rule/"+bindingID] || !seen["token/"+accessor] {
		t.Fatal("native auth method references changed identity")
	}
	nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/auth-method/"+method, nil)
	alive = false
	missing, err := l.ObserveHTTPACLImpact(context.Background(), impactInput("auth-method", method))
	if err == nil || missing.Complete || len(missing.Reads) != 1 || missing.Reads[0].Result.Receipt.HTTP.Status != 404 {
		t.Fatal("deleted auth method accepted an empty impact")
	}
	list := nativeACLFixtureCall(t, base, "GET", "/v1/acl/tokens", nil)
	var rows []struct{ AccessorID string }
	if json.Unmarshal(list, &rows) != nil {
		t.Fatal("original management token inventory unavailable")
	}
	foundUnrelated := false
	for _, row := range rows {
		if row.AccessorID == accessor {
			t.Fatal("native deleted auth method retained issued token")
		}
		foundUnrelated = foundUnrelated || row.AccessorID == unrelatedID
	}
	if !foundUnrelated || !o.Complete {
		t.Fatal("auth deletion removed unrelated token or changed original evidence")
	}
}

func TestConsulNativeACLImpactOriginalFaultsCancellationAndNoReplay(t *testing.T) {
	base, cfg, secret := nativeClient(t)
	if cfg.Auth != "token" {
		_, l := nativeACLDocumentSession(t, cfg, secret)
		o, err := l.ObserveHTTPACLImpact(context.Background(), impactInput("token", impactDirect))
		if err == nil || o.Complete || len(o.Reads) != 1 || o.Reads[0].Result.Receipt.HTTP.Status != 401 {
			t.Fatal("disabled ACL lost refusal")
		}
		return
	}
	f := nativeACLSeed(t, base)
	for _, step := range []string{"target", "references"} {
		for _, mode := range []string{"lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "close"} {
			t.Run(step+"/"+mode, func(t *testing.T) {
				path, count := "/v1/acl/policy/"+f.policy, 1
				if step == "references" {
					path, count = "/v1/acl/tokens", 2
				}
				relay, calls, processed := nativeACLMutationFault(t, cfg, secret, mode, Request{Method: "GET", Path: path})
				_, l := nativeACLDocumentSession(t, relay, secret)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type completion struct {
					o   contract.KVHTTPACLImpact
					err error
				}
				done := make(chan completion, 1)
				go func() {
					o, err := l.ObserveHTTPACLImpact(ctx, impactInput("policy", f.policy))
					done <- completion{o, err}
				}()
				var body []byte
				select {
				case body = <-processed:
				case <-time.After(20 * time.Second):
					t.Fatal("original native impact read not processed")
				}
				closed := make(chan struct{})
				if mode == "cancel" {
					cancel()
				}
				if mode == "close" {
					go func() { l.Close(); close(closed) }()
				}
				var out completion
				select {
				case out = <-done:
				case <-time.After(20 * time.Second):
					t.Fatal("original impact executor failed to settle")
				}
				if mode == "close" {
					wait(t, closed)
				}
				if out.err == nil || out.o.Complete || len(out.o.Reads) != count || calls.Load() != 1 || l.s.reserved != 0 || l.planReserved != 0 || l.writeUnknown.Load() {
					t.Fatal("native incomplete impact accepted, replayed or leaked reservation")
				}
				r := out.o.Reads[count-1].Result.Receipt
				if r.HTTP == nil || !r.Attempted || !r.HTTP.Ended || r.WriteOutcomeUnknown || r.HTTP.WriteOutcomeUnknown {
					t.Fatal("native original partial read receipt lost")
				}
				if mode == "partial" && (!bytes.Equal(r.HTTP.Body, body[:len(body)/2]) || r.HTTP.EOF || r.HTTP.Captured) {
					t.Fatal("native partial impact boundary changed")
				}
				if l.LastResult().HTTP != nil || l.LastHTTPACLMutation().Receipt.HTTP != nil {
					t.Fatal("private impact entered generic/write cache")
				}
			})
		}
	}
}

func TestConsulNativeACLImpactOriginalReferencesAndUnchangedNativeIndices(t *testing.T) {
	base, cfg, secret := nativeClient(t)
	_, l := nativeACLDocumentSession(t, cfg, secret)
	if cfg.Auth != "token" {
		o, err := l.ObserveHTTPACLImpact(context.Background(), impactInput("policy", impactPolicy))
		if err == nil || o.Complete || len(o.Reads) != 1 || o.Reads[0].Result.Receipt.HTTP.Status != 401 {
			t.Fatal("ACL-disabled impact became empty references")
		}
		return
	}
	f := nativeACLSeed(t, base)
	createToken := func(fields map[string]any) string {
		raw := nativeACLFixtureCall(t, base, "PUT", "/v1/acl/token", fields)
		id := nativeACLString(t, raw, "AccessorID")
		t.Cleanup(func() { nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/token/"+id, nil) })
		return id
	}
	both := createToken(map[string]any{"Description": "owned direct and inherited", "Policies": []map[string]string{{"ID": f.policy}}, "Roles": []map[string]string{{"ID": f.role}}})
	for _, target := range []struct {
		resource, id string
		reads, refs  int
	}{
		{"policy", f.policy, 4, 4}, {"role", f.role, 2, 2}, {"auth-method", f.method, 3, 1}, {"binding-rule", f.binding, 1, 0}, {"token", both, 1, 0},
	} {
		t.Run(target.resource, func(t *testing.T) {
			o, err := l.ObserveHTTPACLImpact(context.Background(), impactInput(target.resource, target.id))
			if err != nil || !o.Complete || len(o.Reads) != target.reads || len(o.References) != target.refs {
				t.Fatal("native impact reference traversal failed")
			}
			for _, read := range o.Reads {
				if read.Result.LocalRecordID == "" || read.Result.Receipt.HTTP == nil || read.BodySHA256 != aclImpactBodyHash(read.Result.Receipt.HTTP.Body) || !reflect.DeepEqual(read.Result.Scope, o.Scope) {
					t.Fatal("native impact original evidence changed")
				}
			}
			if target.resource == "policy" {
				found := false
				for _, ref := range o.References {
					if ref.Identity == both {
						found = len(ref.Locations) == 2
					}
				}
				if !found {
					t.Fatal("native direct and inherited token not deduplicated")
				}
			}
			if l.LastResult().HTTP != nil || l.LastHTTPACLMutation().Receipt.HTTP != nil {
				t.Fatal("native impact leaked into generic or mutation receipts")
			}
		})
	}
	// Deleting another linked policy changes these returned token links while
	// the original target still exists and native token indices remain equal.
	other := nativeACLFixtureCall(t, base, "PUT", "/v1/acl/policy", map[string]string{"Name": "owned-impact-second", "Rules": `acl = "read"`})
	otherID := nativeACLString(t, other, "ID")
	alive := true
	t.Cleanup(func() {
		if alive {
			nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/policy/"+otherID, nil)
		}
	})
	nativeACLFixtureCall(t, base, "PUT", "/v1/acl/token/"+both, map[string]any{"Policies": []map[string]string{{"ID": f.policy}, {"ID": otherID}}, "Roles": []map[string]string{{"ID": f.role}}})
	input := impactInput("policy", f.policy)
	before, err := l.ObserveHTTPACLImpact(context.Background(), input)
	if err != nil || !before.Complete {
		t.Fatal("original pre-deletion impact failed")
	}
	stable, err := l.ObserveHTTPACLImpact(context.Background(), input)
	if err != nil || CompareACLImpact(before, stable) != nil {
		t.Fatal("unchanged native original impact did not revalidate")
	}
	nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/policy/"+otherID, nil)
	alive = false
	after, err := l.ObserveHTTPACLImpact(context.Background(), input)
	if err != nil || !after.Complete || !errors.Is(CompareACLImpact(before, after), ErrConflict) {
		t.Fatal("native changed links bypassed original body comparison")
	}
	if !reflect.DeepEqual(before.References, after.References) || !reflect.DeepEqual(before.Reads[1].Result.Receipt.HTTP.Metadata.Index, after.Reads[1].Result.Receipt.HTTP.Metadata.Index) {
		t.Fatal("fixture did not retain target memberships and query index")
	}
	find := func(o contract.KVHTTPACLImpact) contract.KVHTTPACLRow {
		for _, row := range o.Reads[1].Result.Projection.Rows {
			if row.Identity == both {
				return row
			}
		}
		t.Fatal("owned token missing from native impact")
		return contract.KVHTTPACLRow{}
	}
	old, newer := find(before), find(after)
	if !reflect.DeepEqual(old.ModifyIndex, newer.ModifyIndex) || reflect.DeepEqual(old.Raw, newer.Raw) {
		t.Fatal("fixture failed to expose link changes with unchanged ModifyIndex")
	}
	_, reader := nativeACLDocumentSession(t, cfg, f.readonly)
	redacted, err := reader.ObserveHTTPACLImpact(context.Background(), impactInput("token", f.accessor))
	if err == nil || redacted.Complete || len(redacted.Reads) != 1 || !redacted.Reads[0].Result.Projection.ContainsRedaction {
		t.Fatal("redacted target became complete original impact")
	}
	_, denied := nativeACLDocumentSession(t, cfg, f.denied)
	o, err := denied.ObserveHTTPACLImpact(context.Background(), input)
	if err == nil || o.Complete || len(o.Reads) != 1 || o.Reads[0].Result.Receipt.HTTP.Status != 403 {
		t.Fatal("native denied target became empty impact")
	}
	missing, err := l.ObserveHTTPACLImpact(context.Background(), impactInput("policy", "00000000-0000-0000-0000-000000000099"))
	if err == nil || missing.Complete || len(missing.Reads) != 1 || missing.Reads[0].Result.Receipt.HTTP.Status != 404 {
		t.Fatal("native missing target allowed reference enumeration")
	}
	// Restricting to two original reads cannot silently omit role-derived tokens.
	input.MaxReads = 2
	bounded, err := l.ObserveHTTPACLImpact(context.Background(), input)
	if !errors.Is(err, ErrBounds) || bounded.Complete || len(bounded.Reads) != 2 || l.s.reserved != 0 {
		t.Fatal("native incomplete bounded impact was accepted")
	}
}
