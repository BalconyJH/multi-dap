package multi

import (
	"context"
	"errors"
	"testing"
)

func TestDriverConsole(t *testing.T) {
	driver, caller := newTestDriver(t, fakeCall{
		method: "console_read", params: map[string]string{},
		result: map[string]any{"server": "target text", "io": "serial text", "raw_lossy": true, "truncated": true},
	})
	got, err := driver.Console(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Server != "target text" || got.IO != "serial text" || !got.RawLossy || !got.Truncated {
		t.Fatalf("Console() = %#v", got)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestDriverConsoleRejectsMalformedResult(t *testing.T) {
	for _, result := range []map[string]any{
		{"server": "x"},
		{"server": "x", "io": 4},
		{"server": "x", "io": "y", "extra": true},
		{"server": "x", "io": "y", "truncated": false},
	} {
		driver, _ := newTestDriver(t, fakeCall{method: "console_read", params: map[string]string{}, result: result})
		if _, err := driver.Console(context.Background()); !errors.Is(err, ErrProtocol) {
			t.Fatalf("Console(%#v) error = %v, want ErrProtocol", result, err)
		}
	}
}

func TestDriverConsoleEscapesTerminalControls(t *testing.T) {
	driver, _ := newTestDriver(t, fakeCall{
		method: "console_read", params: map[string]string{},
		result: map[string]any{"server": "ok\x1b]0;spoof\a\n\x00\x7f", "io": "\u0085tab\tline\r\n"},
	})
	got, err := driver.Console(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Server != `ok\x1b]0;spoof\x07
\x00\x7f` {
		t.Fatalf("server = %q", got.Server)
	}
	if got.IO != "\\x85tab\tline\r\n" {
		t.Fatalf("io = %q", got.IO)
	}
}

func TestDriverResetConsole(t *testing.T) {
	driver, caller := newTestDriver(t, fakeCall{
		method: "console_reset", params: map[string]string{}, result: map[string]any{"reset": true},
	})
	if err := driver.ResetConsole(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestDriverResetConsoleRejectsMalformedResultAndRemoteError(t *testing.T) {
	for _, result := range []map[string]any{
		{}, {"reset": false}, {"reset": "true"}, {"reset": true, "extra": true},
	} {
		driver, _ := newTestDriver(t, fakeCall{method: "console_reset", params: map[string]string{}, result: result})
		if err := driver.ResetConsole(context.Background()); !errors.Is(err, ErrProtocol) {
			t.Fatalf("ResetConsole(%#v) error = %v, want ErrProtocol", result, err)
		}
	}
	want := errors.New("bridge refused reset")
	driver, _ := newTestDriver(t, fakeCall{method: "console_reset", params: map[string]string{}, err: want})
	if err := driver.ResetConsole(context.Background()); !errors.Is(err, want) {
		t.Fatalf("ResetConsole remote error = %v, want %v", err, want)
	}
}
