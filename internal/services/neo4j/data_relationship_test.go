package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func signedEndpointTest(l *dataLease) *contract.DataGraphEndpoints {
	e := &contract.DataGraphEndpoints{Object: l.object,
		Source: Node{ElementID: "source", Labels: []string{"original"}, Properties: []Property{{Name: "list", Value: Value{Kind: "list", Values: []Value{{Kind: "integer", Text: "9223372036854775807"}}}}}},
		Target: Node{ElementID: "target"}, NativeJSON: `{"original":"read"}`}
	e.Token = l.sign(graphEndpointSigningValue(*e))
	return e
}

func TestNeo4jRelationshipRequestsCaptureEndpointsAndRejectForeignOrAlteredObservations(t *testing.T) {
	s := fakeSessionTest(t)
	l := dataLeaseTest(t, s, "relationship", "owned")
	e := signedEndpointTest(l)
	input := contract.DataEdit{Kind: "insert", GraphEndpoints: e, Values: map[string]contract.DataValue{"property.bytes": {Kind: contract.Binary, Bytes: []byte{0, 255}}}}
	p, err := l.prepareEntityEdits([]contract.DataEdit{input})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(p[0].edit)
	e.Source.Labels[0] = "changed"
	e.Source.Properties[0].Value.Values[0].Text = "0"
	input.Values["property.bytes"].Bytes[0] = 9
	got, _ := json.Marshal(p[0].edit)
	if string(got) != string(want) || p[0].params["bytes"].([]byte)[0] != 0 {
		t.Fatal("relationship worker followed mutable endpoint evidence or bytes")
	}
	for _, mutate := range []func(*contract.DataGraphEndpoints){
		func(e *contract.DataGraphEndpoints) { e.Object.ProfileID++ },
		func(e *contract.DataGraphEndpoints) { e.Object.Catalog = "foreign" },
		func(e *contract.DataGraphEndpoints) { e.Object.Name = "other" },
		func(e *contract.DataGraphEndpoints) { e.Source.ElementID = "other" },
		func(e *contract.DataGraphEndpoints) { e.Target.LegacyID++ },
		func(e *contract.DataGraphEndpoints) { e.Source.Labels[0] = "other" },
		func(e *contract.DataGraphEndpoints) { e.NativeJSON = `{}` },
		func(e *contract.DataGraphEndpoints) { e.Token = strings.Repeat("0", 64) },
	} {
		copy := signedEndpointTest(l)
		mutate(copy)
		if _, err := l.prepareEntityEdits([]contract.DataEdit{{Kind: "insert", GraphEndpoints: copy}}); err == nil {
			t.Fatal("altered original endpoint observation admitted")
		}
	}
	other := dataLeaseTest(t, s, "relationship", "owned")
	if _, err := other.prepareEntityEdits([]contract.DataEdit{p[0].edit}); err == nil {
		t.Fatal("endpoint observation crossed document signing keys")
	}
	for _, kind := range []string{"update", "delete", "detach_delete"} {
		if _, err := l.prepareEntityEdits([]contract.DataEdit{{Kind: kind, GraphEndpoints: signedEndpointTest(l)}}); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("non-create operation silently ignored endpoints", kind, err)
		}
	}
	if _, err := l.save(context.Background(), 42, "neo4j", "owned", []contract.DataEdit{p[0].edit}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("ordinary Saver bypassed owning relationship preview", err)
	}
	if _, err := l.prepareEntityEdits([]contract.DataEdit{{Kind: "insert", GraphEndpoints: signedEndpointTest(l), Values: map[string]contract.DataValue{"property.large": {Kind: contract.Text, Text: strings.Repeat("x", contract.DataEditMaxBytes)}}}}); !errors.Is(err, ErrBounds) {
		t.Fatal("endpoint payload was omitted from batch byte budget", err)
	}
}

func TestNeo4jEntityCountersRequireActualCreationAndRejectMissingOrExtraEffects(t *testing.T) {
	want := map[string]int64{"relationships_created": 1}
	for _, counters := range []map[string]int64{nil, {"relationships_created": 0}, {"relationships_created": 1, "nodes_created": 1}} {
		if entityCounters(&Response{Summary: Summary{Counters: counters}}, want) {
			t.Fatal("missing creation receipt or unrequested node effect accepted")
		}
	}
	if !entityCounters(&Response{Summary: Summary{Counters: map[string]int64{"relationships_created": 1, "nodes_created": 0}}}, want) {
		t.Fatal("exact relationship creation receipt rejected")
	}
}
