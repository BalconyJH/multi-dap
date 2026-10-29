package dap

import (
	"bytes"
	"context"
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

func (s *Session) nativeTrace(ctx context.Context, request Envelope, kind ActionKind) []Envelope {
	if s.lifecycle.Phase != Attached {
		return []Envelope{s.failure(request, "native trace requires an attached, configured frontend")}
	}
	if !capabilities(s.backend).MultiDapSupportsNativeTraceRequests {
		return []Envelope{s.failure(request, (&NativeTraceError{Code: NativeTraceUnavailable}).Error())}
	}
	action, err := decodeNativeTraceAction(kind, request.Arguments)
	if err != nil {
		return []Envelope{s.failure(request, nativeTraceErrorMessage(err))}
	}
	result, err := s.execute(ctx, action)
	if err != nil {
		return []Envelope{s.failure(request, nativeTraceErrorMessage(err))}
	}
	body := bytes.TrimSpace(result.TraceBody)
	if len(body) == 0 || len(result.TraceBody) > MaxNativeTraceBodyLength || body[0] != '{' || !json.Valid(body) {
		return []Envelope{s.failure(request, (&NativeTraceError{Code: NativeTraceInvalidResponse}).Error())}
	}
	return []Envelope{s.success(request, result.TraceBody)}
}

func decodeNativeTraceAction(kind ActionKind, data []byte) (Action, error) {
	invalid := func() (Action, error) {
		return Action{}, NewNativeTraceError(NativeTraceInvalidArguments, nil)
	}
	var required, optional []string
	switch kind {
	case ActionTraceOpen:
		required = []string{"sourceKind", "sessionId", "sourceId", "acknowledgeUnverifiedSource"}
		optional = []string{"coreId", "imageSha256", "snapshotSha256"}
	case ActionTracePoll, ActionTraceClose:
		required = []string{"handle"}
	case ActionTraceRead:
		required = []string{"handle", "start", "generation", "count"}
	case ActionTraceStart, ActionTraceStop:
		required = []string{"handle", "authorized"}
	case ActionTraceStatus:
	default:
		return invalid()
	}
	fields, ok := nativeTraceFields(data, required, optional)
	if !ok {
		return invalid()
	}
	action := Action{Kind: kind}
	decode := func(name string, destination any) bool {
		value, exists := fields[name]
		return exists && json.Unmarshal(value, destination) == nil
	}
	switch kind {
	case ActionTraceOpen:
		args := &action.TraceOpen
		if !decode("sourceKind", &args.SourceKind) || !decode("sessionId", &args.SessionID) ||
			!decode("sourceId", &args.SourceID) || !decode("acknowledgeUnverifiedSource", &args.AcknowledgeUnverifiedSource) {
			return invalid()
		}
		if _, exists := fields["coreId"]; exists {
			var core uint32
			if !decode("coreId", &core) {
				return invalid()
			}
			args.CoreID = &core
		}
		for _, optional := range []struct {
			name   string
			target *string
		}{{"imageSha256", &args.ImageSHA256}, {"snapshotSha256", &args.SnapshotSHA256}} {
			if _, exists := fields[optional.name]; exists {
				if !decode(optional.name, optional.target) || !nativeTraceDigest(*optional.target) {
					return invalid()
				}
			}
		}
	case ActionTracePoll, ActionTraceClose:
		if !decode("handle", &action.TraceHandle.Handle) {
			return invalid()
		}
	case ActionTraceRead:
		args := &action.TraceRead
		var start, generation string
		if !decode("handle", &args.Handle) || !decode("start", &start) ||
			!decode("generation", &generation) || !decode("count", &args.Count) {
			return invalid()
		}
		var err error
		if args.Start, err = canonicalTraceUint(start); err != nil {
			return invalid()
		}
		if args.Generation, err = canonicalTraceUint(generation); err != nil {
			return invalid()
		}
	case ActionTraceStart, ActionTraceStop:
		if !decode("handle", &action.TraceControl.Handle) || !decode("authorized", &action.TraceControl.Authorized) {
			return invalid()
		}
	}
	if err := ValidateNativeTraceAction(action); err != nil {
		return Action{}, err
	}
	return action, nil
}

// ValidateNativeTraceAction also protects direct in-process backend callers.
// This is protocol validation only; the actor owns runtime and lease policy.
func ValidateNativeTraceAction(action Action) error {
	invalid := func() error { return NewNativeTraceError(NativeTraceInvalidArguments, nil) }
	handleOK := func(value uint64) bool { return value > 0 && value <= math.MaxInt32 }
	if (action.Kind != ActionTraceOpen && action.TraceOpen != (NativeTraceOpenArguments{})) ||
		(action.Kind != ActionTracePoll && action.Kind != ActionTraceClose && action.TraceHandle != (NativeTraceHandleArguments{})) ||
		(action.Kind != ActionTraceRead && action.TraceRead != (NativeTraceReadArguments{})) ||
		(action.Kind != ActionTraceStart && action.Kind != ActionTraceStop && action.TraceControl != (NativeTraceControlArguments{})) {
		return invalid()
	}
	switch action.Kind {
	case ActionTraceOpen:
		args := action.TraceOpen
		if args.SourceKind != "live" && args.SourceKind != "snapshot" {
			return invalid()
		}
		if args.SessionID == 0 || args.SessionID > math.MaxInt32 || !nativeTraceLabel(args.SourceID) {
			return invalid()
		}
		if !args.AcknowledgeUnverifiedSource {
			return NewNativeTraceError(NativeTraceSourceAcknowledgementRequired, nil)
		}
		if args.ImageSHA256 != "" && !nativeTraceDigest(args.ImageSHA256) {
			return invalid()
		}
		if (args.SourceKind == "snapshot" && !nativeTraceDigest(args.SnapshotSHA256)) ||
			(args.SourceKind == "live" && args.SnapshotSHA256 != "") {
			return invalid()
		}
	case ActionTracePoll, ActionTraceClose:
		if !handleOK(action.TraceHandle.Handle) {
			return invalid()
		}
	case ActionTraceRead:
		args := action.TraceRead
		if !handleOK(args.Handle) || args.Generation == 0 ||
			args.Count < 1 || args.Count > MaxNativeTracePackets ||
			args.Start > math.MaxUint64-uint64(args.Count) {
			return invalid()
		}
	case ActionTraceStart, ActionTraceStop:
		if !handleOK(action.TraceControl.Handle) || !action.TraceControl.Authorized {
			return invalid()
		}
	case ActionTraceStatus:
	default:
		return invalid()
	}
	return nil
}

func nativeTraceFields(data []byte, required, optional []string) (map[string]json.RawMessage, bool) {
	if len(data) == 0 && len(required) == 0 {
		return map[string]json.RawMessage{}, true
	}
	if len(data) > MaxNativeTraceArguments || !utf8.Valid(data) {
		return nil, false
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
	}
	for _, name := range optional {
		allowed[name] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		name, ok := token.(string)
		if !ok || !allowed[name] {
			return nil, false
		}
		if _, exists := fields[name]; exists {
			return nil, false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, false
		}
		fields[name] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	for _, name := range required {
		if _, exists := fields[name]; !exists {
			return nil, false
		}
	}
	return fields, true
}

func canonicalTraceUint(value string) (uint64, error) {
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(number, 10) != value {
		return 0, errors.New("invalid native trace integer")
	}
	return number, nil
}

func nativeTraceDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func nativeTraceLabel(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, ch := range value {
		if unicode.IsControl(ch) || unicode.Is(unicode.Cf, ch) {
			return false
		}
	}
	return true
}
