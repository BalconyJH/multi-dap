package tracebridge

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestStrictFrameRejectsAmbiguousOrPrivateFields(t *testing.T) {
	for _, data := range []string{
		`null`, `[]`, `{}`,
		`{"v":1,"seq":1,"ok":true,"result":null}`,
		`{"v":1,"seq":1,"ok":null,"result":{}}`,
		`{"v":1,"seq":1,"ok":true,"result":{},"private-secret":0}`,
		`{"v":1,"seq":1,"Seq":1,"ok":true,"result":{}}`,
		`{"v":1,"seq":1,"ok":true,"result":{"generation":"1","Generation":"2"}}`,
		`{"v":1,"seq":1,"ok":true,"result":{}} {}`,
		`{"v":1,"seq":1,"ok":false,"error":{"code":"private-secret"}}`,
		`{"v":1,"seq":1,"ok":false,"error":{"code":"native_failure","message":"private-secret"}}`,
		`{"v":1,"seq":0,"ok":true,"result":{}}`,
		`{"v":1,"seq":1.0,"ok":true,"result":{}}`,
		`{"V":1,"seq":1,"ok":true,"result":{}}`,
		`{"v":1,"event":"private-secret"}`,
		`{"v":1,"event":"fatal","error":{"code":null}}`,
		string([]byte{'{', '"', 0xff, '"', ':', '1', '}'}),
		strings.Repeat("[", 10) + "1" + strings.Repeat("]", 10),
	} {
		if _, err := decodeFrame([]byte(data)); !errors.Is(err, ErrProtocol) || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("invalid frame returned %v", err)
		}
	}
}

func TestStatusRequiresExactNonNullNestedFields(t *testing.T) {
	data, _ := json.Marshal(fakeStatus(1))
	for _, changed := range []string{
		strings.Replace(string(data), `"notifications":`, `"Notifications":`, 1),
		strings.Replace(string(data), `"callback_failed":false`, `"callback_failed":null`, 1),
		strings.Replace(string(data), `"dropped":0`, `"Dropped":0`, 1),
		strings.Replace(string(data), `"events":["decoder_replacement"]`, `"events":[null]`, 1),
		strings.Replace(string(data), `"generation":"1"`, `"generation":"01"`, 1),
	} {
		encoded := `{"v":1,"seq":1,"ok":true,"result":` + changed + `}`
		frame, err := decodeFrame([]byte(encoded))
		if err == nil {
			_, err = decodeStatus(frame.result, 64)
		}
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid nested status accepted: %s", changed)
		}
	}
}

func TestLineReaderIsBoundedAndRequiresNewline(t *testing.T) {
	for _, data := range []string{
		strings.Repeat("x", MaxResponseBytes+1), `{"v":1}`, strings.Repeat("x", MaxResponseBytes) + "\n",
	} {
		if _, err := readBoundedLine(bufio.NewReaderSize(strings.NewReader(data), 32)); err == nil {
			t.Fatal("invalid framing accepted")
		}
	}
	if _, err := readBoundedLine(bufio.NewReader(strings.NewReader(""))); !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream = %v", err)
	}
	if got, err := readBoundedLine(bufio.NewReader(strings.NewReader("{}\r\n"))); err != nil || string(got) != "{}" {
		t.Fatalf("CRLF = %q, %v", got, err)
	}
}

func FuzzStrictHelperFrame(f *testing.F) {
	f.Add([]byte(`{"v":1,"seq":1,"ok":true,"result":{}}`))
	f.Add([]byte(`{"v":1,"event":"fatal","error":{"code":"native_failure"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxResponseBytes {
			t.Skip()
		}
		frame, err := decodeFrame(data)
		if err != nil {
			if !errors.Is(err, ErrProtocol) {
				t.Fatal("unexpected decoder failure")
			}
			return
		}
		if frame.result != nil {
			_, _ = decodeStatus(frame.result, 64)
			_, _ = decodeRead(frame.result, ReadArgs{Start: 0, Count: 1, Generation: 1})
			_, _ = decodeControl(frame.result)
			_ = decodeClose(frame.result)
		}
	})
}
