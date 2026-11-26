// Package netlimit bounds listeners and in-flight request bytes.
//
// A zero limit in Limits means the default. Resolve rejects a negative
// value and a body cap larger than the in-flight budget, so a caller
// cannot turn a limit off by setting it to zero and cannot accept a
// single request that the process is not allowed to buffer.
package netlimit

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/paulpham157/sdf/internal/otlp"
)

const (
	// DefaultMaxConns is the concurrent-connection cap on one listener.
	// A local user runs a handful of agents, each with a few connections.
	// 128 is well above that and well below a file-descriptor pileup.
	DefaultMaxConns = 128
	// DefaultMaxConnsPerIP is the cap from one remote address. The default
	// bind is loopback, so this is the practical cap for a normal install.
	DefaultMaxConnsPerIP = 64
	// DefaultMaxConnsPerToken is the cap on concurrent authenticated
	// requests that present the receiver token. Idle connections do not
	// hold a slot.
	DefaultMaxConnsPerToken = 64
	// DefaultMaxBodyBytes is the per-request body cap, including after
	// gzip. It matches the cap the receiver already enforced.
	DefaultMaxBodyBytes = otlp.MaxBodyBytes
	// DefaultMaxInFlightBytes is the total body bytes the process will
	// buffer at once. It fits eight default-sized bodies (64 MiB) and is
	// checked to be at least one body.
	DefaultMaxInFlightBytes int64 = 64 << 20
	// DefaultReadHeaderTimeout is how long a new request has to send its
	// headers once the first byte arrives. The first request on a
	// connection uses this from accept, so a header trickle cannot sit
	// for the idle timeout.
	DefaultReadHeaderTimeout = 5 * time.Second
	// DefaultReadTimeout is how long the body has to arrive after the
	// headers.
	DefaultReadTimeout = 30 * time.Second
	// DefaultIdleTimeout is how long a keep-alive connection may wait
	// for the next request.
	DefaultIdleTimeout = 90 * time.Second
	// RetryAfter is the delay told to a client that hit a cap. One second
	// is long enough to count as backpressure and short enough that a
	// local exporter is not stalled for a round of exports.
	RetryAfter = time.Second

	// MsgTooManyConns is the HTTP body when the listener is at its cap.
	MsgTooManyConns = "too many connections"
	// MsgTooManyIP is the HTTP body when one address is at its cap.
	MsgTooManyIP = "too many connections from this address"
)

// Limits is the resolved or unresolved resource configuration.
// Zero values are filled by Resolve.
type Limits struct {
	MaxConns          int
	MaxConnsPerIP     int
	MaxConnsPerToken  int
	MaxBodyBytes      int
	MaxInFlightBytes  int64
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	IdleTimeout       time.Duration
}

// Resolve fills defaults and rejects a configuration that would disable a
// limit or accept a body the in-flight budget cannot hold. A per-address
// or per-token cap above the connection cap is lowered to that cap.
func Resolve(in Limits) (Limits, error) {
	out := in
	if out.MaxConns == 0 {
		out.MaxConns = DefaultMaxConns
	}
	if out.MaxConnsPerIP == 0 {
		out.MaxConnsPerIP = DefaultMaxConnsPerIP
	}
	if out.MaxConnsPerToken == 0 {
		out.MaxConnsPerToken = DefaultMaxConnsPerToken
	}
	if out.MaxBodyBytes == 0 {
		out.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if out.MaxInFlightBytes == 0 {
		out.MaxInFlightBytes = DefaultMaxInFlightBytes
	}
	if out.ReadHeaderTimeout == 0 {
		out.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if out.ReadTimeout == 0 {
		out.ReadTimeout = DefaultReadTimeout
	}
	if out.IdleTimeout == 0 {
		out.IdleTimeout = DefaultIdleTimeout
	}
	if out.MaxConns < 1 {
		return Limits{}, fmt.Errorf("max connections must be positive")
	}
	if out.MaxConnsPerIP < 1 {
		return Limits{}, fmt.Errorf("max connections per address must be positive")
	}
	if out.MaxConnsPerToken < 1 {
		return Limits{}, fmt.Errorf("max connections per token must be positive")
	}
	if out.MaxBodyBytes < 1 {
		return Limits{}, fmt.Errorf("max body bytes must be positive")
	}
	if out.MaxInFlightBytes < 1 {
		return Limits{}, fmt.Errorf("max in-flight bytes must be positive")
	}
	if out.ReadHeaderTimeout <= 0 || out.ReadTimeout <= 0 || out.IdleTimeout <= 0 {
		return Limits{}, fmt.Errorf("timeouts must be positive")
	}
	if out.MaxConnsPerIP > out.MaxConns {
		out.MaxConnsPerIP = out.MaxConns
	}
	if out.MaxConnsPerToken > out.MaxConns {
		out.MaxConnsPerToken = out.MaxConns
	}
	if out.MaxInFlightBytes < int64(out.MaxBodyBytes) {
		return Limits{}, fmt.Errorf("max in-flight bytes (%d) must be at least the per-request body cap (%d)", out.MaxInFlightBytes, out.MaxBodyBytes)
	}
	return out, nil
}

// Reject is why a connection was not admitted.
type Reject int

const (
	// Admit means the connection was counted.
	Admit Reject = iota
	// RejectTotal means the listener is at its cap. That is HTTP 503.
	RejectTotal
	// RejectIP means this address is at its cap. That is HTTP 429.
	RejectIP
)

// Conns counts admitted connections, in total and per remote IP.
type Conns struct {
	mu     sync.Mutex
	max    int
	perMax int
	total  int
	per    map[string]int
}

// Try counts ip. On Admit the caller must Release exactly once.
// A non-positive cap admits nothing.
func (c *Conns) Try(ip string) Reject {
	if c == nil {
		return RejectTotal
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.max < 1 || c.total >= c.max {
		return RejectTotal
	}
	if c.perMax < 1 || c.per[ip] >= c.perMax {
		return RejectIP
	}
	c.total++
	if c.per == nil {
		c.per = make(map[string]int)
	}
	c.per[ip]++
	return Admit
}

// Release undoes one successful Try. Extra releases do not drive the
// count negative.
func (c *Conns) Release(ip string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total > 0 {
		c.total--
	}
	n := c.per[ip]
	if n <= 1 {
		delete(c.per, ip)
		return
	}
	c.per[ip] = n - 1
}

// Gate is a single counter, used for the shared bearer token.
type Gate struct {
	mu   sync.Mutex
	max  int
	used int
}

// NewGate returns a gate that admits at most max at once.
// A non-positive max admits nothing.
func NewGate(max int) *Gate {
	return &Gate{max: max}
}

// Acquire takes one slot. The returned function releases it and is safe
// to call more than once.
func (g *Gate) Acquire() (func(), bool) {
	if g == nil || g.max <= 0 {
		return nil, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.used >= g.max {
		return nil, false
	}
	g.used++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			if g.used > 0 {
				g.used--
			}
			g.mu.Unlock()
		})
	}, true
}

// Budget is a byte ceiling shared by every in-flight body.
// grpcMax, when set, is the most of that ceiling gRPC may hold at once,
// so a burst of gRPC streams cannot take the whole budget from HTTP.
type Budget struct {
	mu       sync.Mutex
	max      int64
	used     int64
	grpcMax  int64
	grpcUsed int64
}

// NewBudget returns a budget of max bytes. A negative max is treated as
// zero and then admits nothing.
func NewBudget(max int64) *Budget {
	if max < 0 {
		max = 0
	}
	return &Budget{max: max}
}

// Used reports the bytes currently reserved.
func (b *Budget) Used() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// Hold is one request's reservation. Add grows it. Release returns
// whatever is still held and is safe to call more than once.
type Hold struct {
	b    *Budget
	n    int64
	grpc bool
	once sync.Once
}

// SetGrpcMax caps how much of the budget gRPC may hold at once.
// A non-positive cap makes ReserveGrpc refuse every reservation.
func (b *Budget) SetGrpcMax(n int64) {
	if b == nil {
		return
	}
	if n < 0 {
		n = 0
	}
	b.mu.Lock()
	b.grpcMax = n
	b.mu.Unlock()
}

// Hold starts an empty reservation. A nil budget yields a hold that
// refuses every Add, so a missing budget fails closed.
func (b *Budget) Hold() *Hold {
	if b == nil {
		return &Hold{}
	}
	return &Hold{b: b}
}

// Reserve holds n bytes or returns false without changing the budget.
func (b *Budget) Reserve(n int64) (*Hold, bool) {
	h := b.Hold()
	if !h.Add(n) {
		return nil, false
	}
	return h, true
}

// ReserveGrpc holds n bytes and counts them against the gRPC share.
// It fails when the share or the shared budget cannot hold n.
func (b *Budget) ReserveGrpc(n int64) (*Hold, bool) {
	if b == nil {
		return nil, false
	}
	h := &Hold{b: b, grpc: true}
	if !h.Add(n) {
		return nil, false
	}
	return h, true
}

// Add reserves n more bytes on this hold. Zero succeeds. A negative n
// fails and changes nothing.
func (h *Hold) Add(n int64) bool {
	if h == nil || h.b == nil || n < 0 {
		return false
	}
	if n == 0 {
		return true
	}
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	if h.n > h.b.max-n || h.b.used > h.b.max-n {
		return false
	}
	if h.grpc && (h.b.grpcMax < n || h.b.grpcUsed > h.b.grpcMax-n || h.n > h.b.grpcMax-n) {
		return false
	}
	h.n += n
	h.b.used += n
	if h.grpc {
		h.b.grpcUsed += n
	}
	return true
}

// Sub returns n bytes from this hold early, after a buffer has been
// dropped. It never releases more than the hold has, and never drives
// the budget negative.
func (h *Hold) Sub(n int64) {
	if h == nil || h.b == nil || n <= 0 {
		return
	}
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	if n > h.n {
		n = h.n
	}
	h.n -= n
	if n > h.b.used {
		h.b.used = 0
	} else {
		h.b.used -= n
	}
	if h.grpc {
		if n > h.b.grpcUsed {
			h.b.grpcUsed = 0
		} else {
			h.b.grpcUsed -= n
		}
	}
}

// Shrink reduces this hold to to bytes. A to at or above the current
// hold, or a hold that Release already cleared, changes nothing.
func (h *Hold) Shrink(to int64) {
	if h == nil || h.b == nil || to < 0 {
		return
	}
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	if h.n <= to {
		return
	}
	drop := h.n - to
	h.n = to
	if drop > h.b.used {
		h.b.used = 0
	} else {
		h.b.used -= drop
	}
	if h.grpc {
		if drop > h.b.grpcUsed {
			h.b.grpcUsed = 0
		} else {
			h.b.grpcUsed -= drop
		}
	}
}

// Release returns the rest of the hold. A second call does nothing.
func (h *Hold) Release() {
	if h == nil || h.b == nil {
		return
	}
	h.once.Do(func() {
		h.b.mu.Lock()
		n := h.n
		h.n = 0
		if n > h.b.used {
			h.b.used = 0
		} else {
			h.b.used -= n
		}
		if h.grpc {
			if n > h.b.grpcUsed {
				h.b.grpcUsed = 0
			} else {
				h.b.grpcUsed -= n
			}
		}
		h.b.mu.Unlock()
	})
}

// RemoteIP is the canonical remote address used as a connection key.
// The key is the full IP, not an IPv6 /64, so a client that rotates
// addresses inside one prefix gets a fresh bucket. An address that
// cannot be parsed still gets its own key, so it counts toward the
// total cap and does not share a bucket with anyone else. The function
// does not read X-Forwarded-For. Behind a proxy every connection counts
// as the proxy.
func RemoteIP(addr net.Addr) string {
	if addr == nil {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	if host == "" {
		return "unknown"
	}
	return host
}

// Listener admits connections up to a total and per-address cap.
// A connection past the cap is answered with 503 or 429 and closed
// inside Accept, and is not returned to the caller.
type Listener struct {
	net.Listener
	conns  *Conns
	mu     sync.Mutex
	active map[net.Conn]struct{}
	// Silent closes an over-limit connection without writing. The gRPC
	// listener uses it so Accept does not write an HTTP response, and
	// does not wait on that write, on a connection that will not speak HTTP.
	Silent bool
}

// Wrap limits ln. maxConns and maxPerIP are the caps Try enforces.
func Wrap(ln net.Listener, maxConns, maxPerIP int) *Listener {
	return &Listener{
		Listener: ln,
		conns:    &Conns{max: maxConns, perMax: maxPerIP},
		active:   map[net.Conn]struct{}{},
	}
}

// Accept returns the next admitted connection. Over-limit connections
// are rejected here and the wait continues.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := RemoteIP(c.RemoteAddr())
		switch l.conns.Try(ip) {
		case Admit:
			tc := &tracked{Conn: c}
			tc.release = func() {
				l.mu.Lock()
				delete(l.active, tc)
				l.mu.Unlock()
				l.conns.Release(ip)
			}
			l.mu.Lock()
			l.active[tc] = struct{}{}
			l.mu.Unlock()
			return tc, nil
		case RejectIP:
			l.reject(c, 429, MsgTooManyIP)
		default:
			l.reject(c, 503, MsgTooManyConns)
		}
	}
}

// CloseActive closes connections that have been admitted and not yet
// closed, so a stalled client cannot outlive Shutdown.
func (l *Listener) CloseActive() {
	if l == nil {
		return
	}
	l.mu.Lock()
	conns := make([]net.Conn, 0, len(l.active))
	for c := range l.active {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

type tracked struct {
	net.Conn
	once    sync.Once
	release func()
}

func (t *tracked) Close() error {
	var err error
	t.once.Do(func() {
		err = t.Conn.Close()
		if t.release != nil {
			t.release()
		}
	})
	return err
}

func (l *Listener) reject(c net.Conn, status int, msg string) {
	if l != nil && l.Silent {
		_ = c.Close()
		return
	}
	rejectConn(c, status, msg)
}

func rejectConn(c net.Conn, status int, msg string) {
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	reason := "Service Unavailable"
	if status == 429 {
		reason = "Too Many Requests"
	}
	_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nRetry-After: %d\r\nConnection: close\r\n\r\n%s",
		status, reason, len(msg), int(RetryAfter/time.Second), msg)
	_ = c.Close()
}
