package stagecursor

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testBoundary() Boundary {
	return Boundary{UpdatedAt: time.Date(2026, 7, 17, 12, 34, 56, 123456789, time.UTC), StageID: "stg_0123456789abcdefghijklmnopqrstuv"}
}

func TestRoundTripIsCanonical(t *testing.T) {
	want := testBoundary()
	encoded, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	const canonical = "eyJ2IjoxLCJzY29wZSI6InN0YWdlcyIsImJvdW5kYXJ5Ijp7InVwZGF0ZWRBdCI6IjIwMjYtMDctMTdUMTI6MzQ6NTYuMTIzNDU2Nzg5WiIsInN0YWdlSWQiOiJzdGdfMDEyMzQ1Njc4OWFiY2RlZmdoaWprbG1ub3BxcnN0dXYifX0"
	if encoded != canonical {
		t.Fatalf("Encode() = %q, want %q", encoded, canonical)
	}
	got, err := Decode(encoded)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode() = %#v, %v; want %#v", got, err, want)
	}
}

func TestDecodeRejectsNonCanonicalAndMalformedCursors(t *testing.T) {
	encode := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	valid := `{"v":1,"scope":"stages","boundary":{"updatedAt":"2026-07-17T12:34:56.123456789Z","stageId":"stage_1"}}`
	tests := map[string]string{
		"empty":             "",
		"invalid base64":    "not+base64",
		"padding":           encode(valid) + "=",
		"encoded too long":  strings.Repeat("a", maxEncodedLength+1),
		"decoded too long":  base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(" ", maxDecodedLength+1))),
		"invalid UTF-8":     base64.RawURLEncoding.EncodeToString([]byte{0xff}),
		"wrong version":     encode(strings.Replace(valid, `"v":1`, `"v":2`, 1)),
		"wrong scope":       encode(strings.Replace(valid, `"stages"`, `"stage"`, 1)),
		"outer extra":       encode(strings.TrimSuffix(valid, "}") + `,"extra":true}`),
		"boundary extra":    encode(strings.Replace(valid, `"stageId":"stage_1"`, `"stageId":"stage_1","extra":true`, 1)),
		"missing field":     encode(strings.Replace(valid, `,"stageId":"stage_1"`, "", 1)),
		"duplicate key":     encode(strings.Replace(valid, `"v":1`, `"v":1,"v":1`, 1)),
		"trailing JSON":     encode(valid + `{}`),
		"whitespace":        encode(" " + valid),
		"reordered":         encode(`{"scope":"stages","v":1,"boundary":{"updatedAt":"2026-07-17T12:34:56.123456789Z","stageId":"stage_1"}}`),
		"offset timestamp":  encode(strings.Replace(valid, "2026-07-17T12:34:56.123456789Z", "2026-07-17T15:34:56.123456789+03:00", 1)),
		"noncanonical time": encode(strings.Replace(valid, "2026-07-17T12:34:56.123456789Z", "2026-07-17T12:34:56.1234567890Z", 1)),
		"zero timestamp":    encode(strings.Replace(valid, "2026-07-17T12:34:56.123456789Z", "0001-01-01T00:00:00Z", 1)),
		"unsafe stage ID":   encode(strings.Replace(valid, "stage_1", "stage 1", 1)),
		"long stage ID":     encode(strings.Replace(valid, "stage_1", strings.Repeat("a", maxStageIDLength+1), 1)),
	}
	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(encoded)
			if !errors.Is(err, ErrInvalidCursor) || err.Error() != "invalid stage cursor" {
				t.Fatalf("error = %v, want generic ErrInvalidCursor", err)
			}
		})
	}
}

func TestEncodeRejectsInvalidBoundaries(t *testing.T) {
	valid := testBoundary()
	invalid := []Boundary{
		{},
		{UpdatedAt: valid.UpdatedAt},
		{UpdatedAt: time.Time{}, StageID: valid.StageID},
		{UpdatedAt: valid.UpdatedAt, StageID: "unsafe id"},
		{UpdatedAt: valid.UpdatedAt, StageID: strings.Repeat("a", maxStageIDLength+1)},
		{UpdatedAt: valid.UpdatedAt.In(time.FixedZone("EEST", 3*60*60)), StageID: valid.StageID},
	}
	for i, boundary := range invalid {
		if _, err := Encode(boundary); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("case %d: error = %v, want ErrInvalidCursor", i, err)
		}
	}
	max := valid
	max.StageID = strings.Repeat("a", maxStageIDLength)
	if _, err := Encode(max); err != nil {
		t.Fatalf("maximum safe stage ID rejected: %v", err)
	}
}

func FuzzDecode(f *testing.F) {
	encoded, _ := Encode(testBoundary())
	for _, seed := range []string{encoded, "", "e30=", "not_json"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		boundary, err := Decode(input)
		if err != nil {
			if !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		roundTrip, err := Encode(boundary)
		if err != nil || roundTrip != input {
			t.Fatalf("canonical round trip = %q, %v; want %q", roundTrip, err, input)
		}
	})
}
