package server

import (
	"context"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc/stats"
)

// maxHeaderOnlyStreams is how many RPCs on one connection may be waiting
// for their first DATA frame. A header-only stream holds a full body
// reservation until --read-header-timeout. Two is enough for a client
// that pipelines a pair of exports, and a third is rejected so one
// connection cannot fill the gRPC share by opening streams.
const maxHeaderOnlyStreams = 2

const (
	http2Preface   = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
	http2FrameHdr  = 9
	frameData      = 0x0
	frameHeaders   = 0x1
	flagPadded     = 0x8
	phasePreface   = 0
	phaseHeader    = 1
	phasePayload   = 2
	phasePadLength = 3
	phaseData      = 4
	phasePad       = 5
)

// watchKey is the frameWatch for the connection, stored by TagConn.
type watchKey struct{}

// watchTable maps a remote address to the connection that accepted it.
// TagConn looks the watch up; Accept records it.
type watchTable struct {
	mu     sync.Mutex
	byAddr map[string]*frameWatch
}

func newWatchTable() *watchTable {
	return &watchTable{byAddr: map[string]*frameWatch{}}
}

func (t *watchTable) add(addr net.Addr, w *frameWatch) {
	if t == nil || addr == nil {
		return
	}
	t.mu.Lock()
	t.byAddr[addr.String()] = w
	t.mu.Unlock()
}

func (t *watchTable) get(addr net.Addr) *frameWatch {
	if t == nil || addr == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.byAddr[addr.String()]
}

func (t *watchTable) remove(addr net.Addr) {
	if t == nil || addr == nil {
		return
	}
	t.mu.Lock()
	delete(t.byAddr, addr.String())
	t.mu.Unlock()
}

// watchListener wraps each accepted connection so the server can see
// HTTP/2 DATA frames. Read returns at most one frame at a time, so the
// gRPC framer cannot observe the next HEADERS before the tap for this one.
type watchListener struct {
	net.Listener
	table *watchTable
}

func (l *watchListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	w := newFrameWatch(c, l.table)
	l.table.add(c.RemoteAddr(), w)
	return w, nil
}

// streamSlot is one HTTP/2 stream the watcher has seen headers for.
type streamSlot struct {
	sawData bool
	waiting bool
	onData  func()
}

// frameWatch parses HTTP/2 frames as gRPC reads them. It records which
// stream has carried a DATA payload and how many admitted streams are
// still waiting for that first byte.
type frameWatch struct {
	net.Conn
	table  *watchTable
	readMu sync.Mutex
	mu     sync.Mutex

	phase    int
	prefaceN int
	hdr      [http2FrameHdr]byte
	hdrN     int
	remain   int
	typ      byte
	flags    byte
	sid      uint32
	bodyLeft int
	padLeft  int

	haveHeaders bool
	lastHeaders uint32
	slots       map[uint32]*streamSlot
	unread      int

	// Write-side parser. A HEADERS frame for a stream in committing is
	// the status, and it has reached the kernel when Write returns.
	wPhase     int
	wHdr       [http2FrameHdr]byte
	wHdrN      int
	wRemain    int
	wTyp       byte
	wSid       uint32
	committing map[uint32]func()
}

func newFrameWatch(c net.Conn, table *watchTable) *frameWatch {
	return &frameWatch{Conn: c, table: table, slots: map[uint32]*streamSlot{}}
}

func (w *frameWatch) Write(p []byte) (int, error) {
	n, err := w.Conn.Write(p)
	if n > 0 {
		cbs := w.noteWritten(p[:n])
		for _, fn := range cbs {
			fn()
		}
	}
	return n, err
}

func (w *frameWatch) noteWritten(p []byte) []func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	var cbs []func()
	for len(p) > 0 {
		if w.wPhase == 0 {
			n := http2FrameHdr - w.wHdrN
			if n > len(p) {
				n = len(p)
			}
			copy(w.wHdr[w.wHdrN:], p[:n])
			w.wHdrN += n
			p = p[n:]
			if w.wHdrN < http2FrameHdr {
				break
			}
			w.wRemain = int(w.wHdr[0])<<16 | int(w.wHdr[1])<<8 | int(w.wHdr[2])
			w.wTyp = w.wHdr[3]
			w.wSid = uint32(w.wHdr[5])<<24 | uint32(w.wHdr[6])<<16 | uint32(w.wHdr[7])<<8 | uint32(w.wHdr[8])
			w.wSid &= 0x7fffffff
			w.wHdrN = 0
			if w.wRemain == 0 {
				cbs = append(cbs, w.wroteFrameLocked()...)
				continue
			}
			w.wPhase = 1
			continue
		}
		n := w.wRemain
		if n > len(p) {
			n = len(p)
		}
		w.wRemain -= n
		p = p[n:]
		if w.wRemain == 0 {
			w.wPhase = 0
			cbs = append(cbs, w.wroteFrameLocked()...)
		}
	}
	return cbs
}

func (w *frameWatch) wroteFrameLocked() []func() {
	if w.wTyp != frameHeaders {
		return nil
	}
	fn := w.committing[w.wSid]
	if fn == nil {
		return nil
	}
	delete(w.committing, w.wSid)
	return []func(){fn}
}

// trackCommit releases done once a HEADERS frame for id has been written.
// That frame is the gRPC status, so the client can read it before Shutdown
// closes the connection.
func (w *frameWatch) trackCommit(id uint32, done func()) {
	if w == nil || id == 0 || done == nil {
		return
	}
	w.mu.Lock()
	if w.committing == nil {
		w.committing = map[uint32]func(){}
	}
	w.committing[id] = done
	w.mu.Unlock()
}

func (w *frameWatch) Close() error {
	w.table.remove(w.RemoteAddr())
	return w.Conn.Close()
}

func (w *frameWatch) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.readMu.Lock()
	defer w.readMu.Unlock()
	w.mu.Lock()
	n := w.wantLocked()
	w.mu.Unlock()
	if n > len(p) {
		n = len(p)
	}
	if n < 1 {
		n = 1
	}
	nr, err := w.Conn.Read(p[:n])
	var cbs []func()
	if nr > 0 {
		w.mu.Lock()
		cbs = w.feedLocked(p[:nr])
		w.mu.Unlock()
	}
	for _, fn := range cbs {
		fn()
	}
	return nr, err
}

// wantLocked is how many bytes finish the preface or the current frame
// piece. Limiting Read to that keeps the next frame out of this call.
func (w *frameWatch) wantLocked() int {
	switch w.phase {
	case phasePreface:
		return len(http2Preface) - w.prefaceN
	case phaseHeader:
		return http2FrameHdr - w.hdrN
	default:
		if w.remain < 1 {
			return 1
		}
		return w.remain
	}
}

func (w *frameWatch) feedLocked(p []byte) []func() {
	var cbs []func()
	for len(p) > 0 {
		switch w.phase {
		case phasePreface:
			n := len(http2Preface) - w.prefaceN
			if n > len(p) {
				n = len(p)
			}
			w.prefaceN += n
			p = p[n:]
			if w.prefaceN >= len(http2Preface) {
				w.phase = phaseHeader
				w.hdrN = 0
			}
		case phaseHeader:
			n := http2FrameHdr - w.hdrN
			if n > len(p) {
				n = len(p)
			}
			copy(w.hdr[w.hdrN:], p[:n])
			w.hdrN += n
			p = p[n:]
			if w.hdrN < http2FrameHdr {
				break
			}
			w.remain = int(w.hdr[0])<<16 | int(w.hdr[1])<<8 | int(w.hdr[2])
			w.typ = w.hdr[3]
			w.flags = w.hdr[4]
			w.sid = uint32(w.hdr[5])<<24 | uint32(w.hdr[6])<<16 | uint32(w.hdr[7])<<8 | uint32(w.hdr[8])
			w.sid &= 0x7fffffff
			w.hdrN = 0
			if w.typ == frameHeaders && w.sid != 0 {
				w.haveHeaders = true
				w.lastHeaders = w.sid
				w.slotLocked(w.sid)
			}
			if w.remain == 0 {
				w.phase = phaseHeader
				break
			}
			if w.typ == frameData && w.flags&flagPadded != 0 {
				w.phase = phasePadLength
				break
			}
			if w.typ == frameData {
				w.phase = phaseData
				w.bodyLeft = w.remain
				break
			}
			w.phase = phasePayload
		case phasePadLength:
			pad := int(p[0])
			p = p[1:]
			w.remain--
			if pad > w.remain {
				w.phase = phasePayload
				break
			}
			w.bodyLeft = w.remain - pad
			w.padLeft = pad
			if w.bodyLeft > 0 {
				w.phase = phaseData
			} else if w.padLeft > 0 {
				w.phase = phasePad
			} else {
				w.phase = phaseHeader
			}
		case phaseData:
			n := w.bodyLeft
			if n > len(p) {
				n = len(p)
			}
			if n > 0 {
				if w.sid != 0 {
					cbs = append(cbs, w.sawDataLocked(w.sid)...)
				}
				w.bodyLeft -= n
				w.remain -= n
				p = p[n:]
			}
			if w.bodyLeft > 0 {
				break
			}
			if w.padLeft > 0 {
				w.phase = phasePad
				break
			}
			w.phase = phaseHeader
		case phasePad:
			n := w.padLeft
			if n > len(p) {
				n = len(p)
			}
			w.padLeft -= n
			w.remain -= n
			p = p[n:]
			if w.padLeft == 0 {
				w.phase = phaseHeader
			}
		default:
			n := w.remain
			if n > len(p) {
				n = len(p)
			}
			w.remain -= n
			p = p[n:]
			if w.remain == 0 {
				w.phase = phaseHeader
			}
		}
	}
	return cbs
}

func (w *frameWatch) slotLocked(id uint32) *streamSlot {
	sl := w.slots[id]
	if sl == nil {
		sl = &streamSlot{}
		w.slots[id] = sl
	}
	return sl
}

// sawDataLocked marks the stream and returns the promote callback once.
func (w *frameWatch) sawDataLocked(id uint32) []func() {
	sl := w.slotLocked(id)
	var cbs []func()
	if !sl.sawData {
		sl.sawData = true
		if sl.onData != nil {
			cbs = append(cbs, sl.onData)
			sl.onData = nil
		}
	}
	if sl.waiting {
		sl.waiting = false
		if w.unread > 0 {
			w.unread--
		}
	}
	return cbs
}

// claim reports the stream whose headers the tap is handling.
// reject is set when this connection already has maxHeaderOnlyStreams
// RPCs waiting for a DATA frame.
func (w *frameWatch) claim() (id uint32, sawData bool, reject bool) {
	if w == nil {
		return 0, false, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.haveHeaders {
		return 0, false, false
	}
	id = w.lastHeaders
	sl := w.slotLocked(id)
	if sl.sawData {
		return id, true, false
	}
	if w.unread >= maxHeaderOnlyStreams {
		return id, false, true
	}
	return id, false, false
}

// arm counts the stream as waiting and runs fn from Read when the first
// DATA payload byte arrives. A byte that already arrived runs fn now.
func (w *frameWatch) arm(id uint32, fn func()) {
	if w == nil {
		return
	}
	w.mu.Lock()
	sl := w.slotLocked(id)
	if sl.sawData {
		w.mu.Unlock()
		if fn != nil {
			fn()
		}
		return
	}
	if !sl.waiting {
		sl.waiting = true
		w.unread++
	}
	sl.onData = fn
	w.mu.Unlock()
}

// forget drops the waiting count when the RPC ends before any DATA.
func (w *frameWatch) forget(id uint32) {
	if w == nil {
		return
	}
	w.mu.Lock()
	sl := w.slots[id]
	if sl != nil && sl.waiting {
		sl.waiting = false
		sl.onData = nil
		if w.unread > 0 {
			w.unread--
		}
	}
	delete(w.slots, id)
	w.mu.Unlock()
}

// commitGate counts gRPC handlers that have started a commit and have
// not yet had their status written. Shutdown waits for it, bounded,
// before it force-closes connections.
type commitGate struct {
	mu   sync.Mutex
	cond *sync.Cond
	n    int
}

func newCommitGate() *commitGate {
	g := &commitGate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *commitGate) add() {
	g.mu.Lock()
	g.n++
	g.mu.Unlock()
}

func (g *commitGate) done() {
	g.mu.Lock()
	if g.n > 0 {
		g.n--
	}
	if g.n == 0 {
		g.cond.Broadcast()
	}
	g.mu.Unlock()
}

func (g *commitGate) wait(d time.Duration) {
	done := make(chan struct{})
	go func() {
		g.mu.Lock()
		for g.n > 0 {
			g.cond.Wait()
		}
		g.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// TagConn attaches the frame watch so admitRPC can see this connection.
func (s *Server) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	if s == nil || s.watches == nil || info == nil {
		return ctx
	}
	if w := s.watches.get(info.RemoteAddr); w != nil {
		return context.WithValue(ctx, watchKey{}, w)
	}
	return ctx
}

// TagRPC keeps the stream context, including the reservation and the watch.
func (s *Server) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}

// HandleRPC releases the commit gate after gRPC has written the status.
func (s *Server) HandleRPC(ctx context.Context, rs stats.RPCStats) {
	if _, ok := rs.(*stats.End); ok {
		releaseGRPCCommit(ctx)
	}
}

// HandleConn is part of the stats handler. The connection watch is
// removed when the wrapped conn closes.
func (s *Server) HandleConn(context.Context, stats.ConnStats) {}
