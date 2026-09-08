// Package hint provides the deliberately lossy, nonce-bound loopback UDP
// receiver used for breakpoint hit hints. It has no debugger dependency: a
// valid token merely asks the owner to obtain a fresh authoritative state
// observation.
package hint

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

const maxDatagram = 256

var (
	ErrNonceRequired   = errors.New("hint: nonce is required")
	ErrNonLoopbackBind = errors.New("hint: packet connection must bind loopback")
)

// Receiver accepts compact {"nonce":"...","token":N} datagrams from a
// loopback PacketConn. Tokens are delivered through a bounded channel; when
// the consumer is busy, datagrams are intentionally dropped rather than
// back-pressuring the debugger or accumulating unbounded memory.
type Receiver struct {
	conn  net.PacketConn
	nonce string
	hints chan uint32
	once  sync.Once
}

// NewReceiver owns conn after construction. queue is the maximum number of
// accepted tokens waiting for the consumer; it must be positive.
func NewReceiver(conn net.PacketConn, nonce string, queue int) (*Receiver, error) {
	if conn == nil {
		return nil, errors.New("hint: packet connection is required")
	}
	if !isLoopback(conn.LocalAddr()) {
		return nil, ErrNonLoopbackBind
	}
	if nonce == "" {
		return nil, ErrNonceRequired
	}
	if queue <= 0 {
		return nil, errors.New("hint: queue must be positive")
	}
	return &Receiver{conn: conn, nonce: nonce, hints: make(chan uint32, queue)}, nil
}

// Hints exposes accepted tokens. Values may be dropped under load by design.
func (r *Receiver) Hints() <-chan uint32 { return r.hints }

// Close interrupts Run. It is safe to call more than once.
func (r *Receiver) Close() error {
	var err error
	r.once.Do(func() { err = r.conn.Close() })
	return err
}

// Run receives until ctx is cancelled or the connection is closed. It does
// not create a goroutine per datagram. The output channel closes exactly once
// when Run returns; callers should start at most one Run invocation.
func (r *Receiver) Run(ctx context.Context) error {
	defer close(r.hints)
	var buf [maxDatagram]byte
	for {
		if err := r.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			return err
		}
		n, addr, err := r.conn.ReadFrom(buf[:])
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if networkErr, ok := err.(net.Error); ok && networkErr.Timeout() {
				continue
			}
			return err
		}
		if !isLoopback(addr) {
			continue
		}
		token, ok := r.parse(buf[:n])
		if !ok {
			continue
		}
		select {
		case r.hints <- token:
		default:
		}
	}
}

func (r *Receiver) parse(data []byte) (uint32, bool) {
	var wire struct {
		Nonce string `json:"nonce"`
		Token uint32 `json:"token"`
	}
	if len(data) == 0 || json.Unmarshal(data, &wire) != nil || wire.Token == 0 {
		return 0, false
	}
	if subtle.ConstantTimeCompare([]byte(wire.Nonce), []byte(r.nonce)) != 1 {
		return 0, false
	}
	return wire.Token, true
}

func isLoopback(addr net.Addr) bool {
	udp, ok := addr.(*net.UDPAddr)
	return ok && udp.IP.IsLoopback()
}
