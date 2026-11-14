package dbruntime_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func TestConsulNativePublicRuntimeStructuredToolsCapturedOriginalProvider(t *testing.T) {
	p, secret := consulPublicProfile(t)
	p.ConsulOptions.Prefix = "native/runtime/tools//"
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	provider, err := m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	tools := provider.(contract.KVRequestPresentationProvider).KVRequestPresentation()
	builder := provider.(contract.KVRequestBuilderProvider).KVRequestBuilder()
	if builder == nil || len(tools.Tools) != 35 || tools.Tools[0].Default.Key != p.ConsulOptions.Prefix {
		t.Fatal("public runtime lost original tool builder or prefix")
	}
	tools.Tools[0].Default.Key = "foreign"
	if provider.(contract.KVRequestPresentationProvider).KVRequestPresentation().Tools[0].Default.Key != p.ConsulOptions.Prefix {
		t.Fatal("inspection mutated runtime presentation")
	}
	lease, err := provider.ReserveKV(10)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	input := contract.KVRequestToolInput{Key: "native/runtime/tool-original", ValueBase64: "AP8=", Flags: "18446744073709551615", CompareIndex: "0"}
	request, err := builder.BuildKVRequest(contract.KVToolPut, input)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := lease.Prepare(request)
	if err != nil || !plan.Writes() || plan.Snapshot() != request {
		t.Fatal("runtime preparation changed structured original request", err)
	}
	result, err := lease.Execute(context.Background(), plan)
	if err != nil || !result.Completed || !result.HTTP.Succeeded || !bytes.Equal(result.HTTP.Request.Body, []byte{0, 255}) || result.HTTP.Request.Query["cas"][0] != "0" || result.HTTP.Request.Query["flags"][0] != "18446744073709551615" {
		t.Fatal("runtime executed another structured request", err)
	}
	originalGeneration := lease.Scope().Generation
	p.ConsulOptions.Prefix = "native/runtime/replacement/"
	if err := m.Connect(context.Background(), p, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Prepare(request); err == nil {
		t.Fatal("retired original lease gained replacement authority")
	}
	if provider.(contract.KVRequestBuilderProvider).KVRequestBuilder() != nil {
		t.Fatal("retired provider returned a live builder")
	}
	offline, err := builder.BuildKVRequest(contract.KVToolRead, contract.KVRequestToolInput{Key: input.Key})
	if err != nil || offline.Method != contract.KVConsulHTTPRequestMethod {
		t.Fatal("captured pure builder depended on replacement session", err)
	}
	current, _ := m.KVRequests(p.ID)
	if current.KVScope().Generation <= originalGeneration || tools.Tools[1].Default.Key != "native/runtime/tools//" {
		t.Fatal("original tool templates changed with replacement")
	}
}
