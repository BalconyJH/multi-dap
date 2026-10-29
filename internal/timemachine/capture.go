package timemachine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	CaptureSchema       = "multi-dap.native-capture.v1"
	MaxCaptureBytes     = 16 << 20
	MaxCapturePackets   = 100000
	MaxGenerations      = 1024
	MaxContexts         = 1024
	MaxOpenFrames       = 4096
	MaxContextDepth     = 1024
	maxIdentifierLength = 128
)

type Source struct {
	Kind           string   `json:"kind"`
	SessionID      uint32   `json:"session_id"`
	SourceID       string   `json:"source_id"`
	CoreID         *uint32  `json:"core_id"`
	ImageSHA256    string   `json:"image_sha256,omitempty"`
	SnapshotSHA256 string   `json:"snapshot_sha256,omitempty"`
	InitialContext *Context `json:"initial_context,omitempty"`
}

// A generation is one contiguous observed packet interval. A gap or buffer
// replacement requires a new generation; it cannot be represented as growth.
type Generation struct {
	ID         uint64   `json:"id,string"`
	Reason     string   `json:"reason"`
	FirstIndex uint64   `json:"first_index,string"`
	PacketsHex []string `json:"packets_hex"`
}

// Capture is multi-dap's own interchange document, not a GHS file format.
// Hex fields retain the native API's fixed-size structures without allowing a
// caller to supply mutually inconsistent decoded fields.
type Capture struct {
	Schema       string       `json:"schema"`
	MultiVersion string       `json:"multi_version"`
	ABI          ABI          `json:"abi"`
	Source       Source       `json:"source"`
	InfoHex      string       `json:"info_hex"`
	Generations  []Generation `json:"generations"`
}

func DecodeCapture(reader io.Reader) (Capture, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxCaptureBytes+1))
	if err != nil {
		return Capture{}, errors.New("timemachine: capture read failed")
	}
	if len(data) > MaxCaptureBytes {
		return Capture{}, errors.New("timemachine: capture exceeds byte limit")
	}
	if !utf8.Valid(data) {
		return Capture{}, errors.New("timemachine: capture is not UTF-8")
	}
	if err := validateJSON(data); err != nil {
		return Capture{}, err
	}
	if err := validateCaptureShape(data); err != nil {
		return Capture{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var capture Capture
	if err := decoder.Decode(&capture); err != nil {
		return Capture{}, errors.New("timemachine: invalid capture fields")
	}
	if _, _, err := validateCapture(capture); err != nil {
		return Capture{}, err
	}
	return capture, nil
}

func validateCapture(capture Capture) (Profile, Info, error) {
	fail := func(reason string) (Profile, Info, error) {
		return Profile{}, Info{}, errors.New("timemachine: " + reason)
	}
	if capture.Schema != CaptureSchema {
		return fail("unsupported capture schema")
	}
	profile, err := ProfileForVersion(capture.MultiVersion)
	if err != nil {
		return Profile{}, Info{}, err
	}
	if capture.ABI != profile.ABI {
		return fail("capture ABI does not match exact release")
	}
	infoBytes, err := decodeFixedHex(capture.InfoHex, profile.ABI.InfoSize)
	if err != nil {
		return fail("invalid information encoding")
	}
	info, err := DecodeInfo(profile, infoBytes)
	if err != nil {
		return Profile{}, Info{}, err
	}
	source := capture.Source
	if source.SessionID > math.MaxInt32 {
		return fail("session identity exceeds native integer range")
	}
	if !boundedLabel(source.SourceID) {
		return fail("source identity is required and must be bounded")
	}
	switch source.Kind {
	case "live", "snapshot":
		if source.SessionID == 0 {
			return fail("source requires an explicit nonzero session identity")
		}
	case "synthetic_fixture":
	default:
		return fail("unsupported source kind")
	}
	if source.ImageSHA256 != "" && !validDigest(source.ImageSHA256) {
		return fail("invalid image digest")
	}
	if source.Kind == "snapshot" {
		if !validDigest(source.SnapshotSHA256) {
			return fail("snapshot source requires an explicit digest")
		}
	} else if source.SnapshotSHA256 != "" {
		return fail("snapshot digest requires a snapshot source")
	}
	if source.InitialContext != nil && !validContext(*source.InitialContext) {
		return fail("invalid declared initial context")
	}
	if len(capture.Generations) == 0 || len(capture.Generations) > MaxGenerations {
		return fail("invalid generation count")
	}
	var previousID uint64
	total := 0
	for _, generation := range capture.Generations {
		if generation.ID == 0 || generation.ID <= previousID {
			return fail("generation identities must increase")
		}
		previousID = generation.ID
		if generation.PacketsHex == nil {
			return fail("packet interval must be an explicit array")
		}
		switch generation.Reason {
		case "initial", "trace_start", "data_clear", "count_regression",
			"decoder_replacement", "snapshot_loaded", "packet_gap":
		default:
			return fail("unsupported generation reason")
		}
		if len(generation.PacketsHex) > MaxCapturePackets-total {
			return fail("capture exceeds packet limit")
		}
		total += len(generation.PacketsHex)
		if len(generation.PacketsHex) > 0 &&
			uint64(len(generation.PacketsHex)-1) > math.MaxUint64-generation.FirstIndex {
			return fail("packet index overflows")
		}
		for _, encoded := range generation.PacketsHex {
			data, err := decodeFixedHex(encoded, profile.ABI.PacketSize)
			if err != nil {
				return fail("invalid packet encoding")
			}
			if _, err := DecodePacket(profile, data); err != nil {
				return Profile{}, Info{}, err
			}
		}
	}
	return profile, info, nil
}

func canonicalDigest(capture Capture) (string, error) {
	data, err := json.Marshal(capture)
	if err != nil {
		return "", errors.New("timemachine: canonical capture encoding failed")
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func decodeFixedHex(value string, size int) ([]byte, error) {
	if len(value) != size*2 {
		return nil, errors.New("invalid encoded size")
	}
	data, err := hex.DecodeString(value)
	if err != nil {
		return nil, errors.New("invalid hex encoding")
	}
	return data, nil
}

func validDigest(value string) bool {
	_, err := decodeFixedHex(value, sha256.Size)
	return err == nil
}

func validContext(value Context) bool {
	for _, field := range []string{value.TaskID, value.AddressSpaceID} {
		if field == "" || len(field) > 20 {
			return false
		}
		number, err := strconv.ParseUint(field, 10, 64)
		if err != nil || strconv.FormatUint(number, 10) != field {
			return false
		}
	}
	return true
}

func knownContext(value *Context) bool {
	if value == nil {
		return false
	}
	switch value.TaskID {
	case "4294967295", "4294967293", "18446744073709551615", "18446744073709551613":
		return false
	}
	return value.AddressSpaceID != "4294967295" && value.AddressSpaceID != "18446744073709551615"
}

func boundedLabel(value string) bool {
	if value == "" || len(value) > maxIdentifierLength || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// encoding/json accepts duplicate object fields. Reject them before decoding
// provenance or ABI assertions, including inside nested objects.
func validateJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	values := 0
	var value func(int) error
	value = func(depth int) error {
		values++
		if values > MaxCapturePackets+MaxGenerations*32+64 {
			return errors.New("timemachine: capture JSON exceeds value limit")
		}
		if depth > 16 {
			return errors.New("timemachine: capture JSON exceeds nesting limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return errors.New("timemachine: invalid capture JSON")
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return errors.New("timemachine: invalid capture JSON")
				}
				key, ok := token.(string)
				folded := strings.ToLower(key)
				if !ok || len(key) > 64 || len(keys) >= 64 || keys[folded] {
					return errors.New("timemachine: duplicate or invalid capture field")
				}
				keys[folded] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("timemachine: invalid capture JSON")
		}
		end, err := decoder.Token()
		if err != nil || (delim == '{' && end != json.Delim('}')) ||
			(delim == '[' && end != json.Delim(']')) {
			return errors.New("timemachine: invalid capture JSON")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("timemachine: trailing capture JSON")
	}
	return nil
}

func validateCaptureShape(data []byte) error {
	root, err := exactObject(data,
		[]string{"schema", "multi_version", "abi", "source", "info_hex", "generations"}, nil, nil)
	if err != nil {
		return err
	}
	if _, err := exactObject(root["abi"], []string{
		"byte_order", "pointer_bits", "info_size", "packet_size", "packet_alignment",
		"type_offset", "reconstructed_offset", "address_offset", "payload_offset",
	}, nil, nil); err != nil {
		return err
	}
	source, err := exactObject(root["source"],
		[]string{"kind", "session_id", "source_id", "core_id"},
		[]string{"image_sha256", "snapshot_sha256", "initial_context"},
		[]string{"core_id", "initial_context"})
	if err != nil {
		return err
	}
	if context, exists := source["initial_context"]; exists && !isNull(context) {
		if _, err := exactObject(context, []string{"task_id", "address_space_id"}, nil, nil); err != nil {
			return err
		}
	}
	var generations []json.RawMessage
	if err := json.Unmarshal(root["generations"], &generations); err != nil {
		return errors.New("timemachine: invalid generation array")
	}
	for _, generation := range generations {
		if _, err := exactObject(generation, []string{"id", "reason", "first_index", "packets_hex"}, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func exactObject(data []byte, required, optional, nullable []string) (map[string]json.RawMessage, error) {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = true
	}
	for _, key := range optional {
		allowed[key] = true
	}
	nulls := make(map[string]bool, len(nullable))
	for _, key := range nullable {
		nulls[key] = true
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, errors.New("timemachine: expected capture object")
	}
	for key, value := range fields {
		if !allowed[key] || (isNull(value) && !nulls[key]) {
			return nil, errors.New("timemachine: unknown or null capture field")
		}
	}
	for _, key := range required {
		if _, exists := fields[key]; !exists {
			return nil, errors.New("timemachine: missing required capture field")
		}
	}
	return fields, nil
}

func isNull(data []byte) bool { return bytes.Equal(bytes.TrimSpace(data), []byte("null")) }
