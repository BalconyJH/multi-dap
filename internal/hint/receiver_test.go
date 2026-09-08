package hint

import (
	"context"
	"net"
	"testing"
	"time"
)

func newReceiver(t *testing.T, queue int) (*Receiver, *net.UDPConn) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReceiver(conn, "nonce", queue)
	if err != nil {
		t.Fatal(err)
	}
	return r, conn
}

func TestReceiverValidatesNonceAndToken(t *testing.T) {
	r, conn := newReceiver(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	sender, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	for _, packet := range []string{`{"nonce":"wrong","token":1}`, `{"nonce":"nonce","token":0}`, `{"nonce":"nonce","token":7}`} {
		if _, err := sender.Write([]byte(packet)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case got := <-r.Hints():
		if got != 7 {
			t.Fatalf("token = %d", got)
		}
	case <-time.After(time.Second):
		t.Fatal("valid token not delivered")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReceiverDropsRatherThanBlocking(t *testing.T) {
	r, conn := newReceiver(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	sender, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	for i := 1; i <= 30; i++ {
		_, _ = sender.Write([]byte(`{"nonce":"nonce","token":1}`))
	}
	select {
	case <-r.Hints():
	case <-time.After(time.Second):
		t.Fatal("first token missing")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReceiverRefusesANonLoopbackBinding(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := NewReceiver(conn, "nonce", 1); err != ErrNonLoopbackBind {
		t.Fatalf("NewReceiver() error = %v, want ErrNonLoopbackBind", err)
	}
}
