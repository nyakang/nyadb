package etcd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func snapshotNativeClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("NYADB_ETCD_SNAPSHOT_IT") != "1" {
		t.Skip("requires owned original Snapshot fixture")
	}
	return nativeClient(t)
}
func TestEtcdNativeSnapshotCompleteMultiFrameDiskOutputsRetainOriginalNativeEvidence(t *testing.T) {
	c := snapshotNativeClient(t)
	key := nativeKey(t)
	for i := 0; i < 12; i++ {
		nativeDo(t, c, &pb.PutRequest{Key: append(bytes.Clone(key), byte(i)), Value: bytes.Repeat([]byte{byte(i), 0, 255, 37}, 16384)})
	}
	defer nativeDo(t, c, &pb.DeleteRangeRequest{Key: key, RangeEnd: PrefixRange(key, 1, 0).RangeEnd})
	folder := t.TempDir()
	body, err := os.CreateTemp(folder, "body-")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	journal, err := os.CreateTemp(folder, "journal-")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	p, err := c.PrepareSnapshot(&pb.SnapshotRequest{}, DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.ExecuteSnapshot(context.Background(), p, SnapshotOutputs{body, journal})
	if err != nil || !r.Completed || !r.EOF || !r.SourceComplete || !r.ChecksumMatched || !r.JournalComplete || r.Frames < 4 || r.BodyBytes < 12*65536 || r.BodyBytes%4096 != 0 {
		t.Fatal("complete multi-frame original snapshot", r, err)
	}
	if c.identity.Version == "3.5.35" && r.StorageVersion != "" || c.identity.Version == "3.7.3" && r.StorageVersion != "3.7.0" {
		t.Fatal("storage/product version conflated")
	}
	if _, err = body.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	proof := snapshotTestProof(t, r.BodyBytes)
	proof.identity = c.identity
	proof.maxFrames = r.Frames
	var count uint64
	var origin SnapshotJournalOrigin
	var terminal SnapshotJournalTerminal
	err = ReadSnapshotJournal(journal, r.JournalBytesWritten, func(record SnapshotJournalRecord) error {
		switch record.Kind {
		case SnapshotJournalOriginKind:
			if err := json.Unmarshal(record.Raw, &origin); err != nil {
				return err
			}
		case SnapshotJournalFrameKind:
			f := &pb.SnapshotResponse{}
			if err := proto.Unmarshal(record.Raw, f); err != nil {
				return err
			}
			if f.Header != nil {
				t.Fatal("fixture nil native header changed")
			}
			original := make([]byte, len(f.Blob))
			if _, err := io.ReadFull(body, original); err != nil {
				return err
			}
			if !bytes.Equal(original, f.Blob) {
				t.Fatal("native disk file differs from full framing evidence")
			}
			count++
			_, err := proof.accept(f)
			return err
		case SnapshotJournalTerminalKind:
			return json.Unmarshal(record.Raw, &terminal)
		}
		return nil
	})
	if err != nil || count != r.Frames || proof.finish(io.EOF, context.Background()) != nil || origin.Identity != c.identity || !origin.StatusVerified || !terminal.SourceComplete || terminal.SnapshotBytesWritten != r.SnapshotBytesWritten {
		t.Fatal("disk-backed native proof lost", err)
	}
	var extra [1]byte
	if n, e := body.Read(extra[:]); n != 0 || e != io.EOF {
		t.Fatal("native file trailing bytes")
	}
	journal.Seek(0, 0)
	digest := sha256.New()
	n, e := io.Copy(digest, journal)
	if e != nil || uint64(n) != r.JournalBytesWritten || !bytes.Equal(digest.Sum(nil), r.JournalSHA256) {
		t.Fatal("original journal acknowledgement mismatch")
	}
	before := r.Before().Native().(*pb.StatusResponse)
	if before.Header == nil || before.Header.ClusterId != c.identity.ClusterID || before.Header.MemberId != c.identity.MemberID {
		t.Fatal("pre-status identity missing")
	}
	if _, err := c.ExecuteSnapshot(context.Background(), p, SnapshotOutputs{body, journal}); !errors.Is(err, ErrUsed) {
		t.Fatal("original Snapshot replayed")
	}
}
func TestEtcdNativeSnapshotOriginalRestrictedTokenAndCertificatePermissions(t *testing.T) {
	snapshotNativeClient(t)
	cfg, secret := nativeConfig(t)
	if cfg.Auth == "basic" {
		cfg.Username = "restricted"
		cfg.ProbeKey = []byte("/allowed/key")
		if cfg.ClientCertFile != "" {
			cfg.ClientCertFile = os.Getenv("NYADB_ETCD_ACCESS_CERT")
			cfg.ClientKeyFile = os.Getenv("NYADB_ETCD_ACCESS_KEY")
			if cfg.ClientCertFile == "" {
				t.Fatal("missing original restricted cert")
			}
		}
		c, err := Open(context.Background(), cfg, secret)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		p, _ := c.PrepareSnapshot(&pb.SnapshotRequest{}, DefaultSnapshotLimits())
		var body, journal bytes.Buffer
		r, err := c.ExecuteSnapshot(context.Background(), p, SnapshotOutputs{&body, &journal})
		expectedCode := codes.PermissionDenied
		if c.identity.Version == "3.5.35" {
			expectedCode = codes.Unknown
			nativeStatus := &statuspb.Status{}
			if proto.Unmarshal(r.NativeStatus, nativeStatus) != nil || nativeStatus.Message != "auth: permission denied" || nativeStatus.Code != int32(codes.Unknown) {
				t.Fatal("legacy denial changed original message", err)
			}
		}
		if status.Code(err) != expectedCode || r.NativeCode != expectedCode || r.Completed || r.SourceComplete || !r.Attempted || !r.NativeTerminalObserved || r.EOF || !r.JournalComplete || body.Len() != 0 || len(r.NativeStatus) == 0 || !c.Alive() {
			t.Fatal("actual original denial changed", r, err)
		}
		snapshotJournalRecords(t, journal.Bytes())
	}
	cfg, _ = nativeConfig(t)
	if cfg.ClientCertFile != "" {
		cfg.Auth, cfg.Username = "none", ""
		c, err := Open(context.Background(), cfg, "")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		p, _ := c.PrepareSnapshot(&pb.SnapshotRequest{}, DefaultSnapshotLimits())
		var body, journal bytes.Buffer
		r, err := c.ExecuteSnapshot(context.Background(), p, SnapshotOutputs{&body, &journal})
		if err != nil || !r.Completed {
			t.Fatal("original certificate snapshot denied", err)
		}
	}
}

type snapshotNativeFault struct {
	upstream      *Client
	mode          string
	calls         atomic.Int32
	entered, done chan struct{}
	frames        []*pb.SnapshotResponse
	eof           bool
}

func (f *snapshotNativeFault) serve(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	if method != "/etcdserverpb.Maintenance/Snapshot" {
		return (&nativeFault{upstream: f.upstream}).serve(nil, stream)
	}
	request := &pb.SnapshotRequest{}
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	if f.calls.Add(1) != 1 {
		return status.Error(codes.Internal, "original Snapshot replayed")
	}
	announced := false
	defer func() {
		if !announced {
			close(f.entered)
		}
		close(f.done)
	}()
	upstream, err := pb.NewMaintenanceClient(f.upstream.conn).Snapshot(f.upstream.authContext(stream.Context()), request)
	if err != nil {
		return err
	}
	// Fault fixtures retain the actual complete upstream stream before changing
	// its downstream delivery. Production never accumulates snapshot blobs.
	for {
		v, e := upstream.Recv()
		if e == io.EOF {
			f.eof = true
			break
		}
		if e != nil {
			return e
		}
		f.frames = append(f.frames, proto.Clone(v).(*pb.SnapshotResponse))
	}
	close(f.entered)
	announced = true
	for i, original := range f.frames {
		v := proto.Clone(original).(*pb.SnapshotResponse)
		if f.mode == "missing-hash" && i == len(f.frames)-1 {
			return nil
		}
		if f.mode == "bad-hash" && i == len(f.frames)-1 {
			v.Blob[0] ^= 255
		}
		if f.mode == "changed-count" && i == 0 {
			v.RemainingBytes++
		}
		if f.mode == "foreign-header" && i == 0 {
			v.Header = &pb.ResponseHeader{ClusterId: f.upstream.identity.ClusterID + 1}
		}
		if f.mode == "extensions" {
			v.ProtoReflect().SetUnknown([]byte{0xa0, 6, 7})
		}
		if err := stream.SendMsg(v); err != nil {
			return err
		}
		if f.mode == "mid-loss" && i == 0 {
			return status.Error(codes.Unavailable, "actual original partial stream withheld")
		}
	}
	switch f.mode {
	case "trailing":
		return stream.SendMsg(&pb.SnapshotResponse{Blob: []byte("after checksum")})
	case "lost-eof":
		return status.Error(codes.Unavailable, "actual original EOF withheld")
	case "cancel", "close":
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	return nil
}
func snapshotNativeProxy(t *testing.T, admin *Client, mode string) (*Client, *snapshotNativeFault) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &snapshotNativeFault{upstream: admin, mode: mode, entered: make(chan struct{}), done: make(chan struct{})}
	cfg, secret := nativeConfig(t)
	var options []grpc.ServerOption
	if cfg.CAFile != "" {
		certificate, err := tls.LoadX509KeyPair(strings.TrimSuffix(cfg.CAFile, "ca.crt")+"server.crt", strings.TrimSuffix(cfg.CAFile, "ca.crt")+"server.key")
		if err != nil {
			t.Fatal(err)
		}
		tc := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
		if cfg.ClientCertFile != "" {
			raw, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				t.Fatal(err)
			}
			tc.ClientCAs = x509.NewCertPool()
			if !tc.ClientCAs.AppendCertsFromPEM(raw) {
				t.Fatal("invalid owned CA")
			}
			tc.ClientAuth = tls.RequireAndVerifyClientCert
		}
		options = append(options, grpc.Creds(credentials.NewTLS(tc)))
		cfg.Endpoint = "https://localhost:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	} else {
		cfg.Endpoint = "http://" + listener.Addr().String()
	}
	options = append(options, grpc.UnknownServiceHandler(f.serve))
	server := grpc.NewServer(options...)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	c, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal("original Snapshot relay handshake", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, f
}
func TestEtcdNativeSnapshotActualOriginalStreamFaultsCancellationAndCloseNeverResume(t *testing.T) {
	admin := snapshotNativeClient(t)
	for _, mode := range []string{"mid-loss", "missing-hash", "bad-hash", "changed-count", "foreign-header", "trailing", "lost-eof", "extensions", "cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			c, f := snapshotNativeProxy(t, admin, mode)
			p, _ := c.PrepareSnapshot(&pb.SnapshotRequest{}, DefaultSnapshotLimits())
			var body, journal bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				r   SnapshotResult
				err error
			}
			done := make(chan completion, 1)
			go func() { r, e := c.ExecuteSnapshot(ctx, p, SnapshotOutputs{&body, &journal}); done <- completion{r, e} }()
			select {
			case <-f.entered:
			case early := <-done:
				t.Fatal("snapshot ended before actual source fault", early.err)
			}
			if mode == "cancel" {
				cancel()
			}
			if mode == "close" {
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			}
			result := <-done
			<-f.done
			r, err := result.r, result.err
			if !f.eof || len(f.frames) < 2 || f.calls.Load() != 1 || !admin.Alive() {
				t.Fatal("fault did not follow actual original source or touched another client")
			}
			if mode == "extensions" {
				if err != nil || !r.Completed {
					t.Fatal("native unknown extension rejected", err)
				}
				records := snapshotJournalRecords(t, journal.Bytes())
				for _, record := range records {
					if record.Kind == SnapshotJournalFrameKind {
						frame := &pb.SnapshotResponse{}
						proto.Unmarshal(record.Raw, frame)
						if !bytes.Equal(frame.ProtoReflect().GetUnknown(), []byte{0xa0, 6, 7}) {
							t.Fatal("native extension lost")
						}
					}
				}
			} else {
				if err == nil || r.Completed || !r.Ended || c.Alive() || !r.JournalComplete {
					t.Fatal("failed actual original stream became complete", r, err)
				}
				snapshotJournalRecords(t, journal.Bytes())
				if mode == "lost-eof" {
					if !r.ChecksumMatched || r.EOF || r.SourceComplete || !r.NativeTerminalObserved || len(r.NativeStatus) == 0 {
						t.Fatal("checksum became EOF proof")
					}
				}
				if _, e := c.ExecuteSnapshot(context.Background(), p, SnapshotOutputs{&body, &journal}); !errors.Is(e, ErrRetired) {
					t.Fatal("failed original resumed")
				}
			}
		})
	}
}
