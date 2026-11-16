package sessionbrowser

import (
	"context"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type nativeOnlyTestSession struct {
	id       int64
	metadata bool
}

func (s *nativeOnlyTestSession) ProfileID() int64 { return s.id }
func (*nativeOnlyTestSession) Generation() uint64 { return 101 }
func (s *nativeOnlyTestSession) Capabilities() contract.Capabilities {
	return contract.Capabilities{KeyValues: true, Metadata: s.metadata}
}
func (*nativeOnlyTestSession) Close() error { return nil }

func TestNativeSessionConnectPublishesWithoutInventedMetadataWhileParked(t *testing.T) {
	for _, parked := range []bool{false, true} {
		done := make(chan struct{}, 1)
		connected := int64(0)
		s := New(Dependencies{Connected: func(id int64) { connected = id }})
		defer s.Close()
		session := &nativeOnlyTestSession{id: 7}
		s.SetEngineRuntime(EngineRuntime{
			Connect: func(context.Context, connections.Profile, string) error { return nil },
			Session: func(int64) (contract.Session, error) { return session, nil },
			Open:    func(context.Context, connections.Profile, string) (contract.Session, error) { return session, nil },
		})
		p, _ := connections.Defaults(connections.ZooKeeper)
		p.ID, p.Name = 7, "original"
		s.Connect(p, func() { done <- struct{}{} })
		if parked {
			s.Activate(9)
			s.Current().ProfileID = 9
		}
		awaitJob(t, done)
		s.Consume()
		w := s.Workspace(p.ID)
		if w.Status != "已连接" || w.Error != "" || w.Generation != session.Generation() || len(w.Databases) != 0 || len(w.Namespaces) != 0 || w.Catalog != "" || connected != p.ID {
			t.Fatal("successful native handshake lost generation or invented metadata", w.Status, w.Error)
		}
		if parked && s.Current().ProfileID != 9 {
			t.Fatal("parked native handshake activated another profile")
		}
	}
}

func TestSessionAdvertisingMetadataStillReportsMissingFeatureContract(t *testing.T) {
	done := make(chan struct{}, 1)
	s := New(Dependencies{})
	defer s.Close()
	session := &nativeOnlyTestSession{id: 7, metadata: true}
	s.SetEngineRuntime(EngineRuntime{
		Connect: func(context.Context, connections.Profile, string) error { return nil },
		Session: func(int64) (contract.Session, error) { return session, nil },
		Open:    func(context.Context, connections.Profile, string) (contract.Session, error) { return session, nil },
	})
	p, _ := connections.Defaults(connections.ZooKeeper)
	p.ID, p.Name = 7, "original"
	s.Connect(p, func() { done <- struct{}{} })
	awaitJob(t, done)
	s.Consume()
	if s.Current().Error == "" || !strings.Contains(s.Current().Error, "not supported") {
		t.Fatal("advertised metadata without a contract was hidden", s.Current().Error)
	}
}
