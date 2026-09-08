package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/control"
	"github.com/Tacrolimus/multi-dap/internal/daemon"
)

type ensureChild interface {
	PID() int
	Done() <-chan error
	Kill() error
}

var launchEnsureServe = startEnsureServe
var prepareEnsureConsole = prepareBridgeConsole
var discoverWarmSession = daemon.DiscoverWarmSession

type ensureMessage struct {
	Event      string `json:"event"`
	DAPAddress string `json:"dap_address"`
	PID        int    `json:"pid"`
}

type ensureAcquisition string

const (
	ensureAcquisitionWarm ensureAcquisition = "warm"
	ensureAcquisitionCold ensureAcquisition = "cold"
)

type ensureServeRequest struct {
	ConfigPath   string
	BridgeScript string
	ControlDir   string
	ConfigDigest string
	Acquisition  ensureAcquisition
	Warm         *daemon.WarmSession
}

func runEnsure(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("ensure", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "project TOML configuration")
	bridgeScript := flags.String("bridge-script", "", "bridge.py path")
	controlDir := flags.String("control-dir", "", "private daemon control record directory")
	acquisition := flags.String("acquisition", string(ensureAcquisitionWarm), "session acquisition mode: warm or cold")
	primaryELF := flags.String("primary-elf", "", "warm session primary ELF identity")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError("ensure accepts no positional arguments")
	}
	validated, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	digest, err := config.BindingDigest(validated)
	if err != nil {
		return fmt.Errorf("configuration binding: %w", err)
	}
	script, err := resolveBridgeScript(*bridgeScript)
	if err != nil {
		return fmt.Errorf("bridge script: %w", err)
	}
	mode, primary, err := ensureAcquisitionOptions(*acquisition, *primaryELF)
	if err != nil {
		return err
	}
	store, err := control.NewStore(*controlDir)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	startup, cancel := context.WithTimeout(ctx, validated.Timing.StartupDeadline)
	defer cancel()

	if status, found, _, err := waitForLiveDaemon(startup, store, validated.Probe.ID, digest, 0, nil); err != nil {
		return err
	} else if found {
		return writeEnsure(stdout, "already_ready", status)
	}
	var warm *daemon.WarmSession
	if mode == ensureAcquisitionWarm {
		if err := prepareEnsureConsole(); err != nil {
			return err
		}
		discovered, err := discoverWarmSession(startup, validated, script, primary)
		if err != nil {
			return fmt.Errorf("discover warm MULTI session: %w", err)
		}
		warm = &discovered
	}
	child, err := launchEnsureServe(ensureServeRequest{
		ConfigPath: *configPath, BridgeScript: script, ControlDir: *controlDir,
		ConfigDigest: digest, Acquisition: mode, Warm: warm,
	})
	if err != nil {
		return fmt.Errorf("start daemon process: %w", err)
	}
	ready := false
	defer func() {
		if !ready {
			_ = child.Kill() // This is the exact child we created, never a MULTI process.
		}
	}()
	status, found, owned, err := waitForLiveDaemon(startup, store, validated.Probe.ID, digest, child.PID(), child.Done())
	if err != nil {
		return err
	}
	if !found {
		return errors.New("control: daemon did not publish an authenticated ready record")
	}
	ready = true
	if !owned {
		return writeEnsure(stdout, "already_ready", status)
	}
	return writeEnsure(stdout, "ready", status)
}

func ensureAcquisitionOptions(value, primaryELF string) (ensureAcquisition, string, error) {
	switch mode := ensureAcquisition(strings.TrimSpace(value)); mode {
	case ensureAcquisitionWarm:
		primary, err := absoluteRegularFile(primaryELF)
		if err != nil {
			return "", "", fmt.Errorf("primary ELF: %w", err)
		}
		return mode, primary, nil
	case ensureAcquisitionCold:
		if strings.TrimSpace(primaryELF) != "" {
			return "", "", usageError("--primary-elf requires --acquisition warm")
		}
		return mode, "", nil
	default:
		return "", "", usageError("--acquisition must be warm or cold")
	}
}

func (r ensureServeRequest) args() ([]string, error) {
	args := []string{"serve", "--ensure-child", "--config", r.ConfigPath, "--bridge-script", r.BridgeScript}
	if strings.TrimSpace(r.ConfigDigest) == "" {
		return nil, errors.New("ensure child requires configuration binding")
	}
	args = append(args, "--ensure-config-digest", r.ConfigDigest)
	if r.ControlDir != "" {
		args = append(args, "--control-dir", r.ControlDir)
	}
	switch r.Acquisition {
	case ensureAcquisitionCold:
		if r.Warm != nil {
			return nil, errors.New("cold ensure child received warm session identity")
		}
		return append(args, "--session-mode", "cold"), nil
	case ensureAcquisitionWarm:
		if r.Warm == nil {
			return nil, errors.New("warm ensure child requires session identity")
		}
		return append(args,
			"--session-mode", "warm",
			"--service-router-host", r.Warm.ServiceRouterHost,
			"--service-router-port", strconv.Itoa(r.Warm.ServiceRouterPort),
			"--primary-elf", r.Warm.PrimaryELF,
		), nil
	default:
		return nil, errors.New("ensure child received unknown acquisition mode")
	}
}

func writeEnsure(stdout io.Writer, event string, status control.Status) error {
	if err := json.NewEncoder(stdout).Encode(ensureMessage{Event: event, DAPAddress: status.DAPAddress, PID: status.PID}); err != nil {
		return fmt.Errorf("write ensure result: %w", err)
	}
	return nil
}

// waitForLiveDaemon observes only a complete record that authenticates over
// the control connection. childPID is evidence about our own child, never a
// criterion for trusting a record owned by another successful starter.
func waitForLiveDaemon(ctx context.Context, store *control.Store, probeID, digest string, childPID int, childDone <-chan error) (control.Status, bool, bool, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var childExit error
	// A competing child can lose Reserve and exit before the winner replaces
	// its starting marker. Once that live marker was observed, our child's
	// exit is diagnostic only: readiness still belongs to the authenticated
	// record eventually published by the other starter.
	waitingForOtherStarter := false
	for {
		record, err := store.LoadForConfig(probeID, digest)
		switch {
		case err == nil:
			status, callErr := control.Call(ctx, record, control.MethodStatus)
			if callErr == nil {
				return status, true, childPID != 0 && record.PID == childPID, nil
			}
			if !errors.Is(callErr, control.ErrStaleRecord) {
				return control.Status{}, false, false, callErr
			}
			if err := store.RemoveIfEqual(record); err != nil {
				return control.Status{}, false, false, err
			}
		case errors.Is(err, control.ErrNoRecord):
			if childExit != nil && !waitingForOtherStarter {
				return control.Status{}, false, false, childExit
			}
			// The pre-launch probe has no child identity and may report a clean
			// miss. Once a child was launched, a missing record is transient
			// evidence until readiness, exact dead-owner recovery, or deadline.
			if childDone == nil && childPID == 0 {
				return control.Status{}, false, false, nil
			}
		case errors.Is(err, control.ErrRecordActive):
			recovered, recoverErr := store.RecoverStarting(probeID, digest)
			if recoverErr != nil {
				return control.Status{}, false, false, recoverErr
			}
			if recovered {
				// Exact dead-owner recovery proves no competing startup can
				// publish the record. A captured child failure is final again.
				waitingForOtherStarter = false
				continue
			}
			waitingForOtherStarter = true
		case errors.Is(err, control.ErrRecordTransition):
			// ReplaceFileW can briefly reject a new reader while atomically
			// replacing the starting marker. The authenticated record remains
			// the only readiness authority, so keep observing it.
		default:
			return control.Status{}, false, false, err
		}
		if childDone != nil {
			select {
			case childErr := <-childDone:
				if childErr == nil {
					childExit = errors.New("daemon process exited before readiness")
				} else {
					childExit = fmt.Errorf("daemon process exited before readiness: %w", childErr)
				}
				childDone = nil
			default:
			}
		}
		select {
		case <-ctx.Done():
			if childExit != nil {
				return control.Status{}, false, false, fmt.Errorf("%w (competing daemon readiness wait ended: %v)", childExit, ctx.Err())
			}
			return control.Status{}, false, false, ctx.Err()
		case <-ticker.C:
		}
	}
}
