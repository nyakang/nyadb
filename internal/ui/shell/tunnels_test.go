package shell

import (
	"context"
	"errors"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/transport"
	"testing"
	"time"
)

func TestSSHHostConfirmationNativeTesterTrustRejectAndClose(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(map[bool]string{false: "reject", true: "trust"}[accept], func(t *testing.T) {
			s := New()
			s.EnableTunnelServices(credentials.NewTunnelStore(false), &credentials.MemoryHostStore{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan bool, 1)
			go func() {
				ok, _ := s.tunnelRuntime.confirm(ctx, transport.HostIdentity{Host: "fixture:22", Fingerprint: "SHA256:fixture"})
				result <- ok
			}()
			deadline := time.Now().Add(time.Second)
			for len(s.tunnelRuntime.requests) == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			tt := ui.NewTester(s.tunnelConfirmation, 800, 600)
			if !tt.HasText("确认 SSH 主机身份") {
				t.Fatal("missing fingerprint confirmation")
			}
			button := "拒绝"
			if accept {
				button = "信任此主机"
			}
			if e := tt.Click(button); e != nil {
				t.Fatal(e)
			}
			select {
			case ok := <-result:
				if ok != accept {
					t.Fatal("wrong trust decision")
				}
			case <-time.After(time.Second):
				t.Fatal("confirmation worker did not acknowledge")
			}
			if s.tunnelRuntime.pending != nil {
				t.Fatal("pending confirmation retained")
			}
			s.closeTunnelServices()
		})
	}
	s := New()
	s.EnableTunnelServices(credentials.NewTunnelStore(false), &credentials.MemoryHostStore{})
	result := make(chan error, 1)
	go func() {
		_, e := s.tunnelRuntime.confirm(context.Background(), transport.HostIdentity{Host: "fixture:22", Fingerprint: "SHA256:fixture"})
		result <- e
	}()
	s.closeTunnelServices()
	select {
	case e := <-result:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("close left confirmation worker")
	}
}
