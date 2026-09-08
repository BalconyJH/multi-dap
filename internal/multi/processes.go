package multi

import (
	"fmt"
	"regexp"
	"strings"
)

var processLine = regexp.MustCompile(`^\s*(>>)?\s*(\d+)\s+(0[xX][0-9a-fA-F]+)\s+(\S+)\s+(.+?)\s+([01]+)\s*(.*?)\s*$`)

// Process is one process slot reported by MULTI's P command. It is not a DAP
// thread: Debugger Core maps cores to threads independently.
type Process struct {
	Slot      uint64
	PID       uint64
	ParentPID uint64
	HasParent bool
	Status    Status
	Selected  bool
	Program   string
}

// ParseProcesses parses the P command output captured from MULTI 7.1.6d. Its
// whitespace columns are undocumented and pinned by testdata/processes.txt.
func ParseProcesses(output string) ([]Process, error) {
	var processes []Process
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" || strings.Contains(line, "# PID") {
			continue
		}
		match := processLine.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("parse processes: malformed row")
		}
		slot, ok := parseUint(match[2])
		if !ok {
			return nil, fmt.Errorf("parse processes: invalid slot")
		}
		pid, ok := parseUint(match[3])
		if !ok {
			return nil, fmt.Errorf("parse processes: invalid pid")
		}
		status, ok := parseProcessStatus(match[5])
		if !ok {
			return nil, fmt.Errorf("parse processes: unknown status")
		}
		process := Process{
			Slot:     slot,
			PID:      pid,
			Status:   status,
			Selected: match[1] != "",
			Program:  strings.TrimSpace(match[7]),
		}
		if match[4] != "N/A" {
			parentPID, ok := parseUint(match[4])
			if !ok {
				return nil, fmt.Errorf("parse processes: invalid parent pid")
			}
			process.ParentPID = parentPID
			process.HasParent = true
		}
		processes = append(processes, process)
	}
	if len(processes) == 0 {
		return nil, fmt.Errorf("parse processes: no rows")
	}
	return processes, nil
}

func parseProcessStatus(value string) (Status, bool) {
	switch strings.ToLower(strings.Join(strings.Fields(value), " ")) {
	case "nil":
		return StatusNil, true
	case "no process":
		return StatusNoProcess, true
	case "stopped":
		return StatusStopped, true
	case "running":
		return StatusRunning, true
	case "dying":
		return StatusDying, true
	case "forking":
		return StatusForking, true
	case "executing":
		return StatusExecuting, true
	case "continuing":
		return StatusContinuing, true
	case "zombie":
		return StatusZombie, true
	default:
		return StatusNil, false
	}
}
