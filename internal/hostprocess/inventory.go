// Package hostprocess provides advisory, read-only process snapshots.
// A matching path or PID is not evidence of ownership or probe availability.
package hostprocess

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	StateComplete    = "complete"
	StatePartial     = "partial"
	StateUnavailable = "unavailable"
	StateUnsupported = "unsupported"

	ScopeConfiguredInstallation = "configured_installation"
	MatchInstallationPath       = "installation_path"
	MatchKnownNameUnresolved    = "known_name_unresolved"

	maxProcesses = 256
	maxScanned   = 16384
	maxPathUnits = 32768
)

// Entry describes one observed process, not an owned or necessarily live one.
// ParentPID is snapshot metadata; the parent may already have exited or its PID
// may have been reused. ImagePath is private diagnostic data.
type Entry struct {
	PID        uint32 `json:"pid"`
	ParentPID  uint32 `json:"parent_pid"`
	Executable string `json:"executable"`
	ImagePath  string `json:"image_path,omitempty"`
	Match      string `json:"match"`
}

// Report separates snapshot coverage from the configured-installation matches.
// Scanned and Unresolved count all inspected snapshot entries, including ones
// whose executable identity cannot be established. An empty list never proves
// that a probe is unused or that no other MULTI installation is running.
type Report struct {
	State      string  `json:"state"`
	Scope      string  `json:"scope"`
	Scanned    int     `json:"scanned"`
	Unresolved int     `json:"unresolved"`
	Truncated  bool    `json:"truncated"`
	Reason     string  `json:"reason,omitempty"`
	Processes  []Entry `json:"processes"`
}

type snapshotEntry struct {
	pid       uint32
	parentPID uint32
	name      string
}

type snapshot interface {
	next() (snapshotEntry, error)
	image(snapshotEntry) (string, error)
	close() error
}

func emptyReport(state, reason string) Report {
	return Report{
		State: state, Scope: ScopeConfiguredInstallation, Reason: reason,
		Processes: []Entry{},
	}
}

func collectWith(ctx context.Context, installation string, open func() (snapshot, error)) (report Report) {
	report = emptyReport(StateComplete, "")
	root, ok := windowsPath(installation)
	if !ok {
		return emptyReport(StateUnavailable, "invalid_installation")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return emptyReport(StateUnavailable, "cancelled")
	}
	if open == nil {
		return emptyReport(StateUnavailable, "snapshot_failed")
	}
	processes, err := open()
	if err != nil || processes == nil {
		if processes != nil {
			_ = processes.close()
		}
		return emptyReport(StateUnavailable, "snapshot_failed")
	}
	defer func() {
		if processes.close() != nil {
			report.State = StatePartial
			if report.Reason == "" {
				report.Reason = "snapshot_close_failed"
			}
		}
		sort.Slice(report.Processes, func(i, j int) bool {
			return report.Processes[i].PID < report.Processes[j].PID
		})
	}()
	for report.Scanned < maxScanned {
		if ctx.Err() != nil {
			report.State, report.Reason = StatePartial, "cancelled"
			return report
		}
		row, err := processes.next()
		if errors.Is(err, io.EOF) {
			return report
		}
		if err != nil {
			report.State, report.Reason = StatePartial, "enumeration_failed"
			return report
		}
		report.Scanned++
		if ctx.Err() != nil {
			report.State, report.Reason = StatePartial, "cancelled"
			return report
		}
		image, err := processes.image(row)
		path, pathOK := windowsPath(image)
		nameOK := validBasename(row.name)
		if err != nil || !pathOK || !nameOK || !strings.EqualFold(pathBase(path), row.name) {
			report.Unresolved++
			report.State = StatePartial
			if report.Reason == "" {
				report.Reason = "image_unresolved"
			}
			if row.pid != 0 && knownName(row.name) {
				report.Processes = append(report.Processes, Entry{
					PID: row.pid, ParentPID: row.parentPID, Executable: row.name,
					Match: MatchKnownNameUnresolved,
				})
			}
		} else if row.pid != 0 && beneath(path, root) {
			report.Processes = append(report.Processes, Entry{
				PID: row.pid, ParentPID: row.parentPID, Executable: pathBase(path),
				ImagePath: image, Match: MatchInstallationPath,
			})
		}
		if len(report.Processes) == maxProcesses {
			report.State, report.Reason, report.Truncated = StatePartial, "entry_limit", true
			return report
		}
	}
	report.State, report.Reason, report.Truncated = StatePartial, "scan_limit", true
	return report
}

func beneath(path, root string) bool {
	root = strings.TrimSuffix(root, `\`)
	return len(path) > len(root)+1 && strings.EqualFold(path[:len(root)], root) && path[len(root)] == '\\'
}

func pathBase(path string) string {
	if index := strings.LastIndexByte(path, '\\'); index >= 0 {
		return path[index+1:]
	}
	return path
}

func validBasename(name string) bool {
	return name != "" && len(name) <= 1024 && !strings.ContainsAny(name, `\/:*?"<>|`) &&
		validText(name) && !strings.HasSuffix(name, ".") && !strings.HasSuffix(name, " ")
}

func validText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// windowsPath is lexical only. It does not follow junctions, query a UNC share,
// resolve short names, or turn a device namespace into an installation path.
func windowsPath(value string) (string, bool) {
	if value == "" || len(value) > maxPathUnits*4 || !validText(value) {
		return "", false
	}
	value = strings.ReplaceAll(value, "/", `\`)
	if len(value) >= 8 && strings.EqualFold(value[:8], `\\?\UNC\`) {
		value = `\\` + value[8:]
	} else if strings.HasPrefix(value, `\\?\`) {
		value = value[4:]
		if !driveAbsolute(value) {
			return "", false
		}
	}
	var prefix, rest string
	if driveAbsolute(value) {
		prefix, rest = value[:3], value[3:]
	} else if strings.HasPrefix(value, `\\`) {
		parts := strings.Split(value[2:], `\`)
		if len(parts) < 2 || !validBasename(parts[0]) || !validBasename(parts[1]) ||
			parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
			return "", false
		}
		prefix = `\\` + parts[0] + `\` + parts[1] + `\`
		rest = strings.Join(parts[2:], `\`)
	} else {
		return "", false
	}
	parts := make([]string, 0, 8)
	for _, part := range strings.Split(rest, `\`) {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) == 0 {
				return "", false
			}
			parts = parts[:len(parts)-1]
		default:
			if !validBasename(part) {
				return "", false
			}
			parts = append(parts, part)
		}
	}
	return prefix + strings.Join(parts, `\`), true
}

func driveAbsolute(value string) bool {
	return len(value) >= 3 && (value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z') &&
		value[1] == ':' && value[2] == '\\'
}

func knownName(name string) bool {
	switch strings.ToLower(name) {
	case "multi.exe", "mpythonrun.exe", "svc_answer.exe", "svc_debugcmd.exe",
		"svc_flash.exe", "svc_python.exe", "svc_python3.exe", "svc_recorder.exe",
		"svc_router.exe", "svc_statemgr.exe", "svc_vc_cc.exe", "svc_vc_cvs.exe",
		"svc_vc_svn.exe", "svc_window.exe", "tracegui.exe", "traceserv.exe":
		return true
	default:
		return false
	}
}
