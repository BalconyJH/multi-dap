package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/hostprocess"
)

const (
	processInventoryTimeout = 2 * time.Second
	processInventoryMaxJSON = 1 << 20
)

var collectHostProcesses = hostprocess.Collect

type processInventoryMessage struct {
	Schema                    string             `json:"schema"`
	Event                     string             `json:"event"`
	ConfigChecked             bool               `json:"config_checked"`
	PathsIncluded             bool               `json:"paths_included"`
	OwnershipVerified         bool               `json:"ownership_verified"`
	ProbeAvailabilityVerified bool               `json:"probe_availability_verified"`
	Inventory                 hostprocess.Report `json:"inventory"`
}

type startupProcessMessage struct {
	Event                     string `json:"event"`
	State                     string `json:"state"`
	Scanned                   int    `json:"scanned"`
	Related                   int    `json:"related"`
	Unresolved                int    `json:"unresolved"`
	Truncated                 bool   `json:"truncated"`
	OwnershipVerified         bool   `json:"ownership_verified"`
	ProbeAvailabilityVerified bool   `json:"probe_availability_verified"`
	Hint                      string `json:"hint"`
}

func runDoctor(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "project TOML configuration")
	processes := flags.Bool("processes", false, "observe related host processes without contacting MULTI")
	includePaths := flags.Bool("include-paths", false, "include private executable paths in the explicit process report")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError("doctor accepts no positional arguments")
	}
	if *includePaths && !*processes {
		return usageError("--include-paths requires --processes")
	}
	if !*processes {
		return runCheck([]string{"--config", *configPath}, stdout)
	}
	validated, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	report, err := observeHostProcesses(ctx, validated.Multi.Installation)
	if err != nil {
		return err
	}
	// Copy before redaction so an injected or reused collector result cannot
	// lose its private data as a side effect of this presentation decision.
	report.Processes = append([]hostprocess.Entry{}, report.Processes...)
	if !*includePaths {
		for index := range report.Processes {
			report.Processes[index].ImagePath = ""
		}
	}
	data, err := json.Marshal(processInventoryMessage{
		Schema: "multi-dap.host-processes.v1", Event: "process_inventory",
		ConfigChecked: true, PathsIncluded: *includePaths, Inventory: report,
	})
	if err != nil || len(data)+1 > processInventoryMaxJSON {
		return errors.New("process inventory report exceeds its output limit")
	}
	data = append(data, '\n')
	if written, err := stdout.Write(data); err != nil || written != len(data) {
		return errors.New("write process inventory report failed")
	}
	return nil
}

func observeHostProcesses(ctx context.Context, installation string) (hostprocess.Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return hostprocess.Report{}, err
	}
	observation, cancel := context.WithTimeout(ctx, processInventoryTimeout)
	defer cancel()
	// Local OS queries are not cancellable. The collector checks this deadline
	// between queries; no abandoned worker is left running after a timeout.
	report := collectHostProcesses(observation, installation)
	if err := ctx.Err(); err != nil {
		return hostprocess.Report{}, err
	}
	return report, nil
}

func reportStartupProcesses(ctx context.Context, installation string, stderr io.Writer) error {
	report, err := observeHostProcesses(ctx, installation)
	if err != nil {
		return err
	}
	if report.State == hostprocess.StateComplete && len(report.Processes) == 0 &&
		report.Unresolved == 0 && !report.Truncated {
		return nil
	}
	state := report.State
	switch state {
	case hostprocess.StateComplete, hostprocess.StatePartial, hostprocess.StateUnavailable, hostprocess.StateUnsupported:
	default:
		state = hostprocess.StateUnavailable
	}
	notice := startupProcessMessage{
		Event: "host_process_notice", State: state, Scanned: report.Scanned,
		Related: len(report.Processes), Unresolved: report.Unresolved, Truncated: report.Truncated,
		Hint: "Inspect with doctor --processes; observations do not establish process ownership or probe availability.",
	}
	data, err := json.Marshal(notice)
	if err != nil {
		return errors.New("encode host process notice failed")
	}
	data = append(data, '\n')
	if written, err := stderr.Write(data); err != nil || written != len(data) {
		return errors.New("write host process notice failed")
	}
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}
