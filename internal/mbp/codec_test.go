package mbp

import (
	"bytes"
	"strings"
	"testing"
)

func TestDecodeResponseFrame(t *testing.T) {
	line := `{"id":7,"ok":true,"result":{"execution":"stopped"}}` + "\n"
	d := NewDecoder(strings.NewReader(line))
	f, err := d.Decode()
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if f.Kind() != KindResponse {
		t.Fatalf("Kind() = %v, want KindResponse", f.Kind())
	}
	resp := f.(*Response)
	if resp.ID != 7 || !resp.OK {
		t.Fatalf("got id=%d ok=%v, want id=7 ok=true", resp.ID, resp.OK)
	}
}

func TestDecodeErrorFrameCarriesRaw(t *testing.T) {
	line := `{"id":9,"ok":false,"error":{"kind":"multi_refused","message":"no","raw":"E>"}}` + "\n"
	d := NewDecoder(strings.NewReader(line))
	f, err := d.Decode()
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	resp := f.(*Response)
	if resp.OK {
		t.Fatal("OK = true, want false")
	}
	if resp.Error.Raw != "E>" {
		t.Fatalf("Error.Raw = %q, want %q", resp.Error.Raw, "E>")
	}
}

func TestDecodeEventFrameHasNoID(t *testing.T) {
	line := `{"event":"bridge_closing","params":{}}` + "\n"
	d := NewDecoder(strings.NewReader(line))
	f, err := d.Decode()
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if f.Kind() != KindEvent {
		t.Fatalf("Kind() = %v, want KindEvent", f.Kind())
	}
}

func TestEncodeRequestIsOneNDJSONLine(t *testing.T) {
	var buf bytes.Buffer
	e := NewEncoder(&buf)
	if err := e.Encode(&Request{ID: 1, Method: "state"}); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("Encode() = %q, want exactly one trailing newline", out)
	}
	if strings.Contains(strings.TrimSuffix(out, "\n"), "\n") {
		t.Fatal("encoded frame contains an embedded newline")
	}
}

func TestDecodeRejectsOversizeLine(t *testing.T) {
	big := `{"id":1,"method":"` + strings.Repeat("x", 1<<21) + `"}` + "\n"
	d := NewDecoder(strings.NewReader(big))
	if _, err := d.Decode(); err == nil {
		t.Fatal("Decode() error = nil, want an oversize-frame error")
	}
}
