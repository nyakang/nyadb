package shell

import (
	"context"
	"github.com/nyakang/nyadb/locales"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
	"github.com/nyakang/nyadb/internal/transport"
)

type hostConfirmation struct {
	ctx      context.Context
	identity transport.HostIdentity
	reply    chan bool
}
type tunnelRuntime struct {
	store      *credentials.TunnelStore
	hosts      transport.HostStore
	requests   chan hostConfirmation
	pending    *hostConfirmation
	visible    bool
	mu         sync.Mutex
	invalidate func()
	closed     chan struct{}
	once       sync.Once
}

// EnableTunnelServices is called by bootstrap after installing the native
// credential vault. Headless tests can inject memory stores and mock dialers.
func (s *State) EnableTunnelServices(store *credentials.TunnelStore, hosts transport.HostStore) {
	r := &tunnelRuntime{store: store, hosts: hosts, requests: make(chan hostConfirmation, 16), closed: make(chan struct{})}
	s.tunnelRuntime = r
	if s.sessions.Manager != nil {
		s.sessions.Manager.TransportInvalidated = func() {
			r.mu.Lock()
			invalidate := r.invalidate
			r.mu.Unlock()
			if invalidate != nil {
				invalidate()
			}
		}
		s.sessions.Manager.TransportProvider = func(ctx context.Context, settings connections.ActiveSettings) (transport.Dialer, error) {
			var secrets transport.Secrets
			var err error
			if settings.MySQLAdvanced.SSH.Enabled || settings.MySQLAdvanced.HTTP.Enabled {
				secrets, _, err = store.Get(settings.ProfileID, settings.ConfigurationName)
				if err != nil {
					return nil, err
				}
			}
			return transport.New(settings, transport.Options{Secrets: secrets, Hosts: hosts, ConfirmHost: r.confirm})
		}
	}
	s.connectionEditor.SetTunnelServices(store, func(ctx context.Context, settings connections.ActiveSettings, password string, secrets transport.Secrets) error {
		dialer, err := transport.New(settings, transport.Options{Secrets: secrets, Hosts: hosts, ConfirmHost: r.confirm})
		if err != nil {
			return err
		}
		return mysqlsvc.TestMySQLTransportConnection(ctx, settings, password, dialer)
	}, mysqlsvc.ValidateMySQLTransportSettings)
}
func (r *tunnelRuntime) confirm(ctx context.Context, id transport.HostIdentity) (bool, error) {
	request := hostConfirmation{ctx: ctx, identity: id, reply: make(chan bool, 1)}
	select {
	case r.requests <- request:
	case <-ctx.Done():
		return false, ctx.Err()
	case <-r.closed:
		return false, context.Canceled
	}
	r.mu.Lock()
	invalidate := r.invalidate
	r.mu.Unlock()
	if invalidate != nil {
		invalidate()
	}
	select {
	case ok := <-request.reply:
		return ok, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-r.closed:
		return false, context.Canceled
	}
}
func (s *State) tunnelConfirmation(c *ui.Context) {
	r := s.tunnelRuntime
	if r == nil {
		return
	}
	r.mu.Lock()
	r.invalidate = c.Services().Invalidate
	r.mu.Unlock()
	if r.pending != nil && r.pending.ctx.Err() != nil {
		r.pending = nil
		r.visible = false
	}
	if r.pending == nil {
		select {
		case q := <-r.requests:
			if q.ctx.Err() == nil {
				r.pending = &q
				r.visible = true
			}
		default:
		}
	}
	if r.pending == nil {
		return
	}
	q := r.pending
	wasVisible := r.visible
	choice := ui.AlertDialog(c, &r.visible, locales.Text("确认 SSH 主机身份"), q.identity.Host+"\n"+q.identity.Fingerprint+"\n确认后保存此指纹；后续指纹变化将拒绝连接。", "拒绝", locales.Text("信任此主机"))
	if choice >= 0 || (wasVisible && !r.visible) {
		accepted := choice == 1
		select {
		case q.reply <- accepted:
		default:
		}
		r.pending = nil
		r.visible = false
		c.Services().Invalidate()
	}
}
func (s *State) closeTunnelServices() {
	if r := s.tunnelRuntime; r != nil {
		r.once.Do(func() { close(r.closed) })
		r.store.ClearMemory()
		r.pending = nil
		r.visible = false
	}
}
func (s *State) forgetTunnelProfile(p connections.Profile) error {
	if s.tunnelRuntime == nil {
		return nil
	}
	store := s.tunnelRuntime.store
	if err := store.Forget(p.ID, ""); err != nil {
		return err
	}
	for _, v := range p.Variants {
		if err := store.Forget(p.ID, v.Name); err != nil {
			return err
		}
	}
	return nil
}
