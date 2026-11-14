package consul

import (
	"errors"
	"testing"
)

func TestConsulReadOnlyProfileSuppressesGridWritesAndRejectsAllNativeWriteKinds(t *testing.T) {
	s, calls := documentSession(t, nil)
	s.cfg.ReadOnly = true
	s.root.config.ReadOnly = true
	if s.Capabilities().EditableTables || s.DataDocumentCapabilities().EditableTables {
		t.Fatal("read-only session advertised editable grid")
	}
	l, err := s.ReserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	runtime := l.Runtime()
	if runtime.Capabilities.EditableTables || runtime.Saver != nil || runtime.SubmitEdits != nil || runtime.PreviewKeyCopy != nil || runtime.CaptureKeyPrefixDelete != nil || len(runtime.EditKinds) != 0 {
		t.Fatal("read-only grid retained mutation tools")
	}
	before := calls.Load()
	for _, request := range []Request{
		putRequest("original", []byte("value")),
		{Method: "DELETE", Path: "/v1/kv/original"},
		{Method: "DELETE", Path: "/v1/query/original"},
		{Method: "PUT", Path: "/v1/agent/service/deregister/original"},
		{Method: "PUT", Path: "/v1/session/destroy/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
	} {
		if _, err = s.root.Prepare(request); !errors.Is(err, ErrUnsupported) {
			t.Fatal("read-only native write accepted", request.Path, err)
		}
	}
	if calls.Load() != before {
		t.Fatal("read-only rejection sent HTTP")
	}
}
