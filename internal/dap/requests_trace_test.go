package dap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

type nativeActionBackend struct {
	actionBackend
	enabled bool
}

func (b *nativeActionBackend) DAPCapabilities() Capabilities {
	return Capabilities{MultiDapSupportsNativeTraceRequests: b.enabled}
}

func nativeSession(t *testing.T) (*Session, *nativeActionBackend) {
	t.Helper()
	backend := &nativeActionBackend{enabled: true}
	backend.call = func(Action) (ActionResult, error) {
		return ActionResult{TraceBody: json.RawMessage(`{"status":{"hardware_qualified":false}}`)}, nil
	}
	return attachedSession(t, backend), backend
}

func TestNativeTraceRequestsUseIndependentTypedActions(t *testing.T) {
	session, backend := nativeSession(t)
	cases := []struct {
		command, args string
		kind          ActionKind
	}{
		{"multi-dap/trace/open", `{"sourceKind":"live","sessionId":17,"sourceId":"explicit source","acknowledgeUnverifiedSource":true,"coreId":0}`, ActionTraceOpen},
		{"multi-dap/trace/poll", `{"handle":1}`, ActionTracePoll},
		{"multi-dap/trace/read", `{"handle":1,"start":"18446744073709551614","generation":"18446744073709551615","count":1}`, ActionTraceRead},
		{"multi-dap/trace/start", `{"handle":1,"authorized":true}`, ActionTraceStart},
		{"multi-dap/trace/stop", `{"handle":1,"authorized":true}`, ActionTraceStop},
		{"multi-dap/trace/close", `{"handle":1}`, ActionTraceClose},
		{"multi-dap/trace/status", `{}`, ActionTraceStatus},
	}
	for index, test := range cases {
		messages := session.Handle(context.Background(), request(int64(index+4), test.command, test.args))
		if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
			t.Fatalf("%s = %#v", test.command, messages)
		}
		action := backend.actions[len(backend.actions)-1]
		if action.Kind != test.kind {
			t.Fatalf("%s action = %v", test.command, action.Kind)
		}
		if action.Kind == ActionTraceOpen && (action.TraceOpen.CoreID == nil ||
			*action.TraceOpen.CoreID != 0 || action.TraceOpen.SessionID != 17 ||
			!action.TraceOpen.AcknowledgeUnverifiedSource) {
			t.Fatalf("open fields = %#v", action.TraceOpen)
		}
		if action.Kind == ActionTraceRead && (action.TraceRead.Start != math.MaxUint64-1 || action.TraceRead.Generation != math.MaxUint64) {
			t.Fatal("trace integer precision was lost")
		}
	}
}

func TestNativeTraceArgumentsFailClosedBeforeBackend(t *testing.T) {
	open := `{"sourceKind":"live","sessionId":17,"sourceId":"explicit source","acknowledgeUnverifiedSource":true}`
	cases := []struct{ command, args string }{
		{"open", ``},
		{"open", `null`},
		{"open", `[]`},
		{"open", strings.Replace(open, `"sourceKind"`, `"SourceKind"`, 1)},
		{"open", strings.Replace(open, `"sessionId":17`, `"sessionId":17,"sessionId":18`, 1)},
		{"open", strings.Replace(open, `"sessionId":17`, `"sessionId":17,"session\u0049d":18`, 1)},
		{"open", strings.Replace(open, `"sessionId":17`, `"sessionId":0`, 1)},
		{"open", strings.Replace(open, `"sessionId":17`, `"sessionId":2147483648`, 1)},
		{"open", strings.Replace(open, `"sessionId":17`, `"sessionId":17.0`, 1)},
		{"open", strings.Replace(open, `"sourceId":"explicit source"`, `"sourceId":null`, 1)},
		{"open", strings.Replace(open, `"sourceId":"explicit source"`, `"sourceId":"private\nsource"`, 1)},
		{"open", strings.Replace(open, `"sourceId":"explicit source"`, `"sourceId":"\u202ePRIVATE"`, 1)},
		{"open", strings.Replace(open, `"sourceId":"explicit source"`, `"sourceId":" `+strings.Repeat("a", 127)+`"`, 1)},
		{"open", strings.Replace(open, `"acknowledgeUnverifiedSource":true`, `"acknowledgeUnverifiedSource":false`, 1)},
		{"open", strings.Replace(open, `"acknowledgeUnverifiedSource":true`, `"acknowledgeUnverifiedSource":1`, 1)},
		{"open", strings.TrimSuffix(open, "}") + `,"PRIVATE_UNKNOWN":"PRIVATE_PAYLOAD"}`},
		{"open", strings.TrimSuffix(open, "}") + `,"initial_context":{"PRIVATE_UNKNOWN":true}}`},
		{"open", strings.TrimSuffix(open, "}") + `,"coreId":null}`},
		{"open", strings.TrimSuffix(open, "}") + `,"coreId":-1}`},
		{"open", strings.TrimSuffix(open, "}") + `,"coreId":4294967296}`},
		{"open", strings.TrimSuffix(open, "}") + `,"imageSha256":""}`},
		{"open", strings.TrimSuffix(open, "}") + `,"snapshotSha256":"` + strings.Repeat("a", 64) + `"}`},
		{"open", strings.Replace(open, `"live"`, `"snapshot"`, 1)},
		{"open", strings.Replace(open, `"live"`, `"synthetic_fixture"`, 1)},
		{"open", open + ` {"PRIVATE_UNKNOWN":true}`},
		{"open", open + strings.Repeat(" ", MaxNativeTraceArguments)},
		{"open", string([]byte{'{', '"', 0xff, '"', ':', '1', '}'})},
		{"poll", `{"handle":0}`},
		{"poll", `{"handle":"1"}`},
		{"poll", `{"handle":2147483648}`},
		{"poll", `{"handle":1,"Handle":1}`},
		{"close", `{"handle":1,"authorized":true}`},
		{"start", `{"handle":1}`},
		{"start", `{"handle":1,"authorized":false}`},
		{"stop", `{"handle":1,"authorized":null}`},
		{"stop", `{"handle":1,"authorized":"true"}`},
		{"read", `{"handle":1,"start":0,"generation":"1","count":1}`},
		{"read", `{"handle":1,"start":"00","generation":"1","count":1}`},
		{"read", `{"handle":1,"start":"+1","generation":"1","count":1}`},
		{"read", `{"handle":1,"start":"-1","generation":"1","count":1}`},
		{"read", `{"handle":1,"start":"1.0","generation":"1","count":1}`},
		{"read", `{"handle":1,"start":"1e3","generation":"1","count":1}`},
		{"read", `{"handle":1,"start":"18446744073709551616","generation":"1","count":1}`},
		{"read", `{"handle":1,"start":"18446744073709551615","generation":"1","count":1}`},
		{"read", `{"handle":1,"start":"0","generation":"0","count":1}`},
		{"read", `{"handle":1,"start":"0","generation":1,"count":1}`},
		{"read", `{"handle":1,"start":"0","generation":"1","count":0}`},
		{"read", `{"handle":1,"start":"0","generation":"1","count":4097}`},
		{"read", `{"handle":1,"start":"0","generation":"1","count":1.0}`},
		{"read", `{"handle":1,"start":"0","generation":"1","count":1,"PRIVATE_UNKNOWN":true}`},
		{"status", `null`},
		{"status", `{"handle":1}`},
		{"status", `{} {}`},
	}
	for _, test := range cases {
		session, backend := nativeSession(t)
		before := len(backend.actions)
		messages := session.Handle(context.Background(), request(4, "multi-dap/trace/"+test.command, test.args))
		assertFailure(t, messages)
		if len(backend.actions) != before {
			t.Fatalf("invalid %s reached backend: %s", test.command, test.args)
		}
		if strings.Contains(messages[0].Message, "PRIVATE") {
			t.Fatal("trace validation reflected private arguments")
		}
	}
}

func TestNativeTraceSnapshotDeclarationAndOptionalFields(t *testing.T) {
	session, backend := nativeSession(t)
	arguments := `{"sourceKind":"snapshot","sessionId":17,"sourceId":"snapshot source","acknowledgeUnverifiedSource":true,"coreId":4294967295,"imageSha256":"` +
		strings.Repeat("A", 64) + `","snapshotSha256":"` + strings.Repeat("b", 64) + `"}`
	messages := session.Handle(context.Background(), request(4, "multi-dap/trace/open", arguments))
	if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
		t.Fatalf("snapshot declaration = %#v", messages)
	}
	args := backend.actions[len(backend.actions)-1].TraceOpen
	if args.CoreID == nil || *args.CoreID != math.MaxUint32 || args.ImageSHA256 != strings.Repeat("A", 64) ||
		args.SnapshotSHA256 != strings.Repeat("b", 64) {
		t.Fatalf("snapshot arguments = %#v", args)
	}
}

func TestNativeTraceRequiresConfiguredLifecycleAndWiredCapability(t *testing.T) {
	for _, phase := range []LifecyclePhase{AwaitInitialize, AwaitAttach, Configuring, Disconnected} {
		session, backend := nativeSession(t)
		session.lifecycle.Phase = phase
		before := len(backend.actions)
		assertFailure(t, session.Handle(context.Background(), request(4, "multi-dap/trace/status", `{}`)))
		if len(backend.actions) != before {
			t.Fatal("native trace escaped the attach barrier")
		}
	}
	backend := &nativeActionBackend{}
	session := attachedSession(t, backend)
	before := len(backend.actions)
	assertFailure(t, session.Handle(context.Background(), request(4, "multi-dap/trace/status", `{}`)))
	if len(backend.actions) != before {
		t.Fatal("unwired native request reached backend")
	}
	for _, enabled := range []bool{false, true} {
		session := NewSession(&nativeActionBackend{enabled: enabled})
		messages := session.Handle(context.Background(), request(1, "initialize", `{"adapterID":"test"}`))
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(messages[0].Body, &fields); err != nil {
			t.Fatal(err)
		}
		field, found := fields["multiDapSupportsNativeTraceRequests"]
		if found != enabled || (enabled && string(field) != "true") {
			t.Fatalf("trace capability = %s (found %v), enabled %v", field, found, enabled)
		}
	}
}

func TestNativeTraceErrorsAndInvalidBodiesNeverEscape(t *testing.T) {
	for _, test := range []struct {
		body json.RawMessage
		err  error
	}{
		{json.RawMessage(`{"batch":{"source":"PRIVATE_DATA"}}`), errors.New("PRIVATE_ERROR")},
		{json.RawMessage(`{"batch":{"source":"PRIVATE_DATA"}}`), NewNativeTraceError(NativeTraceReconciliationRequired, errors.New("PRIVATE_ERROR"))},
		{json.RawMessage(`{"batch":{"source":"PRIVATE_DATA"}}`), NewNativeTraceError(NativeTraceErrorCode(255), errors.New("PRIVATE_ERROR"))},
		{nil, nil},
		{json.RawMessage(`[]`), nil},
		{json.RawMessage(`"PRIVATE_DATA"`), nil},
		{json.RawMessage(`{"bad":`), nil},
		{json.RawMessage(`{"value":"` + strings.Repeat("x", MaxNativeTraceBodyLength) + `"}`), nil},
		{json.RawMessage(`{}` + strings.Repeat(" ", MaxNativeTraceBodyLength)), nil},
	} {
		session, backend := nativeSession(t)
		backend.call = func(Action) (ActionResult, error) {
			return ActionResult{TraceBody: test.body}, test.err
		}
		messages := session.Handle(context.Background(), request(4, "multi-dap/trace/status", `{}`))
		assertFailure(t, messages)
		if len(messages[0].Body) != 0 || strings.Contains(messages[0].Message, "PRIVATE") {
			t.Fatal("trace failure exposed a partial batch or raw provider error")
		}
	}
}

func TestNativeTraceResponseFitsNormalDAPEnvelope(t *testing.T) {
	session, backend := nativeSession(t)
	backend.call = func(Action) (ActionResult, error) {
		return ActionResult{TraceBody: json.RawMessage(`{"batch":"` + strings.Repeat("x", MaxNativeTraceBodyLength-12) + `"}`)}, nil
	}
	messages := session.Handle(context.Background(), request(4, "multi-dap/trace/status", `{}`))
	if len(messages) != 1 || messages[0].Success == nil || !*messages[0].Success {
		t.Fatalf("bounded response = %#v", messages)
	}
	if err := NewEncoder(&bytes.Buffer{}).Encode(messages[0]); err != nil {
		t.Fatal(err)
	}
}
