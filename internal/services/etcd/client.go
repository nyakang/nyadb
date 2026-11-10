package etcd

import (
	"context"
	"crypto/x509"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var (
	ErrInvalid  = errors.New("invalid or unsupported etcd v3 request/configuration")
	ErrBounds   = errors.New("etcd request or response exceeds bounds")
	ErrProtocol = errors.New("invalid or changed etcd native identity/response")
	ErrRetired  = errors.New("etcd connection retired; recover explicitly without replay")
	ErrUsed     = errors.New("etcd preparation already used or foreign")
	ErrBusy     = errors.New("etcd connection has an active request")
)

type Header struct {
	ClusterID, MemberID uint64
	Revision            int64
	RaftTerm            uint64
}
type Identity struct {
	Header
	Version string
}
type Response struct {
	Attempted, Completed, WriteOutcomeUnknown bool
	Code                                      codes.Code
	Header                                    Header
	native                                    proto.Message
}

func (r Response) Native() proto.Message {
	if r.native == nil {
		return nil
	}
	return proto.Clone(r.native)
}
func (r Response) clone() Response { r.native = r.Native(); return r }

type Interrupted struct {
	Cause               error
	WriteOutcomeUnknown bool
}

func (e *Interrupted) Error() string {
	return "etcd request interrupted; recover explicitly and verify writes without replay"
}
func (e *Interrupted) Unwrap() error        { return e.Cause }
func (e *Interrupted) Is(target error) bool { return target == ErrRetired }

type Client struct {
	operation       sync.Mutex
	mu              sync.Mutex
	config          Config
	conn            *grpc.ClientConn
	ctx             context.Context
	cancel          context.CancelFunc
	identity        Identity
	token           string
	closed, retired bool
	connClosed      bool // protected by operation after Open
	last            Response
	principal       string
	access          *keyAccess
	closeOnce       sync.Once
	closeErr        error
}

func Open(parent context.Context, cfg Config, secret string) (*Client, error) {
	cfg, tc, err := normalizeConfig(cfg, secret)
	if err != nil {
		return nil, err
	}
	if err = parent.Err(); err != nil {
		return nil, err
	}
	u, _ := url.Parse(cfg.Endpoint)
	target := u.Host
	if u.Port() == "" {
		target = u.Hostname() + ":2379"
		if strings.Contains(u.Hostname(), ":") {
			target = "[" + u.Hostname() + "]:2379"
		}
	}
	var creds credentials.TransportCredentials = insecure.NewCredentials()
	if tc != nil {
		creds = credentials.NewTLS(tc)
	}
	// Raw service stubs bypass clientv3's application/auth retry interceptors.
	// gRPC may transparently retry only unprocessed calls; processed writes are
	// never retried. No resolver service config, proxy, hedging or endpoint loop.
	conn, err := grpc.NewClient("passthrough:///"+target, grpc.WithTransportCredentials(creds), grpc.WithDisableRetry(), grpc.WithDisableServiceConfig(), grpc.WithNoProxy(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(responseLimit), grpc.MaxCallSendMsgSize(requestLimit), grpc.WaitForReady(false)))
	if err != nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, cfg.ConnectTimeout)
	defer cancel()
	root, cancelRoot := context.WithCancel(context.Background())
	c := &Client{config: cfg, conn: conn, ctx: root, cancel: cancelRoot}
	c.principal = cfg.Username
	if cfg.Auth == "none" && tc != nil && len(tc.Certificates) > 0 {
		leaf, e := x509.ParseCertificate(tc.Certificates[0].Certificate[0])
		if e != nil {
			_ = c.Close()
			return nil, ErrInvalid
		}
		c.principal = leaf.Subject.CommonName
	}
	fail := func(e error) (*Client, error) { _ = c.Close(); return nil, e }
	if cfg.Auth == "basic" {
		r, err := pb.NewAuthClient(conn).Authenticate(ctx, &pb.AuthenticateRequest{Name: cfg.Username, Password: secret})
		if err != nil {
			return fail(err)
		}
		if r == nil || r.Token == "" || len(r.Token) > 1<<20 {
			return fail(ErrProtocol)
		}
		c.token = r.Token
	}
	observed, err := pb.NewMaintenanceClient(conn).Status(c.authContext(ctx), &pb.StatusRequest{})
	if err != nil {
		return fail(err)
	}
	if observed == nil || !validHeader(observed.Header) || !validVersion(observed.Version) {
		return fail(ErrProtocol)
	}
	c.identity = Identity{header(observed.Header), observed.Version}
	if cfg.DiscoverAccess {
		access, e := c.permissions(ctx)
		if e != nil {
			return fail(e)
		}
		ranges := access.scoped(cfg.BrowsePrefix).Read
		if len(ranges) == 0 {
			return c, nil
		}
		if !allowsKey(ranges, cfg.ProbeKey) {
			cfg.ProbeKey = ranges[0].Start
		}
	}
	probe, err := pb.NewKVClient(conn).Range(c.authContext(ctx), &pb.RangeRequest{Key: cfg.ProbeKey, Limit: 1, KeysOnly: true})
	if err != nil {
		return fail(err)
	}
	if probe == nil || !c.matches(probe.Header) {
		return fail(ErrProtocol)
	}
	return c, nil
}

func validVersion(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 3 || parts[0] != "3" {
		return false
	}
	for _, part := range parts {
		n, err := strconv.ParseUint(part, 10, 16)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
	}
	minor, _ := strconv.Atoi(parts[1])
	return minor >= 5
}
func validHeader(h *pb.ResponseHeader) bool {
	return h != nil && h.ClusterId != 0 && h.MemberId != 0 && h.Revision > 0 && h.RaftTerm > 0
}
func header(h *pb.ResponseHeader) Header {
	if h == nil {
		return Header{}
	}
	return Header{h.ClusterId, h.MemberId, h.Revision, h.RaftTerm}
}
func (c *Client) matches(h *pb.ResponseHeader) bool {
	return validHeader(h) && h.ClusterId == c.identity.ClusterID && h.MemberId == c.identity.MemberID
}
func (c *Client) authContext(ctx context.Context) context.Context {
	// Strip caller metadata, including any foreign authentication token.
	md := metadata.MD{}
	if c.token != "" {
		md.Set("token", c.token)
	}
	return metadata.NewOutgoingContext(ctx, md)
}
func (c *Client) Alive() bool            { c.mu.Lock(); defer c.mu.Unlock(); return !c.closed && !c.retired }
func (c *Client) Identity() Identity     { c.mu.Lock(); defer c.mu.Unlock(); return c.identity }
func (c *Client) LastResponse() Response { c.mu.Lock(); defer c.mu.Unlock(); return c.last.clone() }

func (c *Client) Execute(parent context.Context, p Plan) (r Response, err error) {
	if err = parent.Err(); err != nil {
		return r, err
	}
	if !c.operation.TryLock() {
		return r, ErrBusy
	}
	defer c.operation.Unlock()
	return c.executeOwned(parent, p)
}

// executeOwned requires operation ownership, including across the Range/Watch
// boundary of a history capture. Only its caller releases that ownership.
func (c *Client) executeOwned(parent context.Context, p Plan) (r Response, err error) {
	if !c.Alive() {
		return r, ErrRetired
	}
	if p.owner != c || p.used == nil || !p.used.CompareAndSwap(false, true) {
		return r, ErrUsed
	}
	ctx, cancel := context.WithTimeout(parent, c.config.RequestTimeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer func() {
		if err != nil {
			r.Code = status.Code(err)
			definite := r.Code == codes.InvalidArgument || r.Code == codes.PermissionDenied || r.Code == codes.Unauthenticated || r.Code == codes.NotFound || r.Code == codes.AlreadyExists || r.Code == codes.OutOfRange || r.Code == codes.FailedPrecondition
			r.WriteOutcomeUnknown = p.write && r.Attempted && !r.Completed && !definite
			if ctx.Err() != nil || r.Code == codes.Unavailable || r.Code == codes.Canceled || r.Code == codes.DeadlineExceeded || r.Code == codes.Unauthenticated || errors.Is(err, ErrProtocol) || r.Code == codes.ResourceExhausted {
				c.mu.Lock()
				c.retired = true
				c.mu.Unlock()
				c.closeConnection()
				if r.Attempted {
					err = &Interrupted{err, r.WriteOutcomeUnknown}
				}
			}
		}
		c.mu.Lock()
		c.last = r.clone()
		c.mu.Unlock()
	}()
	if err = ctx.Err(); err != nil {
		return r, err
	}
	r.Attempted = true
	var native proto.Message
	if v, ok := p.request.(*pb.LeaseKeepAliveRequest); ok {
		stream, e := pb.NewLeaseClient(c.conn).LeaseKeepAlive(c.authContext(ctx))
		if e != nil {
			return r, e
		}
		if e = stream.Send(v); e != nil {
			return r, e
		}
		if e = stream.CloseSend(); e != nil {
			return r, e
		}
		native, err = stream.Recv()
	} else {
		native = responseFor(p.request)
		err = c.conn.Invoke(c.authContext(ctx), p.method, p.request, native)
	}
	if err != nil {
		return r, err
	}
	if native == nil || !native.ProtoReflect().IsValid() {
		return r, ErrProtocol
	}
	r.native = proto.Clone(native)
	h := responseHeader(native)
	r.Header = header(h)
	if !c.matches(h) {
		return r, ErrProtocol
	}
	if s, ok := native.(*pb.StatusResponse); ok && s.Version != c.identity.Version {
		return r, ErrProtocol
	}
	if l, ok := native.(*pb.LeaseKeepAliveResponse); ok && l.ID != p.request.(*pb.LeaseKeepAliveRequest).ID {
		return r, ErrProtocol
	}
	if err := validateLeaseReply(p.request, native); err != nil {
		return r, err
	}
	if err := validateAuthReply(p.request, native); err != nil {
		return r, err
	}
	if _, write, err := validateAuthRequest(p.request); err == nil && write {
		c.mu.Lock()
		c.access = nil
		c.mu.Unlock()
	}
	r.Completed = true
	return r, nil
}

func responseFor(request proto.Message) proto.Message {
	switch request.(type) {
	case *pb.RangeRequest:
		return &pb.RangeResponse{}
	case *pb.PutRequest:
		return &pb.PutResponse{}
	case *pb.DeleteRangeRequest:
		return &pb.DeleteRangeResponse{}
	case *pb.TxnRequest:
		return &pb.TxnResponse{}
	case *pb.LeaseGrantRequest:
		return &pb.LeaseGrantResponse{}
	case *pb.LeaseRevokeRequest:
		return &pb.LeaseRevokeResponse{}
	case *pb.LeaseTimeToLiveRequest:
		return &pb.LeaseTimeToLiveResponse{}
	case *pb.LeaseLeasesRequest:
		return &pb.LeaseLeasesResponse{}
	case *pb.StatusRequest:
		return &pb.StatusResponse{}
	}
	return authResponse(request)
}
func responseHeader(r proto.Message) *pb.ResponseHeader {
	if v, ok := r.(interface{ GetHeader() *pb.ResponseHeader }); ok {
		return v.GetHeader()
	}
	return nil
}

// Close cancels first, then waits for the owning executor before releasing its
// channel and retained result. It never selects or closes a replacement client.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.cancel()
		c.operation.Lock()
		defer c.operation.Unlock()
		c.closeConnection()
		c.mu.Lock()
		c.last = Response{}
		c.token = ""
		c.access = nil
		c.principal = ""
		c.mu.Unlock()
	})
	return c.closeErr
}

func (c *Client) closeConnection() {
	if !c.connClosed {
		c.closeErr = c.conn.Close()
		c.connClosed = true
	}
}
