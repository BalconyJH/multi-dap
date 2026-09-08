package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Proxy authenticates and upgrades one control connection, then copies raw
// DAP bytes in both directions without examining DAP framing or JSON. The
// server hands that exact socket to its DAP server, so no mutable DAP address
// is consulted after the control token is accepted.
func Proxy(ctx context.Context, record Record, input io.Reader, output io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRecord(record, record.ProbeID); err != nil {
		return err
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", record.ControlAddress)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStaleRecord, err)
	}
	defer connection.Close()
	deadline := time.Now().Add(controlDeadline)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return fmt.Errorf("%w: %v", ErrStaleRecord, err)
	}
	request, _ := json.Marshal(struct {
		Version int    `json:"version"`
		Method  string `json:"method"`
		Token   string `json:"token"`
	}{ProtocolVersion, MethodProxy, record.Token})
	if _, err := connection.Write(append(request, '\n')); err != nil {
		return fmt.Errorf("%w: %v", ErrStaleRecord, err)
	}
	line, err := readLine(connection, MaxMessageSize)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStaleRecord, err)
	}
	response, err := decodeResponse(line)
	if err != nil || !response.OK {
		if err == nil && response.Error != nil {
			err = errors.New(response.Error.Message)
		}
		return fmt.Errorf("%w: proxy upgrade: %v", ErrStaleRecord, err)
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("control: clear proxy deadline: %w", err)
	}
	return proxyConnection(ctx, connection, input, output)
}

// proxyConnection is shared by the upgraded control client and tests. EOF
// from input is translated to TCP half-close; bytes already in the
// daemon-to-client direction continue to drain to output.
func proxyConnection(ctx context.Context, connection net.Conn, input io.Reader, output io.Writer) error {
	var once sync.Once
	closeWrite := func() {
		once.Do(func() {
			if tcp, ok := connection.(*net.TCPConn); ok {
				_ = tcp.CloseWrite()
			}
		})
	}
	toDaemon := make(chan error, 1)
	fromDaemon := make(chan error, 1)
	go func() {
		_, err := io.Copy(connection, input)
		closeWrite()
		toDaemon <- err
	}()
	go func() {
		_, err := io.Copy(output, connection)
		fromDaemon <- err
	}()
	// A clean EOF in either direction is a half-close, not permission to
	// discard bytes in the other direction. Only an error or cancellation
	// tears down the full TCP connection early.
	for completed := 0; completed != 2; {
		select {
		case err := <-toDaemon:
			if err != nil {
				_ = connection.Close()
				return fmt.Errorf("control: copy DAP input: %w", err)
			}
			completed++
			toDaemon = nil
		case err := <-fromDaemon:
			if err != nil {
				_ = connection.Close()
				return fmt.Errorf("control: copy DAP output: %w", err)
			}
			completed++
			fromDaemon = nil
		case <-ctx.Done():
			_ = connection.Close()
			return ctx.Err()
		}
	}
	return nil
}
