package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativePrefixDeletePreview(t *testing.T, l *dataLease, prefix string) contract.DataMutationPlan {
	t.Helper()
	p, err := l.previewKeyPrefixDelete(context.Background(), l.object.ProfileID, l.object.Catalog, l.object.Name, contract.DataKeyPrefixDelete{Object: l.object, Prefix: prefix})
	if err != nil {
		t.Fatal("original captured prefix preview", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestConsulNativeDataKeyPrefixDeleteCompleteOriginalSetAndLaterKeysPreserved(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	cfg.Prefix = nativeDataPrefix(t) + "/"
	_, l := nativeDataSession(t, cfg, token)
	prefix := cfg.Prefix + "sub"
	keys := []string{prefix + "/full//猫", prefix + "/empty", prefix + "marine"}
	value := bytes.Repeat([]byte{0, 255}, valueLimit/2)
	for i, key := range keys {
		v := []byte{0, 255}
		if i == 0 {
			v = value
		}
		if i == 1 {
			v = nil
		}
		nativeCopyPut(t, writer, key, v)
	}
	outside := cfg.Prefix + "outside"
	nativeCopyPut(t, writer, outside, []byte("outside"))
	p := nativePrefixDeletePreview(t, l, prefix)
	snapshot := p.Snapshot()
	var observed struct {
		Scope        dataKeyPrefixDeletionScope
		BeforeImages []contract.KVHTTPEntry
		Operations   []TxnOperation
	}
	if err := json.Unmarshal([]byte(snapshot.NativeJSON), &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Scope.Prefix != prefix || observed.Scope.Candidates != 3 || !observed.Scope.Original.EOF || !observed.Scope.Original.Acknowledged || len(observed.BeforeImages) != 3 || len(observed.Operations) != 9 {
		t.Fatal("prefix plan lacks complete original scope")
	}
	found := false
	for _, e := range observed.BeforeImages {
		if e.Key == keys[0] {
			found = bytes.Equal(e.Value, value) && e.Flags == math.MaxUint64
		}
	}
	if !found {
		t.Fatal("prefix candidate lost complete bytes/Flags")
	}
	snapshot.Edits[0].Original.Metadata = "foreign"
	late := prefix + "/created-after-preview"
	nativeCopyPut(t, writer, late, []byte("late"))
	out := nativeMutationSubmit(t, l, p)
	if out.Applied != 3 {
		t.Fatal("captured candidate count changed")
	}
	for _, key := range keys {
		if !nativeMutationAbsent(t, writer, key) {
			t.Fatal("original candidate retained", key)
		}
	}
	if string(nativeRead(t, writer, late).Value) != "late" || string(nativeRead(t, writer, outside).Value) != "outside" {
		t.Fatal("captured set became unbounded prefix deletion")
	}
	if _, err := l.submit(context.Background(), 91, "kv", "keys", p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("prefix plan replayed", err)
	}
	if l.Runtime().PreviewKeyPrefixDelete == nil {
		t.Fatal("public prefix feature was not published")
	}
}

func TestConsulNativeDataKeyPrefixDeleteOriginalConflictsAndSingleTransactionBounds(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, change := range []string{"value", "flags", "delete", "recreate"} {
		t.Run(change, func(t *testing.T) {
			cfg.Prefix = nativeDataPrefix(t) + "/"
			_, l := nativeDataSession(t, cfg, token)
			earlier, changed := cfg.Prefix+"a", cfg.Prefix+"z"
			nativeCopyPut(t, writer, earlier, []byte("earlier"))
			nativeCopyPut(t, writer, changed, []byte("source"))
			p := nativePrefixDeletePreview(t, l, cfg.Prefix)
			switch change {
			case "value":
				nativeCopyPut(t, writer, changed, []byte("changed"))
			case "flags":
				r, _ := KeyPutRequest(changed, []byte("source"), u64(7), nil)
				nativeDo(t, writer, r)
			case "delete", "recreate":
				r, _ := KeyDeleteRequest(changed, nil, false)
				nativeDo(t, writer, r)
				if change == "recreate" {
					nativeCopyPut(t, writer, changed, []byte("source"))
				}
			}
			out, err := l.submit(context.Background(), 91, "kv", "keys", p)
			if !errors.Is(err, contract.ErrDataConflict) || out == nil || out.CommitConfirmed || out.CommitUncertain || out.Applied != 0 {
				t.Fatal("original prefix conflict not atomic", out, err)
			}
			if string(nativeRead(t, writer, earlier).Value) != "earlier" {
				t.Fatal("conflicting batch partially deleted earlier key")
			}
		})
	}
	cfg.Prefix = nativeDataPrefix(t) + "/bounds/"
	_, l := nativeDataSession(t, cfg, token)
	for i := 0; i < 21; i++ {
		nativeCopyPut(t, writer, cfg.Prefix+fmt.Sprintf("%02d", i), nil)
	}
	p := nativePrefixDeletePreview(t, l, cfg.Prefix)
	nativeCopyPut(t, writer, cfg.Prefix+"22", nil)
	if q, err := l.previewKeyPrefixDelete(context.Background(), 91, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: cfg.Prefix}); q != nil || !errors.Is(err, ErrBounds) {
		t.Fatal("22 candidates were split or admitted", err)
	}
	if _, err := l.submit(context.Background(), 91, "kv", "keys", p); err == nil {
		t.Fatal("superseded prefix preview still authorized")
	}
	for i := 0; i < 21; i++ {
		if nativeMutationAbsent(t, writer, cfg.Prefix+fmt.Sprintf("%02d", i)) {
			t.Fatal("limit rejection wrote partial prefix")
		}
	}
}

func TestConsulNativeDataKeyPrefixDeleteProcessedOriginalRepliesNeverReplay(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, mode := range []string{"lost", "partial", "missing", "extra-result", "wrong-check", "missing-check", "http500", "http408", "cancel", "close", "session-close"} {
		t.Run(mode, func(t *testing.T) {
			cfg.Prefix = nativeDataPrefix(t) + "/"
			key := cfg.Prefix + "processed"
			nativeCopyPut(t, writer, key, []byte{0, 255})
			proxy, calls, processed := nativeMutationRelay(t, cfg, token, mode)
			session, l := nativeDataSession(t, proxy, token)
			p := nativePrefixDeletePreview(t, l, cfg.Prefix)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				out *contract.DataEditResult
				err error
			}
			done := make(chan completion, 1)
			go func() { out, err := l.submit(ctx, 91, "kv", "keys", p); done <- completion{out, err} }()
			select {
			case <-processed:
			case <-time.After(10 * time.Second):
				t.Fatal("original deletion not processed")
			}
			switch mode {
			case "cancel":
				cancel()
			case "close":
				l.Close()
			case "session-close":
				session.Close()
			}
			var c completion
			select {
			case c = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("original deletion did not settle")
			}
			if c.out == nil || c.out.CommitConfirmed || c.out.Applied != 0 || !(c.out.CommitUncertain || c.out.Incomplete) || !errors.Is(c.err, contract.ErrDataCommitUncertain) {
				t.Fatal("processed deletion fault confirmed or lost original unknown", c.err)
			}
			if !nativeMutationAbsent(t, writer, key) {
				t.Fatal("fault preceded actual deletion")
			}
			if mode != "close" && mode != "session-close" {
				original := l.write.recoveryOwned().Unknown
				dataTestPage(t, l, contract.DataOptions{Limit: 10})
				if !reflect.DeepEqual(original, l.write.recoveryOwned().Unknown) {
					t.Fatal("later reads replaced original receipt")
				}
				if _, err := l.previewKeyPrefixDelete(context.Background(), 91, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: cfg.Prefix}); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal("prefix preview crossed unknown barrier", err)
				}
				if _, err := l.submit(context.Background(), 91, "kv", "keys", p); !errors.Is(err, contract.ErrDataCommitUncertain) {
					t.Fatal("original deletion replayed", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("original delete transaction retried")
			}
		})
	}
}

func TestConsulNativeDataKeyPrefixDeleteLocksOriginalOwnershipAndCaptureBudget(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	for _, change := range []string{"locked-before-preview", "acquire", "release", "destroy", "expire"} {
		t.Run(change, func(t *testing.T) {
			cfg.Prefix = nativeDataPrefix(t) + "/"
			_, l := nativeDataSession(t, cfg, token)
			key := cfg.Prefix + "key"
			nativeCopyPut(t, writer, key, []byte("source"))
			session := createSession(t, writer, "release")
			lock, _ := nativeMutationLockRequest(key, []byte("source"), nil, session, false)
			if change == "locked-before-preview" {
				nativeDo(t, writer, lock)
				if p, err := l.previewKeyPrefixDelete(context.Background(), 91, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: cfg.Prefix}); p != nil || !errors.Is(err, contract.ErrUnsupported) {
					t.Fatal("original locked prefix admitted", err)
				}
				return
			}
			p := nativePrefixDeletePreview(t, l, cfg.Prefix)
			nativeDo(t, writer, lock)
			switch change {
			case "release":
				r, _ := nativeMutationLockRequest(key, []byte("source"), nil, session, true)
				nativeDo(t, writer, r)
			case "destroy":
				nativeDo(t, writer, Request{Method: "PUT", Path: "/v1/session/destroy/" + session})
			case "expire":
				deadline := time.Now().Add(30 * time.Second)
				for nativeRead(t, writer, key).Session != "" {
					if time.Now().After(deadline) {
						t.Fatal("original session did not expire")
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
			before := nativeRead(t, writer, key)
			out, err := l.submit(context.Background(), 91, "kv", "keys", p)
			if !errors.Is(err, contract.ErrDataConflict) || out == nil || out.CommitConfirmed || out.CommitUncertain || !reflect.DeepEqual(before, nativeRead(t, writer, key)) {
				t.Fatal("prefix lock change was not a known original conflict", err)
			}
		})
	}
	cfg.Prefix = nativeDataPrefix(t) + "/budget/"
	_, l := nativeDataSession(t, cfg, token)
	value := bytes.Repeat([]byte{0, 255}, valueLimit/2)
	for i := 0; i < 8; i++ {
		nativeCopyPut(t, writer, cfg.Prefix+fmt.Sprintf("%02d", i), value)
	}
	if p, err := l.previewKeyPrefixDelete(context.Background(), 91, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: cfg.Prefix}); p != nil || !errors.Is(err, ErrBounds) {
		t.Fatal("complete retained prefix exceeded original plan reservation", err)
	}
	for i := 0; i < 8; i++ {
		if !bytes.Equal(nativeRead(t, writer, cfg.Prefix+fmt.Sprintf("%02d", i)).Value, value) {
			t.Fatal("capture budget rejection changed a candidate")
		}
	}

	cfg.Prefix = nativeDataPrefix(t) + "/owner/"
	original, owner := nativeDataSession(t, cfg, token)
	key := cfg.Prefix + "owned"
	nativeCopyPut(t, writer, key, []byte("original"))
	p := nativePrefixDeletePreview(t, owner, cfg.Prefix)
	_, peer := nativeDataSession(t, cfg, token)
	if _, err := peer.submit(context.Background(), 91, "kv", "keys", p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("prefix plan crossed original lease", err)
	}
	before := owner.write.LastResult()
	foreign := owner.object
	foreign.ProfileID++
	if plan, err := owner.previewKeyPrefixDelete(context.Background(), 91, "kv", "keys", contract.DataKeyPrefixDelete{Object: foreign, Prefix: cfg.Prefix}); plan != nil || err == nil || !reflect.DeepEqual(before, owner.write.LastResult()) {
		t.Fatal("foreign prefix request made I/O", err)
	}
	original.Close()
	if _, err := owner.submit(context.Background(), 91, "kv", "keys", p); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original prefix plan remained authorized", err)
	}
	if string(nativeRead(t, writer, key).Value) != "original" {
		t.Fatal("revoked prefix plan deleted original key")
	}
}

func TestConsulNativeDataKeyPrefixDeleteRestrictedACLFilteringAndOriginalWriteDenial(t *testing.T) {
	writer, cfg, _ := nativeClient(t)
	token := os.Getenv("NYADB_CONSUL_READ_TOKEN")
	if token == "" {
		return
	}
	t.Run("filtered", func(t *testing.T) {
		visible := "allowed/" + nativeDataPrefix(t) + "/visible"
		hidden := "allowed-hidden/" + nativeDataPrefix(t)
		nativeCopyPut(t, writer, visible, []byte("visible"))
		nativeCopyPut(t, writer, hidden, []byte("hidden"))
		cfg.Auth, cfg.Prefix = "token", "allowed"
		_, l := nativeDataSession(t, cfg, token)
		p, err := l.previewKeyPrefixDelete(context.Background(), 91, "kv", "keys", contract.DataKeyPrefixDelete{Object: l.object, Prefix: "allowed"})
		r := l.write.LastResult()
		if p != nil || !errors.Is(err, contract.ErrUnsupported) || r.HTTP == nil || r.HTTP.Metadata.ACLFiltered == nil || !*r.HTTP.Metadata.ACLFiltered || l.uncertain {
			t.Fatal("filtered prefix was treated as complete", err)
		}
		if string(nativeRead(t, writer, visible).Value) != "visible" || string(nativeRead(t, writer, hidden).Value) != "hidden" {
			t.Fatal("ACL-filtered preview changed source keys")
		}
	})
	t.Run("denied-submit", func(t *testing.T) {
		cfg.Auth, cfg.Prefix = "token", "allowed/"+nativeDataPrefix(t)+"/"
		key := cfg.Prefix + "key"
		nativeCopyPut(t, writer, key, []byte("original"))
		_, l := nativeDataSession(t, cfg, token)
		p := nativePrefixDeletePreview(t, l, cfg.Prefix)
		out, err := l.submit(context.Background(), 91, "kv", "keys", p)
		r := l.write.LastResult()
		if err == nil || out == nil || out.CommitConfirmed || out.CommitUncertain || out.Applied != 0 || l.uncertain || r.HTTP == nil || r.HTTP.Status != 409 || r.WriteOutcomeUnknown || !strings.Contains(strings.ToLower(err.Error()), "permission") {
			t.Fatal("original prefix ACL rejection lost native evidence", err)
		}
		if string(nativeRead(t, writer, key).Value) != "original" {
			t.Fatal("denied prefix submit changed source")
		}
	})
}
