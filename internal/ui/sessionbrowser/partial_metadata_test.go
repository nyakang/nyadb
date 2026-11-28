package sessionbrowser

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestPartialMetadataPublishesIndependentTablesWithExplicitNoticeAndRetry(t *testing.T) {
	s, done, notify := connectedState(t)
	w := s.Current()
	ns := contract.NamespaceRef{ProfileID: w.ProfileID, Catalog: "Manticore"}
	w.Namespaces = map[string]contract.NamespaceRef{"app": ns}
	w.Cache["app"] = []contract.DBObject{{Name: "stale_function", Kind: "函数"}}
	var calls atomic.Int32
	s.ObjectLister = func(context.Context, int64, string) ([]contract.DBObject, error) {
		objects := []contract.DBObject{{Ref: ns.Object("TABLE", "readable"), Name: "readable", Kind: "TABLE"}}
		if calls.Add(1) == 1 {
			return objects, &contract.PartialMetadataError{Observation: contract.NativeMetadataObservation{Command: "SHOW PLUGINS", ErrorCode: 1045, SQLState: "42000", Error: "permission denied"}}
		}
		return objects, nil
	}
	s.LoadObjects("app", true, notify)
	awaitJob(t, done)
	s.Consume()
	if len(w.Cache["app"]) != 1 || w.Cache["app"][0].Name != "readable" || w.QualifiedCache[ns][0].Name != "readable" || !strings.Contains(w.Errors["app"], "SHOW PLUGINS [1045/42000]") {
		t.Fatal("independent subset or native notice lost", w.Cache, w.Errors)
	}
	s.LoadExpanded(func(string) bool { return true }, notify)
	if calls.Load() != 1 {
		t.Fatal("partial metadata retried automatically")
	}
	s.LoadObjects("app", true, notify)
	awaitJob(t, done)
	s.Consume()
	if w.Errors["app"] != "" || len(w.Cache["app"]) != 1 || calls.Load() != 2 {
		t.Fatal("explicit retry did not settle native notice")
	}
	// An unrelated transport failure cannot publish incidental partial rows.
	s.ObjectLister = func(context.Context, int64, string) ([]contract.DBObject, error) {
		return []contract.DBObject{{Name: "unverified"}}, errors.New("connection failed")
	}
	s.LoadObjects("app", true, notify)
	awaitJob(t, done)
	s.Consume()
	if w.Cache["app"][0].Name != "readable" || w.Errors["app"] != "connection failed" {
		t.Fatal("ordinary failure replaced verified subset")
	}
}
