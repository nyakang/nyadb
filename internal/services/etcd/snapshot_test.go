package etcd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type snapshotTestServer struct {
	frames      []*pb.SnapshotResponse
	nativeError error
	status      *pb.StatusResponse
	calls       atomic.Int32
}

func (s *snapshotTestServer) serve(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	if method == "/etcdserverpb.Maintenance/Status" {
		if err := stream.RecvMsg(&pb.StatusRequest{}); err != nil {
			return err
		}
		return stream.SendMsg(s.status)
	}
	if method != "/etcdserverpb.Maintenance/Snapshot" {
		return status.Error(codes.Unimplemented, "unexpected RPC")
	}
	if err := stream.RecvMsg(&pb.SnapshotRequest{}); err != nil {
		return err
	}
	s.calls.Add(1)
	for _, f := range s.frames {
		if err := stream.SendMsg(f); err != nil {
			return err
		}
	}
	return s.nativeError
}
func snapshotUnitClient(t *testing.T, s *snapshotTestServer) *Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnknownServiceHandler(s.serve))
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	conn, err := grpc.NewClient("passthrough:///"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDisableRetry(), grpc.WithDisableServiceConfig())
	if err != nil {
		t.Fatal(err)
	}
	c := sessionIdentityClient(2)
	c.conn, c.connClosed = conn, false
	c.config.RequestTimeout = time.Second
	s.status = &pb.StatusResponse{Header: &pb.ResponseHeader{ClusterId: 2, MemberId: 3, Revision: 1, RaftTerm: 1}, Version: "3.7.3"}
	t.Cleanup(func() { c.Close() })
	return c
}
func snapshotUnitFrames() []*pb.SnapshotResponse {
	body := []byte("original\x00\xffsnapshot-body")
	sum := sha256.Sum256(body)
	frames := []*pb.SnapshotResponse{{Blob: body[:5], RemainingBytes: uint64(len(body) - 5), Version: "3.7.0"}, {Blob: body[5:]}, {Blob: sum[:]}}
	for i, f := range frames {
		f.ProtoReflect().SetUnknown([]byte{0xa0, 6, byte(i + 1)})
	}
	return frames
}
func snapshotJournalRecords(t *testing.T, b []byte) []SnapshotJournalRecord {
	t.Helper()
	var records []SnapshotJournalRecord
	if err := ReadSnapshotJournal(bytes.NewReader(b), uint64(len(b)), func(r SnapshotJournalRecord) error { records = append(records, r); return nil }); err != nil {
		t.Fatal("incomplete original journal", err)
	}
	return records
}
func TestSnapshotExecutorCapturesCompleteOriginalFramesAndSeparatesNativeProof(t *testing.T) {
	s := &snapshotTestServer{frames: snapshotUnitFrames()}
	c := snapshotUnitClient(t, s)
	p, err := c.PrepareSnapshot(&pb.SnapshotRequest{}, DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	p.Native().ProtoReflect().SetUnknown([]byte{0xa0, 6, 1})
	var body, journal bytes.Buffer
	r, err := c.ExecuteSnapshot(context.Background(), p, SnapshotOutputs{&body, &journal})
	if err != nil || !r.Completed || !r.SourceComplete || !r.JournalComplete || !r.NativeTerminalObserved || !r.EOF || !r.OutputAcknowledged || r.StorageVersion != "3.7.0" {
		t.Fatal("original completion", err, r)
	}
	var expected []byte
	for _, f := range s.frames {
		expected = append(expected, f.Blob...)
	}
	if !bytes.Equal(body.Bytes(), expected) || r.SnapshotBytesWritten != uint64(len(expected)) {
		t.Fatal("snapshot concatenation changed")
	}
	records := snapshotJournalRecords(t, journal.Bytes())
	if len(records) != 5 {
		t.Fatal("lost native records")
	}
	var origin SnapshotJournalOrigin
	json.Unmarshal(records[0].Raw, &origin)
	if !origin.StatusVerified || origin.Identity != r.Identity {
		t.Fatal("original pre-status lost")
	}
	for i, f := range s.frames {
		actual := &pb.SnapshotResponse{}
		proto.Unmarshal(records[i+1].Raw, actual)
		if !proto.Equal(actual, f) || actual.Header != nil {
			t.Fatal("blob/unknown/nil header changed")
		}
	}
	var terminal SnapshotJournalTerminal
	json.Unmarshal(records[4].Raw, &terminal)
	if !terminal.SourceComplete || !terminal.EOF || terminal.SnapshotBytesWritten != r.SnapshotBytesWritten {
		t.Fatal("terminal proof lost")
	}
	digest := sha256.Sum256(journal.Bytes())
	if !bytes.Equal(digest[:], r.JournalSHA256) {
		t.Fatal("acknowledged journal hash changed")
	}
	r.Checksum[0] ^= 255
	r.Before().Native().(*pb.StatusResponse).Version = "changed"
	if bytes.Equal(r.Checksum, c.LastSnapshot().Checksum) || c.LastSnapshot().Before().Native().(*pb.StatusResponse).Version != "3.7.3" {
		t.Fatal("original result aliases caller")
	}
	if _, err := c.ExecuteSnapshot(context.Background(), p, SnapshotOutputs{&body, &journal}); !errors.Is(err, ErrUsed) || s.calls.Load() != 1 {
		t.Fatal("original stream replayed")
	}
	c.Close()
	if c.LastSnapshot().Ended {
		t.Fatal("close retains duplicate cache")
	}
}
func TestSnapshotExecutorArchivesRejectedFramesAndNativeStatusWithoutInventingEOF(t *testing.T) {
	for _, mode := range []string{"early-eof", "missing-hash", "bad-hash", "changed-count", "foreign-header", "trailing", "native-denial", "lost-terminal"} {
		t.Run(mode, func(t *testing.T) {
			s := &snapshotTestServer{frames: snapshotUnitFrames()}
			switch mode {
			case "early-eof":
				s.frames = nil
			case "missing-hash":
				s.frames = s.frames[:2]
			case "bad-hash":
				s.frames[2].Blob[0] ^= 255
			case "changed-count":
				s.frames[0].RemainingBytes++
			case "foreign-header":
				s.frames[1].Header = &pb.ResponseHeader{ClusterId: 99}
			case "trailing":
				s.frames = append(s.frames, &pb.SnapshotResponse{Blob: []byte("extra")})
			case "native-denial":
				s.frames = nil
				s.nativeError = status.Error(codes.PermissionDenied, "original denied")
			case "lost-terminal":
				s.nativeError = status.Error(codes.Unavailable, "actual original terminal withheld")
			}
			c := snapshotUnitClient(t, s)
			p, _ := c.PrepareSnapshot(&pb.SnapshotRequest{}, DefaultSnapshotLimits())
			var body, journal bytes.Buffer
			r, err := c.ExecuteSnapshot(context.Background(), p, SnapshotOutputs{&body, &journal})
			if err == nil || r.Completed || r.SourceComplete || !r.Ended || !r.JournalComplete {
				t.Fatal("failed original declared complete", r, err)
			}
			records := snapshotJournalRecords(t, journal.Bytes())
			if len(records) < 2 {
				t.Fatal("failure evidence lost")
			}
			if mode == "native-denial" {
				if !c.Alive() || r.NativeCode != codes.PermissionDenied || len(r.NativeStatus) == 0 {
					t.Fatal("definite denial changed")
				}
			} else if c.Alive() {
				t.Fatal("failed original transport not retired")
			}
			if mode == "bad-hash" || mode == "foreign-header" || mode == "trailing" || mode == "changed-count" {
				if len(r.RejectedNative) == 0 || r.NativeTerminalObserved || r.EOF {
					t.Fatal("local rejection became original native terminal")
				}
			}
		})
	}
}

type snapshotFailWriter struct {
	limit  int
	buffer bytes.Buffer
}

func (w *snapshotFailWriter) Write(b []byte) (int, error) {
	if len(b) > w.limit {
		n, _ := w.buffer.Write(b[:w.limit])
		w.limit = 0
		return n, io.ErrShortWrite
	}
	w.limit -= len(b)
	return w.buffer.Write(b)
}
func TestSnapshotExecutorKeepsPartialAcknowledgedOutputsAndBudgetFailuresSeparate(t *testing.T) {
	for _, target := range []string{"body", "journal", "body-budget", "journal-budget"} {
		t.Run(target, func(t *testing.T) {
			c := snapshotUnitClient(t, &snapshotTestServer{frames: snapshotUnitFrames()})
			limits := DefaultSnapshotLimits()
			var body, journal bytes.Buffer
			outputs := SnapshotOutputs{&body, &journal}
			failed := &snapshotFailWriter{limit: 3}
			switch target {
			case "body":
				outputs.Snapshot = failed
			case "journal":
				outputs.Journal = failed
			case "body-budget":
				limits.BodyBytes = 3
			case "journal-budget":
				limits.JournalBytes = uint64(len(snapshotJournalMagic)) + 1
			}
			p, _ := c.PrepareSnapshot(&pb.SnapshotRequest{}, limits)
			r, err := c.ExecuteSnapshot(context.Background(), p, outputs)
			if err == nil || r.Completed || r.SourceComplete || r.CaptureError == nil && r.LocalError == nil {
				t.Fatal("partial output falsely completed", r, err)
			}
			if target == "body" {
				if r.SnapshotBytesWritten != 3 || r.OutputAcknowledged || len(r.RejectedNative) == 0 || !r.JournalComplete {
					t.Fatal("partial body acknowledgement lost", r)
				}
			}
			if target == "journal" || target == "journal-budget" {
				if r.JournalComplete {
					t.Fatal("partial archive repaired with a footer")
				}
			}
		})
	}
}
func TestSnapshotJournalRejectsTruncationOversizeDuplicatesAndTrailingBytes(t *testing.T) {
	c := snapshotUnitClient(t, &snapshotTestServer{frames: snapshotUnitFrames()})
	p, _ := c.PrepareSnapshot(&pb.SnapshotRequest{}, DefaultSnapshotLimits())
	var body, journal bytes.Buffer
	if _, err := c.ExecuteSnapshot(context.Background(), p, SnapshotOutputs{&body, &journal}); err != nil {
		t.Fatal(err)
	}
	original := journal.Bytes()
	for i := 0; i < len(original); i++ {
		if err := ReadSnapshotJournal(bytes.NewReader(original[:i]), uint64(len(original)), func(SnapshotJournalRecord) error { return nil }); err == nil {
			t.Fatal("truncated journal accepted", i)
		}
	}
	for _, b := range [][]byte{append(bytes.Clone(original), 1), []byte("wrong magic"), append([]byte(snapshotJournalMagic), []byte{2, 255, 255, 255, 255, 255, 255, 255, 255}...)} {
		if err := ReadSnapshotJournal(bytes.NewReader(b), uint64(len(b)), func(SnapshotJournalRecord) error { return nil }); err == nil {
			t.Fatal("malformed journal accepted")
		}
	}
}

type snapshotBlockingWriter struct {
	entered, release chan struct{}
	once             atomic.Bool
	buffer           bytes.Buffer
}

func (w *snapshotBlockingWriter) Write(b []byte) (int, error) {
	if w.once.CompareAndSwap(false, true) {
		close(w.entered)
		<-w.release
	}
	return w.buffer.Write(b)
}
func TestSnapshotCloseWaitsOriginalWriterAcknowledgementAndPreservesOtherClient(t *testing.T) {
	c := snapshotUnitClient(t, &snapshotTestServer{frames: snapshotUnitFrames()})
	other := snapshotUnitClient(t, &snapshotTestServer{frames: snapshotUnitFrames()})
	p, _ := c.PrepareSnapshot(&pb.SnapshotRequest{}, DefaultSnapshotLimits())
	w := &snapshotBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var journal bytes.Buffer
	done := make(chan SnapshotResult, 1)
	go func() { r, _ := c.ExecuteSnapshot(context.Background(), p, SnapshotOutputs{w, &journal}); done <- r }()
	<-w.entered
	if _, err := c.ExecuteSnapshot(context.Background(), p, SnapshotOutputs{w, &journal}); !errors.Is(err, ErrBusy) {
		t.Fatal("writer released original operation")
	}
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("close returned before original writer ACK")
	case <-time.After(20 * time.Millisecond):
	}
	close(w.release)
	r := <-done
	<-closed
	if !r.Canceled || r.Completed || !r.Ended || !other.Alive() {
		t.Fatal("close lost original ownership", r)
	}
	if err := ReadSnapshotJournal(bytes.NewReader(journal.Bytes()), uint64(journal.Len()), func(SnapshotJournalRecord) error { return nil }); err != nil {
		t.Fatal("original local-stop evidence missing", err)
	}
}

func TestSnapshotLegacyDenialMatchesOnlyOriginalVersionCodeAndExactMessage(t *testing.T) {
	for _, version := range []string{"3.5.35", "3.7.3"} {
		c := &Client{identity: Identity{Version: version}}
		for _, native := range []error{
			status.Error(codes.Unknown, "auth: permission denied"),
			status.Error(codes.Unknown, "different unknown failure"),
			status.Error(codes.Unavailable, "auth: permission denied"),
		} {
			expected := version == "3.5.35" && status.Code(native) == codes.Unknown && status.Convert(native).Message() == "auth: permission denied"
			if c.definiteSnapshotRejection(native) != expected {
				t.Fatal("legacy Snapshot denial was generalized", version, native)
			}
		}
	}
}
