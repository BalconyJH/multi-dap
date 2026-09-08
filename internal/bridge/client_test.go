package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/mbp"
)

const testHandshake = `{"protocol_version":1,"bridge_version":"test","max_message_size":4096,"encoding":"utf-8"}` + "\n"

func TestClientHandshakeValidation(t *testing.T) {
	tests := []struct {
		name      string
		handshake string
	}{
		{"version", `{"protocol_version":2,"bridge_version":"test","max_message_size":4096,"encoding":"utf-8"}` + "\n"},
		{"encoding", `{"protocol_version":1,"bridge_version":"test","max_message_size":4096,"encoding":"latin-1"}` + "\n"},
		{"max", `{"protocol_version":1,"bridge_version":"test","max_message_size":0,"encoding":"utf-8"}` + "\n"},
		{"unknown field", `{"protocol_version":1,"bridge_version":"test","max_message_size":4096,"encoding":"utf-8","extra":true}` + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			go func() { _, _ = server.Write([]byte(test.handshake)); _ = server.Close() }()
			if _, err := NewClient(client); err == nil {
				t.Fatal("NewClient() error = nil, want handshake rejection")
			}
		})
	}
}

func TestClientCallSuccessAndRemoteError(t *testing.T) {
	client := pipeClient(t, func(server net.Conn) {
		decoder := mbp.NewDecoder(server)
		frame, err := decoder.Decode()
		if err != nil {
			t.Errorf("server Decode() error = %v", err)
			return
		}
		request := frame.(*mbp.Request)
		if request.ID != 1 || request.Method != "state" {
			t.Errorf("request = %#v, want id=1 method=state", request)
		}
		if err := mbp.NewEncoder(server).Encode(&mbp.Response{ID: request.ID, OK: true, Result: json.RawMessage(`{"running":false}`)}); err != nil {
			t.Errorf("server Encode() error = %v", err)
		}
		frame, err = decoder.Decode()
		if err != nil {
			t.Errorf("server Decode second request error = %v", err)
			return
		}
		request = frame.(*mbp.Request)
		if err := mbp.NewEncoder(server).Encode(&mbp.Response{ID: request.ID, OK: false, Error: &mbp.Error{Kind: "multi_refused", Message: "no", Raw: "E>"}}); err != nil {
			t.Errorf("server Encode error response error = %v", err)
		}
	})
	defer client.Close()

	var result struct {
		Running bool `json:"running"`
	}
	if err := client.Call(context.Background(), "state", nil, &result); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result.Running {
		t.Fatal("result.Running = true, want false")
	}
	err := client.Call(context.Background(), "state", nil, nil)
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("Call() error = %v, want RemoteError", err)
	}
	if client.Poisoned() {
		t.Fatal("valid remote error poisoned client")
	}
}

func TestClientWrongIDPoisonsConnection(t *testing.T) {
	client := pipeClient(t, func(server net.Conn) {
		request := readRequest(t, server)
		_ = mbp.NewEncoder(server).Encode(&mbp.Response{ID: request.ID + 1, OK: true, Result: json.RawMessage(`{}`)})
	})
	defer client.Close()
	if err := client.Call(context.Background(), "state", nil, nil); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("Call() error = %v, want ErrPoisoned", err)
	}
	if !client.Poisoned() {
		t.Fatal("client not poisoned after wrong response ID")
	}
	if err := client.Call(context.Background(), "state", nil, nil); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("second Call() error = %v, want ErrPoisoned", err)
	}
}

func TestClientResultDecodeFailurePoisonsConnection(t *testing.T) {
	client := pipeClient(t, func(server net.Conn) {
		request := readRequest(t, server)
		_ = mbp.NewEncoder(server).Encode(&mbp.Response{ID: request.ID, OK: true, Result: json.RawMessage(`{"value":"text"}`)})
	})
	defer client.Close()
	var result struct {
		Value int `json:"value"`
	}
	if err := client.Call(context.Background(), "state", nil, &result); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("Call() error = %v, want poisoned decode failure", err)
	}
	if !client.Poisoned() {
		t.Fatal("client not poisoned after result decode failure")
	}
}

func TestClientMalformedResponsePoisonsConnection(t *testing.T) {
	client := pipeClient(t, func(server net.Conn) {
		request := readRequest(t, server)
		// A success envelope cannot carry error in place of result.
		_, _ = server.Write([]byte(`{"id":` + "1" + `,"ok":true,"error":{"kind":"bad","message":"bad","raw":""}}` + "\n"))
		if request.ID != 1 {
			t.Errorf("request ID = %d, want 1", request.ID)
		}
	})
	defer client.Close()
	if err := client.Call(context.Background(), "state", nil, nil); err == nil {
		t.Fatal("Call() error = nil, want malformed-response protocol error")
	}
	if !client.Poisoned() {
		t.Fatal("client not poisoned after malformed response")
	}
}

func TestClientTimeoutPoisonsConnection(t *testing.T) {
	client := pipeClient(t, func(server net.Conn) {
		_ = readRequest(t, server)
		// Keep the peer open until Client's deadline wakes its Read and poisons
		// the connection. Returning here would exercise EOF, not timeout.
		var one [1]byte
		_, _ = server.Read(one[:])
	})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := client.Call(ctx, "state", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call() error = %v, want context deadline exceeded", err)
	}
	if !client.Poisoned() {
		t.Fatal("client not poisoned after timeout")
	}
}

func TestDialLoopbackRejectsNonLoopback(t *testing.T) {
	if _, err := DialLoopback(context.Background(), "192.0.2.1:1234"); !errors.Is(err, ErrNonLoopbackAddress) {
		t.Fatalf("DialLoopback(non-loopback) error = %v, want ErrNonLoopbackAddress", err)
	}
	if err := validateLoopbackAddress("127.0.0.1:1234"); err != nil {
		t.Fatalf("validateLoopbackAddress(loopback) error = %v", err)
	}
}

func pipeClient(t *testing.T, serve func(net.Conn)) *Client {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	go func() {
		defer serverConn.Close()
		if _, err := serverConn.Write([]byte(testHandshake)); err != nil {
			return
		}
		serve(serverConn)
	}()
	client, err := NewClient(clientConn)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func readRequest(t *testing.T, conn net.Conn) *mbp.Request {
	t.Helper()
	frame, err := mbp.NewDecoder(conn).Decode()
	if err != nil {
		t.Errorf("server Decode() error = %v", err)
		return &mbp.Request{}
	}
	request, ok := frame.(*mbp.Request)
	if !ok {
		t.Errorf("server frame = %T, want *mbp.Request", frame)
		return &mbp.Request{}
	}
	return request
}
