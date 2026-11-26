package protocol

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// The vectors are shared with the TypeScript implementation.
const vectorsPath = "../../../packages/protocol/testdata/frames.json"

type frameVectors struct {
	Valid []struct {
		Name       string `json:"name"`
		Hex        string `json:"hex"`
		Type       string `json:"type"`
		Channel    uint32 `json:"channel"`
		Offset     uint64 `json:"offset"`
		PayloadHex string `json:"payloadHex"`
	} `json:"valid"`
	Unknown   []hexCase `json:"unknown"`
	Malformed []hexCase `json:"malformed"`
}

type hexCase struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
}

func loadVectors(t *testing.T) frameVectors {
	t.Helper()
	data, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v frameVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(v.Valid) == 0 || len(v.Unknown) == 0 || len(v.Malformed) == 0 {
		t.Fatalf("vectors file is missing a section")
	}
	return v
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func TestFrameVectors(t *testing.T) {
	v := loadVectors(t)

	for _, c := range v.Valid {
		t.Run(c.Name, func(t *testing.T) {
			wantType := map[string]byte{"output": FrameOutput, "input": FrameInput}[c.Type]
			payload := unhex(t, c.PayloadHex)

			f, err := ParseFrame(unhex(t, c.Hex))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if f.Type != wantType || f.Channel != c.Channel || f.Offset != c.Offset {
				t.Fatalf("parsed %+v, want type 0x%02x channel %d offset %d", f, wantType, c.Channel, c.Offset)
			}
			if hex.EncodeToString(f.Payload) != c.PayloadHex {
				t.Fatalf("payload = %x, want %s", f.Payload, c.PayloadHex)
			}

			var encoded []byte
			if wantType == FrameOutput {
				encoded = AppendOutput(nil, c.Channel, c.Offset, payload)
			} else {
				encoded = AppendInput(nil, c.Channel, payload)
			}
			if hex.EncodeToString(encoded) != c.Hex {
				t.Fatalf("encoded %x, want %s", encoded, c.Hex)
			}
		})
	}

	for _, c := range v.Unknown {
		t.Run(c.Name, func(t *testing.T) {
			if _, err := ParseFrame(unhex(t, c.Hex)); !errors.Is(err, ErrUnknownFrame) {
				t.Fatalf("err = %v, want ErrUnknownFrame", err)
			}
		})
	}

	for _, c := range v.Malformed {
		t.Run(c.Name, func(t *testing.T) {
			if _, err := ParseFrame(unhex(t, c.Hex)); !errors.Is(err, ErrMalformedFrame) {
				t.Fatalf("err = %v, want ErrMalformedFrame", err)
			}
		})
	}
}

func TestAppendKeepsPrefix(t *testing.T) {
	got := AppendInput([]byte{0xaa}, 1, []byte{0xbb})
	if hex.EncodeToString(got) != "aa0200000001bb" {
		t.Fatalf("got %x", got)
	}
}
