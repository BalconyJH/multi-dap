package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TerminalPhase identifies the daemon lifecycle boundary that ended. It is
// deliberately a closed vocabulary: diagnostic records must not become an
// unbounded carrier for backend details.
type TerminalPhase string

const (
	TerminalPhaseRuntimeStart TerminalPhase = "runtime_start"
	TerminalPhaseRuntimeServe TerminalPhase = "runtime_serve"
)

// TerminalClassification is a safe, stable terminal-result category. It
// never contains a raw MULTI diagnostic, connection argument, or filesystem
// path.
type TerminalClassification string

const (
	TerminalContextDeadline TerminalClassification = "context_deadline"
	TerminalBridgeExit      TerminalClassification = "bridge_exit"
	TerminalActorFault      TerminalClassification = "actor_fault"
	TerminalRuntimeClosed   TerminalClassification = "runtime_closed"
	TerminalOther           TerminalClassification = "other"
)

// TerminalOperation is the optional actor work category supplied with an
// actor fault. It is empty when no actor fault produced the terminal result.
type TerminalOperation string

const (
	TerminalOperationUnknown           TerminalOperation = "unknown"
	TerminalOperationOpen              TerminalOperation = "open"
	TerminalOperationCores             TerminalOperation = "cores"
	TerminalOperationState             TerminalOperation = "state"
	TerminalOperationExecution         TerminalOperation = "execution"
	TerminalOperationBreakpoints       TerminalOperation = "breakpoints"
	TerminalOperationBreakpointCleanup TerminalOperation = "breakpoint-cleanup"
	TerminalOperationInspection        TerminalOperation = "inspection"
	TerminalOperationRollbackClose     TerminalOperation = "rollback-close"
)

// TerminalRecord is private, last-terminal metadata for one probe. InstanceID
// associates the record with the daemon that produced it, but callers need not
// expose it outside the owner-only control directory.
type TerminalRecord struct {
	Version        int                    `json:"version"`
	ProbeID        string                 `json:"probe_id"`
	ConfigDigest   string                 `json:"config_digest"`
	InstanceID     string                 `json:"instance_id"`
	PID            int                    `json:"pid"`
	Phase          TerminalPhase          `json:"phase"`
	Classification TerminalClassification `json:"classification"`
	Operation      TerminalOperation      `json:"operation,omitempty"`
	OccurredAt     time.Time              `json:"occurred_at"`
}

// LoadTerminal returns the most recent committed daemon terminal result for a
// probe. It is independent from the live-record lifecycle, so a daemon exit
// cannot erase the evidence needed to diagnose that exit.
func (s *Store) LoadTerminal(probeID string) (TerminalRecord, error) {
	path, err := s.terminalPath(probeID)
	if err != nil {
		return TerminalRecord{}, ErrTerminalRecordUnavailable
	}
	data, err := readStoreFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return TerminalRecord{}, fmt.Errorf("%w %q", ErrNoTerminalRecord, probeID)
	}
	if err != nil {
		return TerminalRecord{}, ErrTerminalRecordUnavailable
	}
	record, err := decodeTerminalRecord(data, probeID)
	if err != nil {
		return TerminalRecord{}, ErrTerminalRecordUnavailable
	}
	return record, nil
}

// LoadTerminalForConfig returns terminal evidence only when it belongs to the
// requested configuration. It never exposes a different configuration's
// diagnostic result to a config-bound caller.
func (s *Store) LoadTerminalForConfig(probeID, digest string) (TerminalRecord, error) {
	if !validConfigDigest(digest) {
		return TerminalRecord{}, ErrConfigurationMismatch
	}
	path, err := s.terminalPath(probeID)
	if err != nil {
		return TerminalRecord{}, ErrTerminalRecordUnavailable
	}
	data, err := readStoreFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return TerminalRecord{}, fmt.Errorf("%w %q", ErrNoTerminalRecord, probeID)
	}
	if err != nil {
		return TerminalRecord{}, ErrTerminalRecordUnavailable
	}
	record, err := decodeTerminalRecord(data, probeID)
	if err != nil {
		// An old or malformed private record cannot establish an exact
		// configuration binding; no terminal detail is released to the caller.
		return TerminalRecord{}, ErrConfigurationMismatch
	}
	if record.ConfigDigest != digest {
		return TerminalRecord{}, ErrConfigurationMismatch
	}
	return record, nil
}

// WriteTerminal atomically replaces the prior terminal result with one from a
// committed daemon instance. It does not touch the live daemon record, and no
// daemon startup clears a previous instance's terminal evidence.
func (s *Store) WriteTerminal(record TerminalRecord) error {
	if err := validateTerminalRecord(record, record.ProbeID); err != nil {
		return err
	}
	path, err := s.terminalPath(record.ProbeID)
	if err != nil {
		return ErrTerminalRecordWrite
	}
	data, err := json.Marshal(record)
	if err != nil {
		return ErrTerminalRecordWrite
	}
	if len(data) > MaxMessageSize {
		return ErrTerminalRecordWrite
	}
	if err := writePrivateRecord(path, ".multi-dap-terminal-*", data, "terminal"); err != nil {
		return ErrTerminalRecordWrite
	}
	return nil
}

func (s *Store) terminalPath(probeID string) (string, error) {
	path, err := s.recordPath(probeID)
	if err != nil {
		return "", err
	}
	return path[:len(path)-len(".json")] + ".terminal.json", nil
}

func decodeTerminalRecord(data []byte, probeID string) (TerminalRecord, error) {
	if len(data) > MaxMessageSize {
		return TerminalRecord{}, errors.New("control: terminal record exceeds size limit")
	}
	fields, err := strictObject(data, map[string]struct{}{
		"version": {}, "probe_id": {}, "config_digest": {}, "instance_id": {}, "pid": {}, "phase": {}, "classification": {}, "operation": {}, "occurred_at": {},
	})
	if err != nil || len(fields) < 8 || len(fields) > 9 {
		return TerminalRecord{}, errors.New("control: invalid terminal record schema")
	}
	var record TerminalRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return TerminalRecord{}, fmt.Errorf("control: decode terminal record: %w", err)
	}
	if err := validateTerminalRecord(record, probeID); err != nil {
		return TerminalRecord{}, err
	}
	return record, nil
}

func validateTerminalRecord(record TerminalRecord, probeID string) error {
	if record.Version != ProtocolVersion || record.ProbeID != probeID || !validConfigDigest(record.ConfigDigest) || record.PID <= 0 || !validSecret(record.InstanceID) || record.OccurredAt.IsZero() {
		return errors.New("control: invalid terminal record")
	}
	if record.Phase != TerminalPhaseRuntimeStart && record.Phase != TerminalPhaseRuntimeServe {
		return errors.New("control: invalid terminal record phase")
	}
	switch record.Classification {
	case TerminalContextDeadline, TerminalBridgeExit, TerminalActorFault, TerminalRuntimeClosed, TerminalOther:
	default:
		return errors.New("control: invalid terminal record classification")
	}
	switch record.Operation {
	case "", TerminalOperationUnknown, TerminalOperationOpen, TerminalOperationCores, TerminalOperationState,
		TerminalOperationExecution, TerminalOperationBreakpoints, TerminalOperationBreakpointCleanup,
		TerminalOperationInspection, TerminalOperationRollbackClose:
		return nil
	default:
		return errors.New("control: invalid terminal record operation")
	}
}

func writePrivateRecord(path, pattern string, data []byte, label string) error {
	file, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return fmt.Errorf("control: create %s record: %w", label, err)
	}
	temporary := file.Name()
	replaced := false
	defer func() {
		if !replaced {
			_ = os.Remove(temporary)
		}
	}()
	if err := secureFile(temporary); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("control: write %s record: %w", label, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("control: close %s record: %w", label, err)
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(temporary, path); err != nil {
			return fmt.Errorf("control: publish %s record: %w", label, err)
		}
	} else if err != nil {
		return fmt.Errorf("control: inspect %s record: %w", label, err)
	} else if err := replaceFile(temporary, path); err != nil {
		return fmt.Errorf("control: publish %s record: %w", label, err)
	}
	replaced = true
	return nil
}
