package gokb

// IsValid prevents database/sql from pooling a cancelled or broken physical
// connection. The owning operation has acknowledged cancellation before this
// method is called; cancellation goroutines do not mutate connection state.
func (cn *conn) IsValid() bool { return !cn.bad }
