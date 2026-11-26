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
		Cols       uint16 `json:"cols"`
		Rows       uint16 `json:"rows"`
		PayloadHex string `json:"payloadHex"`
	} `json:"valid"`
	Unknown   []hexCase `json:"unknown"`
	Malformed []hexCase `json:"malformed"`
}

type hexCase struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
}

var frameTypes = map[string]byte{
	"output": FrameOutput,
	"reset":  FrameReset,
	"state":  FrameState,
	"resize": FrameResize,
	"input":  FrameInput,
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
	covered := map[byte]bool{}

	for _, c := range v.Valid {
		t.Run(c.Name, func(t *testing.T) {
			typ, ok := frameTypes[c.Type]
			if !ok {
				t.Fatalf("vector has unknown type %q", c.Type)
			}
			covered[typ] = true
			want := Frame{Type: typ, Channel: c.Channel, Offset: c.Offset, Cols: c.Cols, Rows: c.Rows}

			got, err := ParseFrame(unhex(t, c.Hex))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if hex.EncodeToString(got.Payload) != c.PayloadHex {
				t.Fatalf("payload = %x, want %s", got.Payload, c.PayloadHex)
			}
			payload := got.Payload
			got.Payload = nil
			if got.Type != want.Type || got.Channel != want.Channel || got.Offset != want.Offset ||
				got.Cols != want.Cols || got.Rows != want.Rows {
				t.Fatalf("parsed %+v, want %+v", got, want)
			}

			want.Payload = payload
			if encoded := AppendFrame(nil, want); hex.EncodeToString(encoded) != c.Hex {
				t.Fatalf("encoded %x, want %s", encoded, c.Hex)
			}
		})
	}

	for name, typ := range frameTypes {
		if !covered[typ] {
			t.Errorf("no vector covers frame type %q", name)
		}
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

func TestAppendFrameKeepsPrefix(t *testing.T) {
	got := AppendFrame([]byte{0xaa}, Frame{Type: FrameInput, Channel: 1, Payload: []byte{0xbb}})
	if hex.EncodeToString(got) != "aa1000000001bb" {
		t.Fatalf("got %x", got)
	}
}
