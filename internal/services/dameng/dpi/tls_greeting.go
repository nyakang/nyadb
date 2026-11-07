package dpi

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
)

// The native DM greeting has a 64-byte header: uint16 kind at 4, uint32 body
// length at 6, int32 status at 10 and uint32 SSL mode at 20, all little-endian.
// Only the initial kind=200 exchange is bounded/read here. It is forwarded
// intact; all subsequent native authentication and message semantics stay DPI's.
func readGreeting(connection net.Conn) ([]byte, error) {
	header := make([]byte, 64)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint32(header[6:10])
	kind := binary.LittleEndian.Uint16(header[4:6])
	if (kind != 200 && kind != 228) || length > 64<<10 {
		return nil, fmt.Errorf("unsupported bounded DM8 native greeting (kind %d, body %d)", binary.LittleEndian.Uint16(header[4:6]), length)
	}
	packet := append(header, make([]byte, int(length))...)
	_, err := io.ReadFull(connection, packet[64:])
	return packet, err
}

func (t *transport) negotiate(downstream net.Conn) (net.Conn, net.Conn, error) {
	request, err := readGreeting(downstream)
	if err != nil {
		return nil, nil, err
	}
	if _, err = io.Copy(t.upstream, bytes.NewReader(request)); err != nil {
		return nil, nil, err
	}
	response, err := readGreeting(t.upstream)
	if err != nil {
		return nil, nil, err
	}
	if int32(binary.LittleEndian.Uint32(response[10:14])) != 0 {
		return nil, nil, errors.New("DM8 native negotiation was refused")
	}
	mode := binary.LittleEndian.Uint32(response[20:24])
	if t.remoteTLS == nil {
		if mode != 0 {
			return nil, nil, errors.New("DM8 server TLS mode conflicts with explicit disabled TLS")
		}
		_, err = io.Copy(downstream, bytes.NewReader(response))
		return downstream, t.upstream, err
	}
	if mode != 1 {
		return nil, nil, fmt.Errorf("DM8 server does not offer supported certificate-authenticated encrypted TLS (mode %d, kind %d)", mode, binary.LittleEndian.Uint16(response[4:6]))
	}
	if t.remoteTLS.GetClientCertificate == nil {
		return nil, nil, errors.New("DM8 mutual TLS requires an explicit client certificate and key")
	}
	config := t.remoteTLS.Clone()
	identityRequested := false
	identity := config.GetClientCertificate
	config.GetClientCertificate = func(request *tls.CertificateRequestInfo) (*tls.Certificate, error) {
		certificate, err := identity(request)
		identityRequested = err == nil && certificate != nil && len(certificate.Certificate) != 0
		return certificate, err
	}
	remote := tls.Client(t.upstream, config)
	if err = remote.HandshakeContext(t.startup); err != nil {
		return nil, nil, errors.New("DM8 remote TLS certificate or peer identity verification failed")
	}
	if !identityRequested {
		return nil, nil, errors.New("DM8 server did not request the explicit mutual TLS client identity")
	}
	// The DPI login cannot send credentials before remote TLS was authenticated.
	if _, err = io.Copy(downstream, bytes.NewReader(response)); err != nil {
		return nil, nil, err
	}
	local := tls.Server(downstream, t.localTLS)
	if err = local.HandshakeContext(t.startup); err != nil {
		return nil, nil, fmt.Errorf("DM8 private native TLS handshake failed: %w", err)
	}
	return local, remote, nil
}
