package engines

import (
	"context"
	"sync"
)

// schemaGate coordinates arbitrary SQL with schema inspection/mutation. Waits
// are cancellable; schema writers cannot starve behind newly started queries.
// Data readers/writers already use SQLite's transactional locks and never
// create connection-local SQL objects, so they do not need this gate.
type schemaGate struct {
	mu               sync.Mutex
	readers, waiting int
	writer           bool
	changed          chan struct{}
}

func (g *schemaGate) signal() {
	if g.changed != nil {
		close(g.changed)
	}
	g.changed = make(chan struct{})
}
func (g *schemaGate) acquire(ctx context.Context, write bool) (func(), error) {
	g.mu.Lock()
	if g.changed == nil {
		g.changed = make(chan struct{})
	}
	if write {
		g.waiting++
	}
	for {
		if err := ctx.Err(); err != nil {
			if write {
				g.waiting--
				g.signal()
			}
			g.mu.Unlock()
			return nil, err
		}
		if !g.writer && (write && g.readers == 0 || !write && g.waiting == 0) {
			if write {
				g.waiting--
				g.writer = true
			} else {
				g.readers++
			}
			g.mu.Unlock()
			return func() {
				g.mu.Lock()
				if write {
					g.writer = false
				} else {
					g.readers--
				}
				g.signal()
				g.mu.Unlock()
			}, nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		g.mu.Lock()
	}
}
