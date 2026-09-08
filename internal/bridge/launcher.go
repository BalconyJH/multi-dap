package bridge

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// ServiceRouter identifies the optional MULTI service-router connection
// handed to mpythonrun. Host and Port are an atomic pair: passing only one
// would produce an invocation whose meaning depends on MULTI defaults.
type ServiceRouter struct {
	Host string
	Port int
}

// SessionMode states whether the bridge creates a new MULTI program session
// or binds an existing program window in the service router supplied to
// mpythonrun. It deliberately lives at the launcher boundary: bridge.py only
// receives the resulting typed MBP open request and never chooses a router.
type SessionMode string

const (
	SessionModeCold SessionMode = "cold"
	SessionModeWarm SessionMode = "warm"
)

// LaunchSpec is the platform-independent command contract for mpythonrun.
// It is intentionally separate from process creation so its argument ordering
// can be tested without starting MULTI or requiring hardware.
type LaunchSpec struct {
	Executable   string
	BridgeScript string
	RPCPort      int
	RPCHost      string
	// ReadyFile is the private bridge startup rendezvous path. Start replaces
	// any caller-supplied value with an owned temporary path.
	ReadyFile     string
	ServiceRouter *ServiceRouter
	// SessionMode defaults to cold for compatibility with the original launch
	// contract. A warm launch is invalid without a live service-router pair.
	SessionMode SessionMode
}

// Args returns the exact mpythonrun argument sequence required by
// architecture.md §7.1.
func (s LaunchSpec) Args() ([]string, error) {
	if strings.TrimSpace(s.Executable) == "" {
		return nil, errors.New("bridge: launcher executable is required")
	}
	if strings.TrimSpace(s.BridgeScript) == "" {
		return nil, errors.New("bridge: launcher bridge script is required")
	}
	if s.RPCPort < 0 || s.RPCPort > 65535 {
		return nil, fmt.Errorf("bridge: launcher RPC port %d is outside 0..65535", s.RPCPort)
	}
	if s.RPCPort == 0 && strings.TrimSpace(s.ReadyFile) == "" {
		return nil, errors.New("bridge: launcher RPC port 0 requires a ready file")
	}

	mode := s.SessionMode
	if mode == "" {
		mode = SessionModeCold
	}
	if mode != SessionModeCold && mode != SessionModeWarm {
		return nil, fmt.Errorf("bridge: unknown session mode %q", mode)
	}
	if mode == SessionModeWarm && s.ServiceRouter == nil {
		return nil, errors.New("bridge: warm launch requires a service router")
	}

	args := make([]string, 0, 15)
	if s.ServiceRouter != nil {
		host := strings.TrimSpace(s.ServiceRouter.Host)
		address, err := netip.ParseAddr(host)
		if err != nil || !address.IsLoopback() {
			return nil, errors.New("bridge: service router host must be a loopback IP literal")
		}
		if s.ServiceRouter.Port < 1 || s.ServiceRouter.Port > 65535 {
			return nil, errors.New("bridge: service router port must be in 1..65535")
		}
		args = append(args,
			"-sr_connect_servicerouter_host", host,
			"-sr_connect_servicerouter_port", strconv.Itoa(s.ServiceRouter.Port),
		)
	}
	var rpcHost string
	if s.RPCHost != "" {
		host := strings.TrimSpace(s.RPCHost)
		address, err := netip.ParseAddr(host)
		if err != nil || !address.IsLoopback() {
			return nil, errors.New("bridge: RPC host must be a loopback IP literal")
		}
		rpcHost = address.String()
	}
	args = append(args,
		"-f", s.BridgeScript, "-args",
		"--rpc-port", strconv.Itoa(s.RPCPort),
		"--session-mode", string(mode),
	)
	if rpcHost != "" {
		args = append(args, "--rpc-host", rpcHost)
	}
	if readyFile := strings.TrimSpace(s.ReadyFile); readyFile != "" {
		args = append(args, "--ready-file", readyFile)
	}
	return args, nil
}
