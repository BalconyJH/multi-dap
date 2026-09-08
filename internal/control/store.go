package control

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
)

var (
	ErrNoRecord     = errors.New("control: no daemon record for probe")
	ErrRecordActive = errors.New("control: daemon record is already live")
	// ErrRecordTransition is a transient observation of an atomic record
	// publication. Callers that are already waiting for daemon readiness may
	// retry; one-shot control commands must not treat it as a valid record.
	ErrRecordTransition = errors.New("control: daemon record publication in progress")
	// ErrConfigurationMismatch deliberately provides no metadata about either
	// configuration. Callers using a configuration-bound command must not use a
	// same-probe record that was created for another configuration.
	ErrConfigurationMismatch = errors.New("control: daemon configuration does not match")
	ErrNoTerminalRecord      = errors.New("control: no terminal record for probe")
	// ErrTerminalRecordUnavailable intentionally hides filesystem and record
	// parsing detail from the diagnose command's stderr surface.
	ErrTerminalRecordUnavailable = errors.New("control: terminal record unavailable")
	// ErrTerminalRecordWrite intentionally hides filesystem publication detail
	// from a daemon that is already reporting an abnormal termination.
	ErrTerminalRecordWrite = errors.New("control: terminal record write failed")
)

// Record is private rendezvous metadata. Token is intentionally never
// written to stdout and never returned by the control protocol.
type Record struct {
	Version int    `json:"version"`
	ProbeID string `json:"probe_id"`
	// ConfigDigest is private owner-only rendezvous metadata, not control-wire
	// state. It binds this record to one normalized configuration.
	ConfigDigest   string `json:"config_digest"`
	InstanceID     string `json:"instance_id"`
	PID            int    `json:"pid"`
	ControlAddress string `json:"control_address"`
	DAPAddress     string `json:"dap_address"`
	Token          string `json:"token"`
}

// Store owns the user-private record directory. It has no process cleanup
// behavior: a stale record is evidence, not authority to kill anything.
type Store struct{ directory string }

// Reservation owns the final record path while a daemon is starting. It is a
// one-shot transaction: Commit replaces its private starting marker with the
// complete record, and Cancel removes only that marker. A reservation is not
// a record and must never be treated as stale evidence by another process.
type Reservation struct {
	store   *Store
	probeID string
	digest  string
	marker  []byte
	done    bool
	mu      sync.Mutex
}

type startingRecord struct {
	Version      int    `json:"version"`
	State        string `json:"state"`
	ConfigDigest string `json:"config_digest"`
	InstanceID   string `json:"instance_id"`
	PID          int    `json:"pid"`
}

func NewStore(directory string) (*Store, error) {
	if directory == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("control: resolve user config directory: %w", err)
		}
		directory = filepath.Join(base, "multi-dap", "control")
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("control: resolve record directory: %w", err)
	}
	if err := secureDirectory(abs); err != nil {
		return nil, err
	}
	return &Store{directory: filepath.Clean(abs)}, nil
}

func (s *Store) recordPath(probeID string) (string, error) {
	if s == nil || s.directory == "" {
		return "", errors.New("control: record store is required")
	}
	if probeID == "" {
		return "", errors.New("control: probe ID is required")
	}
	digest := sha256.Sum256([]byte(probeID))
	return filepath.Join(s.directory, fmt.Sprintf("%x.json", digest[:])), nil
}

func readStoreFile(path string) ([]byte, error) {
	file, err := openStoreFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func (s *Store) Load(probeID string) (Record, error) {
	path, err := s.recordPath(probeID)
	if err != nil {
		return Record{}, err
	}
	data, err := readStoreFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, fmt.Errorf("%w %q", ErrNoRecord, probeID)
	}
	if isTransientStoreReadError(err) {
		return Record{}, ErrRecordTransition
	}
	if err != nil {
		return Record{}, fmt.Errorf("control: read daemon record: %w", err)
	}
	if len(data) > MaxMessageSize {
		return Record{}, errors.New("control: daemon record exceeds size limit")
	}
	if _, ok := decodeStartingRecord(data); ok {
		return Record{}, ErrRecordActive
	}
	record, err := decodeStoredRecord(data, probeID)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

// LoadForConfig returns only a live-record candidate bound to digest. It is
// intentionally separate from Load, which remains the explicit legacy,
// identity-only escape hatch.
func (s *Store) LoadForConfig(probeID, digest string) (Record, error) {
	if !validConfigDigest(digest) {
		return Record{}, ErrConfigurationMismatch
	}
	record, err := s.Load(probeID)
	if err != nil {
		if !errors.Is(err, ErrNoRecord) && !errors.Is(err, ErrRecordActive) && !errors.Is(err, ErrRecordTransition) {
			// A legacy or malformed private record cannot prove an exact
			// configuration binding. Do not allow a config-bound caller to
			// continue toward an authenticated control call.
			return Record{}, ErrConfigurationMismatch
		}
		return Record{}, err
	}
	if record.ConfigDigest != digest {
		return Record{}, ErrConfigurationMismatch
	}
	return record, nil
}

func decodeStoredRecord(data []byte, probeID string) (Record, error) {
	if len(data) > MaxMessageSize {
		return Record{}, errors.New("control: daemon record exceeds size limit")
	}
	fields, err := strictObject(data, map[string]struct{}{
		"version": {}, "probe_id": {}, "config_digest": {}, "instance_id": {}, "pid": {}, "control_address": {}, "dap_address": {}, "token": {},
	})
	if err != nil || len(fields) != 8 {
		return Record{}, errors.New("control: invalid daemon record schema")
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, fmt.Errorf("control: decode daemon record: %w", err)
	}
	if err := validateRecord(record, probeID); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Store) Publish(record Record) error {
	if err := validateRecord(record, record.ProbeID); err != nil {
		return err
	}
	path, err := s.recordPath(record.ProbeID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("control: encode daemon record: %w", err)
	}
	if len(data) > MaxMessageSize {
		return errors.New("control: daemon record exceeds size limit")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrRecordActive
		}
		return fmt.Errorf("control: create daemon record: %w", err)
	}
	name := file.Name()
	if err := secureFile(name); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return fmt.Errorf("control: write daemon record: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("control: close daemon record: %w", err)
	}
	return nil
}

// Reserve atomically claims the final record path before daemon startup. The
// marker is schema-distinct from a record so concurrent control commands see
// an active startup instead of deleting it as stale state.
func (s *Store) Reserve(probeID, digest string) (*Reservation, error) {
	if !validConfigDigest(digest) {
		return nil, ErrConfigurationMismatch
	}
	path, err := s.recordPath(probeID)
	if err != nil {
		return nil, err
	}
	instanceID, err := randomToken()
	if err != nil {
		return nil, err
	}
	marker, err := json.Marshal(startingRecord{Version: ProtocolVersion, State: "starting", ConfigDigest: digest, InstanceID: instanceID, PID: os.Getpid()})
	if err != nil {
		return nil, fmt.Errorf("control: encode daemon record reservation: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			if data, readErr := readStoreFile(path); readErr == nil {
				if starting, ok := decodeStartingRecord(data); ok && starting.ConfigDigest != digest {
					return nil, ErrConfigurationMismatch
				}
				if existing, decodeErr := decodeStoredRecord(data, probeID); decodeErr == nil && existing.ConfigDigest != digest {
					return nil, ErrConfigurationMismatch
				}
			}
			return nil, ErrRecordActive
		}
		return nil, fmt.Errorf("control: reserve daemon record: %w", err)
	}
	if err := secureFile(file.Name()); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, err
	}
	if _, err := file.Write(marker); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, fmt.Errorf("control: write daemon record reservation: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return nil, fmt.Errorf("control: close daemon record reservation: %w", err)
	}
	return &Reservation{store: s, probeID: probeID, digest: digest, marker: marker}, nil
}

// RecoverStarting reclaims only an exact, valid starting reservation whose
// owner process no longer exists. It never terminates a process and never
// removes a published record or another instance's reservation.
func (s *Store) RecoverStarting(probeID, digest string) (bool, error) {
	if !validConfigDigest(digest) {
		return false, ErrConfigurationMismatch
	}
	path, err := s.recordPath(probeID)
	if err != nil {
		return false, err
	}
	marker, err := readStoreFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("control: read daemon record reservation: %w", err)
	}
	starting, ok := decodeStartingRecord(marker)
	if !ok {
		// A reservation may have been committed after the caller observed its
		// marker. Leave a complete record in place; readiness still requires an
		// authenticated control call by the caller.
		if record, recordErr := decodeStoredRecord(marker, probeID); recordErr == nil {
			if record.ConfigDigest != digest {
				return false, ErrConfigurationMismatch
			}
			return false, nil
		}
		return false, ErrConfigurationMismatch
	}
	if starting.ConfigDigest != digest {
		return false, ErrConfigurationMismatch
	}
	alive, err := processAlive(starting.PID)
	if err != nil {
		return false, fmt.Errorf("control: inspect starting daemon process: %w", err)
	}
	if alive {
		return false, nil
	}
	current, err := readStoreFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("control: re-read daemon record reservation: %w", err)
	}
	if !bytes.Equal(current, marker) {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("control: remove stale daemon record reservation: %w", err)
	}
	return true, nil
}

// Commit replaces this reservation's marker with a validated record. It
// refuses to overwrite anything other than its own marker, including a record
// created after an out-of-band filesystem intervention.
func (r *Reservation) Commit(record Record) error {
	if r == nil {
		return errors.New("control: active record reservation is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r == nil || r.store == nil || r.done {
		return errors.New("control: active record reservation is required")
	}
	if record.ProbeID != r.probeID {
		return errors.New("control: reservation probe ID does not match record")
	}
	if record.ConfigDigest != r.digest {
		return ErrConfigurationMismatch
	}
	if err := validateRecord(record, record.ProbeID); err != nil {
		return err
	}
	path, err := r.store.recordPath(r.probeID)
	if err != nil {
		return err
	}
	current, err := readStoreFile(path)
	if err != nil {
		return fmt.Errorf("control: read daemon record reservation: %w", err)
	}
	if !bytes.Equal(current, r.marker) {
		r.done = true
		return errors.New("control: daemon record reservation was replaced")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("control: encode daemon record: %w", err)
	}
	if len(data) > MaxMessageSize {
		return errors.New("control: daemon record exceeds size limit")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".multi-dap-record-*")
	if err != nil {
		return fmt.Errorf("control: create committed daemon record: %w", err)
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
		return fmt.Errorf("control: write daemon record: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("control: close daemon record: %w", err)
	}
	// Publication must not expose a truncated JSON prefix to concurrent ensure
	// callers. Recheck ownership immediately before the platform-atomic replace.
	current, err = readStoreFile(path)
	if err != nil {
		return fmt.Errorf("control: re-read daemon record reservation: %w", err)
	}
	if !bytes.Equal(current, r.marker) {
		r.done = true
		return errors.New("control: daemon record reservation was replaced")
	}
	if err := replaceFile(temporary, path); err != nil {
		return fmt.Errorf("control: publish committed daemon record: %w", err)
	}
	replaced = true
	r.done = true
	return nil
}

// Cancel releases an uncommitted reservation. It never removes a committed
// record or a path whose marker no longer belongs to this reservation.
func (r *Reservation) Cancel() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r == nil || r.store == nil || r.done {
		return nil
	}
	path, err := r.store.recordPath(r.probeID)
	if err != nil {
		return err
	}
	current, err := readStoreFile(path)
	if errors.Is(err, os.ErrNotExist) {
		r.done = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("control: read daemon record reservation: %w", err)
	}
	if !bytes.Equal(current, r.marker) {
		r.done = true
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("control: cancel daemon record reservation: %w", err)
	}
	r.done = true
	return nil
}

// RemoveIfInstance removes only this daemon's own record, so a late shutdown
// cannot erase a record created by a later instance for the same probe.
func (s *Store) RemoveIfInstance(probeID, instanceID string) error {
	path, err := s.recordPath(probeID)
	if err != nil {
		return err
	}
	data, err := readStoreFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("control: read daemon record before removal: %w", err)
	}
	var record Record
	if json.Unmarshal(data, &record) != nil || record.InstanceID != instanceID {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("control: remove daemon record: %w", err)
	}
	return nil
}

// RemoveIfEqual clears a record only after a failed authenticated probe has
// established it is stale. Comparing the complete record protects a newly
// published daemon from a delayed status/shutdown client.
func (s *Store) RemoveIfEqual(expected Record) error {
	path, err := s.recordPath(expected.ProbeID)
	if err != nil {
		return err
	}
	data, err := readStoreFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("control: read daemon record before stale removal: %w", err)
	}
	var current Record
	if json.Unmarshal(data, &current) != nil || !equalRecord(current, expected) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("control: remove stale daemon record: %w", err)
	}
	return nil
}

func validateRecord(record Record, probeID string) error {
	if record.Version != ProtocolVersion || record.ProbeID != probeID || !validConfigDigest(record.ConfigDigest) || record.InstanceID == "" || record.PID <= 0 || record.Token == "" {
		return errors.New("control: invalid daemon record")
	}
	for _, address := range []string{record.ControlAddress, record.DAPAddress} {
		host, port, err := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() || port == "0" {
			return fmt.Errorf("control: invalid loopback address %q", address)
		}
	}
	if !validSecret(record.Token) || !validSecret(record.InstanceID) {
		return errors.New("control: invalid daemon record credential")
	}
	return nil
}

func validSecret(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func decodeStartingRecord(data []byte) (startingRecord, bool) {
	fields, err := strictObject(data, map[string]struct{}{"version": {}, "state": {}, "config_digest": {}, "instance_id": {}, "pid": {}})
	if err != nil || len(fields) != 5 {
		return startingRecord{}, false
	}
	var record startingRecord
	if json.Unmarshal(data, &record) != nil || record.Version != ProtocolVersion || record.State != "starting" || !validConfigDigest(record.ConfigDigest) || record.PID <= 0 || !validSecret(record.InstanceID) {
		return startingRecord{}, false
	}
	return record, true
}

func validConfigDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

func equalRecord(a, b Record) bool {
	return bytes.Equal(mustMarshalRecord(a), mustMarshalRecord(b))
}

func mustMarshalRecord(record Record) []byte {
	data, _ := json.Marshal(record)
	return data
}
