package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

const testConfigDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

const otherTestConfigDigest = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

func TestServerStatusRequiresTokenAndUsesLoopback(t *testing.T) {
	server, err := NewServer(ServerOptions{
		ProbeID: "probe-1", ConfigDigest: testConfigDigest, DAPAddress: "127.0.0.1:43123",
		Status: func(context.Context) (Status, error) {
			return Status{ProbeID: "probe-1", PID: os.Getpid(), DAPAddress: "127.0.0.1:43123", TargetState: "stopped", ExecutionEpoch: 4, StopEpoch: 9, DAPOwned: 5, Pending: 2, Orphaned: 1}, nil
		},
		Shutdown: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if host, _, err := net.SplitHostPort(server.Record().ControlAddress); err != nil || !net.ParseIP(host).IsLoopback() {
		t.Fatalf("control address = %q, err = %v", server.Record().ControlAddress, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { <-done })

	status, err := Call(context.Background(), server.Record(), MethodStatus)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.TargetState != "stopped" || status.StopEpoch != 9 || status.DAPOwned != 5 || status.Pending != 2 || status.Orphaned != 1 {
		t.Fatalf("status = %+v", status)
	}

	wrong := server.Record()
	wrong.Token, err = randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Call(context.Background(), wrong, MethodStatus); !errors.Is(err, ErrStaleRecord) {
		t.Fatalf("wrong token error = %v, want stale record", err)
	}
}

func TestServerRejectsUnknownDuplicateAndOversizeRequests(t *testing.T) {
	server, err := NewServer(ServerOptions{
		ProbeID: "probe-1", ConfigDigest: testConfigDigest, DAPAddress: "127.0.0.1:43123",
		Status: func(context.Context) (Status, error) { return Status{}, nil }, Shutdown: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { <-done })
	for _, request := range [][]byte{
		[]byte(`{"version":1,"method":"status","token":"a","extra":0}` + "\n"),
		[]byte(`{"version":1,"version":1,"method":"status","token":"a"}` + "\n"),
		append(bytes.Repeat([]byte{'x'}, MaxMessageSize+1), '\n'),
	} {
		connection, err := net.Dial("tcp", server.Record().ControlAddress)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Write(request); err != nil {
			t.Fatal(err)
		}
		if len(request) > MaxMessageSize {
			buffer := make([]byte, 1)
			_, _ = connection.Read(buffer) // oversize is deliberately dropped.
			_ = connection.Close()
			continue
		}
		line, err := readLine(connection, MaxMessageSize)
		_ = connection.Close()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var reply response
		if err := json.Unmarshal(line, &reply); err != nil || reply.OK || reply.Error == nil || reply.Error.Code != "malformed" {
			t.Fatalf("request %q reply = %s, err = %v", request, line, err)
		}
	}
}

func TestStoreStaleRecordAndInstanceGuard(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	record := Record{Version: ProtocolVersion, ProbeID: "probe-1", ConfigDigest: testConfigDigest, InstanceID: instance, PID: 42, ControlAddress: "127.0.0.1:1", DAPAddress: "127.0.0.1:43123", Token: token}
	if err := store.Publish(record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Call(context.Background(), loaded, MethodStatus); !errors.Is(err, ErrStaleRecord) {
		t.Fatalf("stale call error = %v", err)
	}
	later, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveIfInstance("probe-1", later); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("probe-1"); err != nil {
		t.Fatalf("wrong instance removed record: %v", err)
	}
	if err := store.RemoveIfEqual(loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("probe-1"); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("record after stale removal: %v", err)
	}
}

func TestConfigBoundLoadRejectsDifferentOrInvalidDigest(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	record := Record{Version: ProtocolVersion, ProbeID: "probe-1", ConfigDigest: testConfigDigest, InstanceID: instance, PID: 42, ControlAddress: "127.0.0.1:1", DAPAddress: "127.0.0.1:43123", Token: token}
	if err := store.Publish(record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadForConfig("probe-1", otherTestConfigDigest); !errors.Is(err, ErrConfigurationMismatch) {
		t.Fatalf("LoadForConfig(other) error = %v, want configuration mismatch", err)
	}
	if _, err := store.LoadForConfig("probe-1", strings.ToUpper(testConfigDigest)); !errors.Is(err, ErrConfigurationMismatch) {
		t.Fatalf("LoadForConfig(uppercase) error = %v, want configuration mismatch", err)
	}
	if got, err := store.LoadForConfig("probe-1", testConfigDigest); err != nil || !equalRecord(got, record) {
		t.Fatalf("LoadForConfig(exact) = (%#v, %v)", got, err)
	}
	bad := record
	bad.ConfigDigest = strings.ToUpper(testConfigDigest)
	if err := store.Publish(bad); err == nil {
		t.Fatal("Publish accepted uppercase configuration digest")
	}
}

func TestReservationIsActiveUntilCommittedOrCancelled(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.Reserve("probe-1", testConfigDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("probe-1"); !errors.Is(err, ErrRecordActive) {
		t.Fatalf("Load(reservation) error = %v, want active", err)
	}
	if err := reservation.Cancel(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("probe-1"); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("Load(cancelled reservation) error = %v, want no record", err)
	}

	reservation, err = store.Reserve("probe-1", testConfigDigest)
	if err != nil {
		t.Fatal(err)
	}
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	record := Record{Version: ProtocolVersion, ProbeID: "probe-1", ConfigDigest: testConfigDigest, InstanceID: instance, PID: os.Getpid(), ControlAddress: "127.0.0.1:43122", DAPAddress: "127.0.0.1:43123", Token: token}
	path, err := store.recordPath("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := openStoreFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := reservation.Commit(record); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load("probe-1"); err != nil || !equalRecord(loaded, record) {
		t.Fatalf("Load(committed reservation) = (%#v, %v)", loaded, err)
	}
	if recovered, err := store.RecoverStarting("probe-1", testConfigDigest); err != nil || recovered {
		t.Fatalf("RecoverStarting(committed record) = (%v, %v)", recovered, err)
	}
	if err := reservation.Cancel(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("probe-1"); err != nil {
		t.Fatalf("Cancel committed reservation removed record: %v", err)
	}
}

func TestReservationBindsDigestAndReserveRejectsOtherConfiguration(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.Reserve("probe-1", testConfigDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve("probe-1", otherTestConfigDigest); !errors.Is(err, ErrConfigurationMismatch) {
		t.Fatalf("Reserve(other) error = %v, want configuration mismatch", err)
	}
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	wrong := Record{Version: ProtocolVersion, ProbeID: "probe-1", ConfigDigest: otherTestConfigDigest, InstanceID: instance, PID: os.Getpid(), ControlAddress: "127.0.0.1:43122", DAPAddress: "127.0.0.1:43123", Token: token}
	if err := reservation.Commit(wrong); !errors.Is(err, ErrConfigurationMismatch) {
		t.Fatalf("Commit(other digest) error = %v, want configuration mismatch", err)
	}
	if err := reservation.Cancel(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverStartingReclaimsOnlyDeadReservation(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(startingRecord{Version: ProtocolVersion, State: "starting", ConfigDigest: testConfigDigest, InstanceID: instance, PID: int(^uint(0) >> 1)})
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.recordPath("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.RecoverStarting("probe-1", testConfigDigest)
	if err != nil || !recovered {
		t.Fatalf("RecoverStarting() = (%v, %v)", recovered, err)
	}
	if _, err := store.Load("probe-1"); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("Load(recovered reservation) error = %v, want no record", err)
	}
}

func TestRecoverStartingDoesNotTouchOtherConfigurationMarker(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(startingRecord{Version: ProtocolVersion, State: "starting", ConfigDigest: testConfigDigest, InstanceID: instance, PID: int(^uint(0) >> 1)})
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.recordPath("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if recovered, err := store.RecoverStarting("probe-1", otherTestConfigDigest); recovered || !errors.Is(err, ErrConfigurationMismatch) {
		t.Fatalf("RecoverStarting(other) = (%v, %v), want mismatch without recovery", recovered, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, marker) {
		t.Fatalf("mismatched recovery changed marker: %q, %v", got, err)
	}
}

func TestServerCanReserveBeforeDAPAddressIsKnown(t *testing.T) {
	server, err := NewServer(ServerOptions{ProbeID: "probe-1", ConfigDigest: testConfigDigest, Status: func(context.Context) (Status, error) { return Status{}, nil }, Shutdown: func() {}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if server.Record().DAPAddress != "" {
		t.Fatalf("unfinalized DAP address = %q", server.Record().DAPAddress)
	}
	if err := server.SetDAPAddress("127.0.0.1:43123"); err != nil {
		t.Fatal(err)
	}
	if err := server.SetDAPAddress("127.0.0.1:43124"); err == nil {
		t.Fatal("second DAP address finalization unexpectedly succeeded")
	}
}

func TestTerminalRecordRoundTripAndStartupPreservesPriorInstance(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	want := TerminalRecord{
		Version: ProtocolVersion, ProbeID: "probe-1", ConfigDigest: testConfigDigest, InstanceID: instance, PID: 42,
		Phase: TerminalPhaseRuntimeServe, Classification: TerminalContextDeadline,
		Operation:  TerminalOperationState,
		OccurredAt: time.Date(2026, time.August, 20, 1, 2, 3, 0, time.UTC),
	}
	if err := store.WriteTerminal(want); err != nil {
		t.Fatal(err)
	}
	reservation, err := store.Reserve("probe-1", testConfigDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Cancel(); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadTerminal("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.OccurredAt.Equal(want.OccurredAt) || got.ProbeID != want.ProbeID || got.InstanceID != want.InstanceID || got.PID != want.PID || got.Phase != want.Phase || got.Classification != want.Classification || got.Operation != want.Operation {
		t.Fatalf("terminal record = %#v, want %#v", got, want)
	}
	want.Classification = TerminalBridgeExit
	want.PID = 43
	if err := store.WriteTerminal(want); err != nil {
		t.Fatal(err)
	}
	got, err = store.LoadTerminal("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != want.PID || got.Classification != want.Classification {
		t.Fatalf("replaced terminal record = %#v, want %#v", got, want)
	}
}

func TestConfigBoundTerminalLoadRejectsDifferentConfiguration(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	want := TerminalRecord{Version: ProtocolVersion, ProbeID: "probe-1", ConfigDigest: testConfigDigest, InstanceID: instance, PID: 42, Phase: TerminalPhaseRuntimeServe, Classification: TerminalOther, OccurredAt: time.Now().UTC()}
	if err := store.WriteTerminal(want); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadTerminalForConfig("probe-1", otherTestConfigDigest); !errors.Is(err, ErrConfigurationMismatch) {
		t.Fatalf("LoadTerminalForConfig(other) error = %v, want mismatch", err)
	}
	if got, err := store.LoadTerminalForConfig("probe-1", testConfigDigest); err != nil || got.ConfigDigest != testConfigDigest {
		t.Fatalf("LoadTerminalForConfig(exact) = (%#v, %v)", got, err)
	}
}

func TestServerRequiresCanonicalConfigurationDigestAndDoesNotExposeIt(t *testing.T) {
	if _, err := NewServer(ServerOptions{ProbeID: "probe-1", Status: func(context.Context) (Status, error) { return Status{}, nil }, Shutdown: func() {}}); err == nil {
		t.Fatal("NewServer accepted missing configuration digest")
	}
	if _, err := NewServer(ServerOptions{ProbeID: "probe-1", ConfigDigest: strings.ToUpper(testConfigDigest), Status: func(context.Context) (Status, error) { return Status{}, nil }, Shutdown: func() {}}); err == nil {
		t.Fatal("NewServer accepted uppercase configuration digest")
	}
	data, err := json.Marshal(Status{ProbeID: "probe-1", PID: 42, DAPAddress: "127.0.0.1:43123", TargetState: "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("config_digest")) || bytes.Contains(data, []byte(testConfigDigest)) {
		t.Fatalf("public status exposed configuration binding: %s", data)
	}
}

func TestTerminalRecordAcceptsPreOperationSchema(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.terminalPath("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	oldRecord := `{"version":1,"probe_id":"probe-1","config_digest":"` + testConfigDigest + `","instance_id":"` + instance + `","pid":42,"phase":"runtime_serve","classification":"other","occurred_at":"2026-08-20T01:02:03Z"}`
	if err := os.WriteFile(path, []byte(oldRecord), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := store.LoadTerminal("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if record.Operation != "" {
		t.Fatalf("pre-operation record operation = %q, want empty", record.Operation)
	}
}

func TestTerminalStorageErrorsDoNotExposePaths(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.terminalPath("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadTerminal("probe-1"); !errors.Is(err, ErrTerminalRecordUnavailable) || strings.Contains(err.Error(), path) {
		t.Fatalf("LoadTerminal() error = %v, want stable unavailable error without path", err)
	}
	instance, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	err = store.WriteTerminal(TerminalRecord{
		Version: ProtocolVersion, ProbeID: "probe-1", ConfigDigest: testConfigDigest, InstanceID: instance, PID: 42,
		Phase: TerminalPhaseRuntimeServe, Classification: TerminalOther, OccurredAt: time.Now().UTC(),
	})
	if !errors.Is(err, ErrTerminalRecordWrite) || strings.Contains(err.Error(), path) {
		t.Fatalf("WriteTerminal() error = %v, want stable write error without path", err)
	}
}

func TestTerminalRecordRejectsUnsafeSchema(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteTerminal(TerminalRecord{
		Version: ProtocolVersion, ProbeID: "probe-1", ConfigDigest: testConfigDigest, InstanceID: instance, PID: 42,
		Phase: TerminalPhase("unknown"), Classification: TerminalOther, OccurredAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("WriteTerminal accepted unknown phase")
	}
	path, err := store.terminalPath("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"probe_id":"probe-1","config_digest":"`+testConfigDigest+`","instance_id":"`+instance+`","pid":42,"phase":"runtime_serve","classification":"other","occurred_at":"2026-08-20T01:02:03Z","raw":"forbidden"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadTerminal("probe-1"); err == nil {
		t.Fatal("LoadTerminal accepted unknown schema field")
	}
}

func TestProxyForwardsBytesAndPreservesInputEOF(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer connection.Close()
		got, err := io.ReadAll(connection)
		if err != nil {
			serverDone <- err
			return
		}
		if string(got) != "Content-Length: 2\r\n\r\n{}" {
			serverDone <- errors.New("DAP input changed")
			return
		}
		_, err = connection.Write([]byte("Content-Length: 2\r\n\r\n{}"))
		serverDone <- err
	}()
	var output bytes.Buffer
	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := proxyConnection(context.Background(), connection, bytes.NewBufferString("Content-Length: 2\r\n\r\n{}"), &output); err != nil {
		t.Fatalf("proxy: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "Content-Length: 2\r\n\r\n{}"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestProxyAppliesOutputBackpressureWithoutDroppingBytes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	payload := bytes.Repeat([]byte("dap"), 64*1024)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = io.ReadAll(connection)
		_, _ = connection.Write(payload)
	}()
	writer := &gateWriter{opened: make(chan struct{})}
	done := make(chan error, 1)
	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	go func() { done <- proxyConnection(context.Background(), connection, bytes.NewReader(nil), writer) }()
	select {
	case err := <-done:
		t.Fatalf("proxy completed before output drain: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(writer.opened)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := writer.buffer.Len(); got != len(payload) {
		t.Fatalf("forwarded %d bytes, want %d", got, len(payload))
	}
}

func TestProxyUpgradeNeverDialsPublishedDAPAddress(t *testing.T) {
	// The published DAP port is deliberately occupied by an unrelated local
	// listener. A status-then-dial implementation would connect to it after a
	// daemon death/rebind; the authenticated upgrade must instead hand the
	// original control socket directly to the daemon callback.
	malicious, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer malicious.Close()
	server, err := NewServer(ServerOptions{
		ProbeID: "probe-1", ConfigDigest: testConfigDigest, DAPAddress: malicious.Addr().String(),
		Status:   func(context.Context) (Status, error) { return Status{}, nil },
		Shutdown: func() {},
		Proxy: func(_ context.Context, connection net.Conn) error {
			data, err := io.ReadAll(connection)
			if err != nil {
				return err
			}
			if string(data) != "DAP request" {
				return errors.New("unexpected upgraded DAP input")
			}
			_, err = connection.Write([]byte("DAP response"))
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { <-done })

	var output bytes.Buffer
	if err := Proxy(context.Background(), server.Record(), bytes.NewBufferString("DAP request"), &output); err != nil {
		t.Fatalf("proxy upgrade: %v", err)
	}
	if output.String() != "DAP response" {
		t.Fatalf("proxy output = %q", output.String())
	}
	if tcp, ok := malicious.(*net.TCPListener); ok {
		if err := tcp.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if connection, err := malicious.Accept(); err == nil {
		_ = connection.Close()
		t.Fatal("proxy dialed the published DAP address")
	} else if networkErr, ok := err.(net.Error); !ok || !networkErr.Timeout() {
		t.Fatalf("observe malicious listener: %v", err)
	}
}

type gateWriter struct {
	opened chan struct{}
	buffer bytes.Buffer
}

func (w *gateWriter) Write(data []byte) (int, error) {
	<-w.opened
	return w.buffer.Write(data)
}
