package bridge

import (
	"reflect"
	"testing"
)

func TestLaunchSpecArgs(t *testing.T) {
	spec := LaunchSpec{Executable: "mpythonrun.exe", BridgeScript: "bridge.py", RPCPort: 43123}
	args, err := spec.Args()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-f", "bridge.py", "-args", "--rpc-port", "43123", "--session-mode", "cold"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("Args() = %#v, want %#v", args, want)
	}
}

func TestLaunchSpecRPCAddress(t *testing.T) {
	spec := LaunchSpec{
		Executable: "mpythonrun.exe", BridgeScript: "bridge.py", RPCPort: 43123,
		RPCHost: "::1",
	}
	args, err := spec.Args()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-f", "bridge.py", "-args", "--rpc-port", "43123", "--session-mode", "cold", "--rpc-host", "::1"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("Args() = %#v, want %#v", args, want)
	}
}

func TestLaunchSpecNormalizesRPCAddressForPythonBridge(t *testing.T) {
	spec := LaunchSpec{
		Executable: "mpythonrun.exe", BridgeScript: "bridge.py", RPCPort: 43123,
		RPCHost: "0:0:0:0:0:0:0:1",
	}
	args, err := spec.Args()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-f", "bridge.py", "-args", "--rpc-port", "43123", "--session-mode", "cold", "--rpc-host", "::1"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("Args() = %#v, want %#v", args, want)
	}
}

func TestLaunchSpecRejectsNonLoopbackRPCHost(t *testing.T) {
	spec := LaunchSpec{
		Executable: "mpythonrun.exe", BridgeScript: "bridge.py", RPCPort: 43123,
		RPCHost: "localhost",
	}
	if _, err := spec.Args(); err == nil {
		t.Fatal("Args() error = nil, want RPC host validation error")
	}
}

func TestLaunchSpecServiceRouterArgs(t *testing.T) {
	spec := LaunchSpec{
		Executable: "mpythonrun.exe", BridgeScript: "bridge.py", RPCPort: 43123,
		ServiceRouter: &ServiceRouter{Host: "127.0.0.1", Port: 40123},
	}
	args, err := spec.Args()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-sr_connect_servicerouter_host", "127.0.0.1",
		"-sr_connect_servicerouter_port", "40123",
		"-f", "bridge.py", "-args", "--rpc-port", "43123", "--session-mode", "cold",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("Args() = %#v, want %#v", args, want)
	}
}

func TestWarmLaunchRequiresAndUsesServiceRouter(t *testing.T) {
	withoutRouter := LaunchSpec{Executable: "mpythonrun.exe", BridgeScript: "bridge.py", RPCPort: 43123, SessionMode: SessionModeWarm}
	if _, err := withoutRouter.Args(); err == nil {
		t.Fatal("warm Args() without service router succeeded")
	}
	withRouter := withoutRouter
	withRouter.ServiceRouter = &ServiceRouter{Host: "127.0.0.1", Port: 40123}
	args, err := withRouter.Args()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-sr_connect_servicerouter_host", "127.0.0.1",
		"-sr_connect_servicerouter_port", "40123",
		"-f", "bridge.py", "-args", "--rpc-port", "43123", "--session-mode", "warm",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("Args() = %#v, want %#v", args, want)
	}
}

func TestLaunchSpecRejectsIncompleteServiceRouter(t *testing.T) {
	spec := LaunchSpec{Executable: "mpythonrun.exe", BridgeScript: "bridge.py", RPCPort: 43123, ServiceRouter: &ServiceRouter{Host: "127.0.0.1"}}
	if _, err := spec.Args(); err == nil {
		t.Fatal("Args() error = nil, want service-router validation error")
	}
}

func TestLaunchSpecRejectsNonLoopbackServiceRouter(t *testing.T) {
	spec := LaunchSpec{Executable: "mpythonrun.exe", BridgeScript: "bridge.py", RPCPort: 43123, ServiceRouter: &ServiceRouter{Host: "192.0.2.1", Port: 40123}}
	if _, err := spec.Args(); err == nil {
		t.Fatal("Args() error = nil, want non-loopback service-router rejection")
	}
}

func TestLaunchSpecEphemeralPortRequiresAndPassesReadyFile(t *testing.T) {
	spec := LaunchSpec{Executable: "mpythonrun.exe", BridgeScript: "bridge.py", RPCPort: 0}
	if _, err := spec.Args(); err == nil {
		t.Fatal("Args() error = nil, want ready-file requirement")
	}
	spec.ReadyFile = `C:\\private\\ready.json`
	args, err := spec.Args()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-f", "bridge.py", "-args", "--rpc-port", "0", "--session-mode", "cold", "--ready-file", `C:\\private\\ready.json`}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("Args() = %#v, want %#v", args, want)
	}
}
