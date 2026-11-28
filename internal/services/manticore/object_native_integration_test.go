package manticore

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func objectEvidence(t *testing.T, name string, value any) {
	t.Helper()
	raw, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	t.Log(name, string(raw))
	if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
		if e = os.WriteFile(filepath.Join(dir, name), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
func originalIndexes(t *testing.T, m *contract.TableStructure) contract.NativeMetadataObservation {
	t.Helper()
	if len(m.Indexes) != 0 || len(m.Definitions) != 1 || m.Definitions[0].Kind != "manticore_indexes" {
		t.Fatalf("fabricated relational indexes or missing native snapshot: %+v", m)
	}
	var o contract.NativeMetadataObservation
	if e := json.Unmarshal([]byte(m.Definitions[0].SQL), &o); e != nil {
		t.Fatal(e)
	}
	return o
}
func TestNativeObjectsUDFAndOriginalIndexObservations(t *testing.T) {
	s := openNative(t, nativeConfig(t))
	q := nativeLease(t, s, 7201)
	table := createNativeTable(t, q)
	object := s.Namespace("Manticore").Object("TABLE", table)
	runNative(t, q, "CREATE FUNCTION nyadb_fixed RETURNS INT SONAME 'nyadb_fixture.so'")
	dropped := false
	defer func() {
		if !dropped {
			runNative(t, q, "DROP FUNCTION nyadb_fixed")
		}
	}()
	original := runNative(t, q, "SHOW PLUGINS").Sets[0]
	functions, e := s.Browse(context.Background(), object.Namespace())
	if e != nil {
		t.Fatal(e)
	}
	var function contract.DBObject
	for _, f := range functions {
		if f.Kind == "函数" && f.Name == "nyadb_fixed" {
			function = f
		}
	}
	if function.Ref != object.Namespace().Object("function", "nyadb_fixed") || function.ReturnType != "INT" || !strings.Contains(function.Comment, "nyadb_fixture.so") {
		t.Fatal("lost original qualified function", function)
	}
	var observation contract.NativeMetadataObservation
	if e = json.Unmarshal([]byte(function.Definition), &observation); e != nil {
		t.Fatal(e)
	}
	if observation.Command != "SHOW PLUGINS" || observation.ErrorCode != 0 || !reflect.DeepEqual(observation.Result, original) {
		t.Fatal("original plugin row changed", observation, original)
	}
	actual := runNative(t, q, "SELECT nyadb_fixed()")
	if len(actual.Rows) != 1 || actual.Rows[0][0] != "69" {
		t.Fatal(actual)
	}
	objectEvidence(t, "native-function.json", function)
	before, e := s.TableMetadata(context.Background(), object)
	if e != nil {
		t.Fatal(e)
	}
	first := originalIndexes(t, before)
	if os.Getenv("NYADB_MANTICORE_VERSION") == "6.3.8" {
		if first.ErrorCode == 0 || first.Error == "" || first.SQLState == "" {
			t.Fatal("unsupported old native query presented as empty", first)
		}
	} else if first.ErrorCode != 0 || len(first.Result.Rows) != 0 || !metadataShape(first.Result, "Name", "Type", "Enabled", "Percent") {
		t.Fatal("expected real empty before disk flush", first)
	}
	objectEvidence(t, "native-indexes-before-flush.json", first)
	stableBefore, e := s.tableMetadata(context.Background(), object, false)
	if e != nil {
		t.Fatal(e)
	}
	runNative(t, q, "INSERT INTO "+table+" (id,title,qty,price,tag) VALUES (1,'猫 metadata',7,1.25,'original')")
	runNative(t, q, "FLUSH RAMCHUNK "+table)
	after, e := s.TableMetadata(context.Background(), object)
	if e != nil {
		t.Fatal(e)
	}
	last := originalIndexes(t, after)
	if os.Getenv("NYADB_MANTICORE_VERSION") == "6.3.8" {
		if last.ErrorCode == 0 {
			t.Fatal("unsupported query became successful", last)
		}
	} else {
		raw := runNative(t, q, "SHOW TABLE "+quote(table)+" INDEXES").Sets[0]
		if len(last.Result.Rows) < 4 || !reflect.DeepEqual(last.Result, raw) {
			t.Fatal("nonempty original indexes changed", last, raw)
		}
		names := map[string]bool{}
		for _, r := range last.Result.Rows {
			names[r[0].Text] = true
			if r[2].Text != "1" || r[3].Text != "100" {
				t.Fatal("native status lost", r)
			}
		}
		for _, name := range []string{"id", "qty", "price", "tag"} {
			if !names[name] {
				t.Fatal("missing original index", name)
			}
		}
	}
	objectEvidence(t, "native-indexes-after-flush.json", last)
	stableAfter, e := s.tableMetadata(context.Background(), object, false)
	if e != nil {
		t.Fatal(e)
	}
	if metadataSignature(stableBefore) != metadataSignature(stableAfter) || len(stableAfter.Definitions) != 0 {
		t.Fatal("volatile index coverage polluted data schema signature")
	}
	wrong := object
	wrong.ProfileID++
	if _, e = s.TableMetadata(context.Background(), wrong); !errors.Is(e, connections.ErrInvalid) {
		t.Fatal("foreign table", e)
	}
	if _, e = s.Browse(context.Background(), wrong.Namespace()); !errors.Is(e, connections.ErrInvalid) {
		t.Fatal("foreign browse", e)
	}
	if _, e = s.TableMetadata(context.Background(), function.Ref); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal("function treated as table", e)
	}
	runNative(t, q, "DROP FUNCTION nyadb_fixed")
	dropped = true
	functions, e = s.Browse(context.Background(), object.Namespace())
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range functions {
		if f.Kind == "函数" && f.Name == "nyadb_fixed" {
			t.Fatal("dropped function remained")
		}
	}
	objectEvidence(t, "native-functions-after-drop.json", functions)
}

func TestNativeObjectMetadataOriginalCancellationAndClose(t *testing.T) {
	cfg := nativeConfig(t)
	control := openNative(t, cfg)
	q := nativeLease(t, control, 7202)
	table := createNativeTable(t, q)
	for _, operation := range []string{"plugins", "indexes"} {
		for _, action := range []string{"cancel", "close"} {
			t.Run(operation+"/"+action, func(t *testing.T) {
				gate := newPacketGate(t, net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
				c := cfg
				host, port, _ := net.SplitHostPort(gate.listener.Addr().String())
				n, _ := strconv.Atoi(port)
				c.Host, c.Port = host, uint16(n)
				s := openNative(t, c)
				query := "SHOW PLUGINS"
				if operation == "indexes" {
					query = "SHOW TABLE " + quote(table) + " INDEXES"
				}
				observed := gate.arm(query, "hold")
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					if operation == "plugins" {
						v, e := s.Browse(ctx, s.Namespace("Manticore"))
						if e != nil && v != nil {
							e = errors.New("failed browse published partial objects")
						}
						done <- e
					} else {
						v, e := s.TableMetadata(ctx, s.Namespace("Manticore").Object("TABLE", table))
						if e != nil && v != nil {
							e = errors.New("failed read published partial structure")
						}
						done <- e
					}
				}()
				select {
				case packet := <-observed:
					t.Logf("held original %s response %x", operation, packet)
				case <-time.After(10 * time.Second):
					t.Fatal("no original native response")
				}
				if action == "cancel" {
					cancel()
				} else {
					if e := s.Close(); e != nil {
						t.Fatal(e)
					}
				}
				select {
				case e := <-done:
					if e == nil || !errors.Is(e, context.Canceled) {
						t.Fatal("metadata executor did not acknowledge original cancellation", e)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("metadata worker did not settle")
				}
				if gate.calls.Load() != 1 {
					t.Fatal("metadata replayed", gate.calls.Load())
				}
			})
		}
	}
}
