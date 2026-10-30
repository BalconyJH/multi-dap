package hostprocess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type fakeImage struct {
	path string
	err  error
}

type fakeSnapshot struct {
	rows       []snapshotEntry
	images     map[uint32]fakeImage
	index      int
	nextErr    error
	closeErr   error
	closed     int
	queries    int
	beforeNext func()
	beforeRead func()
}

func (s *fakeSnapshot) next() (snapshotEntry, error) {
	if s.beforeNext != nil {
		s.beforeNext()
	}
	if s.index == len(s.rows) {
		if s.nextErr != nil {
			return snapshotEntry{}, s.nextErr
		}
		return snapshotEntry{}, io.EOF
	}
	row := s.rows[s.index]
	s.index++
	return row, nil
}

func (s *fakeSnapshot) image(row snapshotEntry) (string, error) {
	s.queries++
	if s.beforeRead != nil {
		s.beforeRead()
	}
	image := s.images[row.pid]
	return image.path, image.err
}

func (s *fakeSnapshot) close() error {
	s.closed++
	return s.closeErr
}

func runFake(ctx context.Context, root string, processes *fakeSnapshot) Report {
	return collectWith(ctx, root, func() (snapshot, error) { return processes, nil })
}

func TestInventorySelectionAndScope(t *testing.T) {
	processes := &fakeSnapshot{
		rows: []snapshotEntry{
			{pid: 20, parentPID: 7, name: "multi.exe"},
			{pid: 10, parentPID: 7, name: "svc_router.exe"},
			{pid: 30, parentPID: 7, name: "multi.exe"},
			{pid: 40, parentPID: 7, name: "application.exe"},
		},
		images: map[uint32]fakeImage{
			20: {path: `C:\GHS\MULTI\multi.exe`},
			10: {path: `\\?\C:\ghs\multi\services\svc_router.exe`},
			30: {path: `C:\ghs\multi-other\multi.exe`},
			40: {path: `C:\another\application.exe`},
		},
	}
	report := runFake(nil, `c:\ghs\multi\`, processes)
	if report.State != StateComplete || report.Scope != ScopeConfiguredInstallation ||
		report.Scanned != 4 || report.Unresolved != 0 || report.Truncated || report.Reason != "" {
		t.Fatalf("unexpected summary: %#v", report)
	}
	if len(report.Processes) != 2 || report.Processes[0].PID != 10 || report.Processes[1].PID != 20 {
		t.Fatalf("unexpected related PIDs: %#v", report.Processes)
	}
	for _, entry := range report.Processes {
		if entry.Match != MatchInstallationPath || entry.ParentPID != 7 || entry.ImagePath == "" {
			t.Fatalf("unexpected related entry: %#v", entry)
		}
	}
	if processes.closed != 1 || processes.queries != 4 {
		t.Fatalf("snapshot calls: close=%d image=%d", processes.closed, processes.queries)
	}
}

func TestInventoryUnresolvedNeverBecomesConfirmedOrLeaksErrors(t *testing.T) {
	secret := errors.New("private command-line token and target path")
	processes := &fakeSnapshot{
		rows: []snapshotEntry{
			{pid: 1, parentPID: 7, name: "MULTI.EXE"},
			{pid: 2, parentPID: 8, name: "unrelated.exe"},
			{pid: 3, parentPID: 9, name: "svc_router.exe"},
			{pid: 4, parentPID: 10, name: "mpythonrun.exe"},
			{pid: 0, parentPID: 0, name: "multi.exe"},
		},
		images: map[uint32]fakeImage{
			1: {path: `C:\private\secret.exe`, err: secret},
			2: {err: secret},
			3: {path: `C:\ghs\multi\changed.exe`},
			4: {path: `\Device\HarddiskVolume1\ghs\multi\mpythonrun.exe`},
			0: {err: secret},
		},
	}
	report := runFake(context.Background(), `C:\ghs\multi`, processes)
	if report.State != StatePartial || report.Scanned != 5 || report.Unresolved != 5 ||
		report.Reason != "image_unresolved" || report.Truncated || len(report.Processes) != 3 {
		t.Fatalf("unexpected unresolved result: %#v", report)
	}
	for _, entry := range report.Processes {
		if entry.Match != MatchKnownNameUnresolved || entry.ImagePath != "" || entry.PID == 0 {
			t.Fatalf("unresolved entry became confirmed: %#v", entry)
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, prohibited := range []string{"private", "token", "secret", "changed.exe", "unrelated.exe"} {
		if strings.Contains(string(encoded), prohibited) {
			t.Fatalf("report disclosed excluded data %q", prohibited)
		}
	}
}

func TestInventoryNoRelatedProcessesStillReportsUnresolvedCoverage(t *testing.T) {
	processes := &fakeSnapshot{
		rows:   []snapshotEntry{{pid: 3, name: "unrelated.exe"}},
		images: map[uint32]fakeImage{3: {err: errors.New("denied")}},
	}
	report := runFake(nil, `C:\ghs\multi`, processes)
	if report.State != StatePartial || report.Unresolved != 1 || len(report.Processes) != 0 || report.Processes == nil {
		t.Fatalf("partial empty report = %#v", report)
	}
}

func TestInventoryBounds(t *testing.T) {
	for _, related := range []bool{false, true} {
		name := "scan_limit"
		count := maxScanned + 2
		if related {
			name, count = "entry_limit", maxProcesses+2
		}
		t.Run(name, func(t *testing.T) {
			processes := &fakeSnapshot{images: make(map[uint32]fakeImage)}
			for index := 1; index <= count; index++ {
				pid := uint32(index)
				processes.rows = append(processes.rows, snapshotEntry{pid: pid, name: "multi.exe"})
				path := `C:\elsewhere\multi.exe`
				if related {
					path = `C:\ghs\multi\multi.exe`
				}
				processes.images[pid] = fakeImage{path: path}
			}
			report := runFake(nil, `C:\ghs\multi`, processes)
			if report.State != StatePartial || !report.Truncated || report.Reason != name ||
				report.Scanned != count-2 || processes.closed != 1 {
				t.Fatalf("bounded result: %#v; closed %d", report, processes.closed)
			}
			wantEntries := 0
			if related {
				wantEntries = maxProcesses
			}
			if len(report.Processes) != wantEntries || processes.queries != count-2 {
				t.Fatalf("bounds entries=%d queries=%d", len(report.Processes), processes.queries)
			}
		})
	}
}

func TestInventoryCancellationAndFailureClosure(t *testing.T) {
	t.Run("before_open", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		opened := false
		report := collectWith(ctx, `C:\ghs\multi`, func() (snapshot, error) {
			opened = true
			return nil, nil
		})
		if opened || report.State != StateUnavailable || report.Reason != "cancelled" {
			t.Fatalf("cancelled result: %#v opened=%v", report, opened)
		}
	})
	t.Run("between_enumeration_and_image", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		processes := &fakeSnapshot{rows: []snapshotEntry{{pid: 1, name: "multi.exe"}}, beforeNext: cancel}
		report := runFake(ctx, `C:\ghs\multi`, processes)
		if report.State != StatePartial || report.Reason != "cancelled" ||
			processes.queries != 0 || processes.closed != 1 {
			t.Fatalf("cancelled result: %#v queries=%d closed=%d", report, processes.queries, processes.closed)
		}
	})
	t.Run("between_image_and_next", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		processes := &fakeSnapshot{
			rows:       []snapshotEntry{{pid: 1, name: "multi.exe"}, {pid: 2, name: "multi.exe"}},
			images:     map[uint32]fakeImage{1: {path: `C:\ghs\multi\multi.exe`}},
			beforeRead: cancel,
		}
		report := runFake(ctx, `C:\ghs\multi`, processes)
		if report.State != StatePartial || report.Reason != "cancelled" ||
			processes.queries != 1 || processes.closed != 1 || report.Scanned != 1 {
			t.Fatalf("cancelled result: %#v", report)
		}
	})
	t.Run("enumeration_error", func(t *testing.T) {
		processes := &fakeSnapshot{nextErr: errors.New("private system diagnostic")}
		report := runFake(nil, `C:\ghs\multi`, processes)
		if report.State != StatePartial || report.Reason != "enumeration_failed" || processes.closed != 1 {
			t.Fatalf("enumeration result: %#v", report)
		}
	})
	t.Run("close_error", func(t *testing.T) {
		processes := &fakeSnapshot{closeErr: errors.New("private system diagnostic")}
		report := runFake(nil, `C:\ghs\multi`, processes)
		if report.State != StatePartial || report.Reason != "snapshot_close_failed" || processes.closed != 1 {
			t.Fatalf("close result: %#v", report)
		}
	})
	t.Run("open_error", func(t *testing.T) {
		report := collectWith(nil, `C:\ghs\multi`, func() (snapshot, error) {
			return nil, errors.New("private system diagnostic")
		})
		if report.State != StateUnavailable || report.Reason != "snapshot_failed" || report.Processes == nil {
			t.Fatalf("open result: %#v", report)
		}
	})
	t.Run("partial_open_error", func(t *testing.T) {
		processes := &fakeSnapshot{}
		report := collectWith(nil, `C:\ghs\multi`, func() (snapshot, error) {
			return processes, errors.New("private system diagnostic")
		})
		if report.State != StateUnavailable || report.Reason != "snapshot_failed" || processes.closed != 1 {
			t.Fatalf("partial open result: %#v; closed %d", report, processes.closed)
		}
	})
}

func TestInventoryWindowsPathPolicy(t *testing.T) {
	tests := []struct {
		name  string
		root  string
		image string
		match bool
	}{
		{"drive", `C:\ghs\multi`, `c:/GHS/multi/multi.exe`, true},
		{"drive_root", `C:\`, `C:\multi.exe`, true},
		{"same_path", `C:\ghs\multi`, `C:\ghs\multi`, false},
		{"sibling_prefix", `C:\ghs\multi`, `C:\ghs\multi2\multi.exe`, false},
		{"traversal", `C:\ghs\multi`, `C:\ghs\multi\..\other\multi.exe`, false},
		{"cleaned_root", `C:\ghs\.\multi\sub\..`, `C:\ghs\multi\multi.exe`, true},
		{"extended_drive", `\\?\C:\ghs\multi`, `\\?\C:\ghs\multi\multi.exe`, true},
		{"unc", `\\server\share\multi`, `\\SERVER\share\multi\multi.exe`, true},
		{"unc_share_root", `\\server\share`, `\\server\share\multi.exe`, true},
		{"unc_extended", `\\server\share\multi`, `\\?\UNC\server\share\multi\multi.exe`, true},
		{"unc_other_share", `\\server\share`, `\\server\share2\multi.exe`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, rootOK := windowsPath(test.root)
			image, imageOK := windowsPath(test.image)
			if !rootOK || !imageOK || beneath(image, root) != test.match {
				t.Fatalf("path result root=%q/%v image=%q/%v", root, rootOK, image, imageOK)
			}
		})
	}
	for _, path := range []string{
		"", "C:relative", `\root-relative`, "/posix/path", `\\server`, `\\server\`,
		`\\.\C:\multi`, `\\?\GLOBALROOT\Device\HarddiskVolume1\multi`,
		`\??\C:\multi`, `\\?\Volume{abc}\multi`, `C:\..\multi`, `\\server\share\..\multi`,
		`C:\multi.`, `C:\multi `, `C:\multi:stream`, `C:\mul?ti`, "C:\\multi\x00other",
		"C:\\multi\nother", "C:\\multi\u202eother", "C:\\multi\xff",
	} {
		t.Run("invalid_"+strings.ReplaceAll(path, `\`, "_"), func(t *testing.T) {
			if normalized, ok := windowsPath(path); ok {
				t.Fatalf("accepted ambiguous path %q as %q", path, normalized)
			}
			opened := false
			report := collectWith(nil, path, func() (snapshot, error) {
				opened = true
				return nil, nil
			})
			if opened || report.State != StateUnavailable || report.Reason != "invalid_installation" {
				t.Fatalf("invalid root result %#v opened=%v", report, opened)
			}
		})
	}
}
