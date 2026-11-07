# NyaDB native Kingbase driver patch

Based on `gitea.com/kingbase/gokb` at
`v0.0.0-20201021123113-29bd62a876c3`, commit
`29bd62a876c30fc79f64efd0985712f91b005f2e`. `UPSTREAM.json` records the module
checksum and original hashes for all retained production sources. The original
MIT license is retained. Authentication, native OIDs and value decoding are
unchanged. No server binaries or license files are distributed here.

The local patch adds:

- An explicit typed configuration constructor, copied runtime settings and an
  injected context dialer. It does not read environment settings, OS usernames,
  password files or home-directory certificates. The legacy DSN constructor
  remains available for upstream API compatibility; NyaDB never uses it.
- Explicit native TLS configurations and verified handshakes, used by the
  original connection and its separate cancellation socket.
- Context bounds and captured-socket cleanup throughout TLS, authentication and
  startup; successful connections clear the startup deadline. Failed deadline
  setup also closes its captured socket.
- Cancellation installed before BEGIN, prepare and prepared executions; already
  cancelled requests perform no protocol I/O. Cancellation reads the actual TLS
  socket until EOF, bounds acknowledgement I/O and waits for cleanup. Failed
  cancellation delivery also closes the original query socket.
- Idempotent finish callbacks and retirement of cancelled physical connections.
  Only the owning operation changes connection state after the cancellation
  goroutine finishes. `driver.Validator` prevents pooling broken connections.
- `ErrOutcomeUnknown` for lost physical write/commit acknowledgements. Such
  errors deliberately do not match `driver.ErrBadConn`, which would let
  `database/sql` replay a potentially committed operation. Native server and
  network errors remain available through `errors.As`.

The nested module uses Go 1.27.3, matching the application. Its tests are not
included in the root module's `go test ./...`; run them explicitly:

```sh
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
```

Tests cover explicit configuration isolation, real TLS cancellation packets,
EOF acknowledgement waiting, failed cancel delivery, cancelled BEGIN and
prepared requests, stalled connection initialization, deadline clearing, and
lost write/commit acknowledgements without replay. Real product authentication,
values, transactions and cancellation are exercised separately by
`tools/verify_kingbase_fixture.py` in the application repository.
