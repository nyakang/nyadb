# NyaDB driver patch

Based on official openGauss Go connector v1.0.9, commit
`1b2ca1e407de54157316fcb4a808d8a87e007dac`. Upstream hashes are recorded in
UPSTREAM.json; the original MIT license and production packages are retained.
NyaDB does not alter SHA256/SM3 authentication or value decoding.

The local patch corrects:

- TLS cancellation handshakes the new cancellation socket, preserving the
  original query socket and its captured backend key.
- Cancellation I/O is bounded by its context and waits for acknowledgement
  before the caller settles and releases the original handle.
- Transaction BEGIN installs cancellation before sending its first command.
- Prepared executions follow the same cancellation acknowledgement and socket
  retirement protocol; already cancelled requests perform no protocol I/O.
- Connection deadlines cover TLS/authentication/startup; cancellation closes
  only the captured original socket, and successful startup clears deadlines.

Standalone regression tests exercise real verified TLS cancellation packets,
acknowledgement waiting, failed cancellation delivery, cancelled BEGIN, stalled
TLS/authentication initialization and successful deadline clearing. Run this
nested module explicitly: `GOWORK=off go test -race ./...`. The root module's
`go test ./...` does not include it.
