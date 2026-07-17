package cursor

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func testCursor() ChannelHistory {
	since := int64(10)
	return ChannelHistory{
		Version: 1, Scope: "channel", ChannelID: "channel",
		Boundary: Boundary{CreateAt: 123, ID: "post"}, Since: &since,
	}
}

func TestChannelHistoryRoundTrip(t *testing.T) {
	want := testCursor()
	encoded, err := EncodeChannelHistory(want)
	if err != nil {
		t.Fatal(err)
	}
	const canonical = `eyJ2IjoxLCJzY29wZSI6ImNoYW5uZWwiLCJjaGFubmVsSWQiOiJjaGFubmVsIiwiYm91bmRhcnkiOnsiY3JlYXRlQXQiOjEyMywiaWQiOiJwb3N0In0sInNpbmNlIjoxMH0`
	if encoded != canonical {
		t.Fatalf("encoded = %q, want %q", encoded, canonical)
	}
	got, err := DecodeChannelHistory(encoded)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeChannelHistory() = %#v, %v; want %#v", got, err, want)
	}
}

func TestDecodeChannelHistoryRejectsInvalid(t *testing.T) {
	encode := func(json string) string { return base64.RawURLEncoding.EncodeToString([]byte(json)) }
	tests := map[string]string{
		"empty": "", "not JSON": "not_json", "padding": "e30=", "too long": strings.Repeat("a", 2049),
		"version":               encode(`{"v":2,"scope":"channel","channelId":"channel","boundary":{"createAt":123,"id":"post"},"since":10}`),
		"bad boundary":          encode(`{"v":1,"scope":"channel","channelId":"channel","boundary":{"createAt":-1,"id":""},"since":10}`),
		"extra":                 encode(`{"v":1,"scope":"channel","channelId":"channel","boundary":{"createAt":123,"id":"post"},"since":10,"extra":true}`),
		"boundary extra":        encode(`{"v":1,"scope":"channel","channelId":"channel","boundary":{"createAt":123,"id":"post","extra":true},"since":10}`),
		"since beyond boundary": encode(`{"v":1,"scope":"channel","channelId":"channel","boundary":{"createAt":123,"id":"post"},"since":124}`),
		"optional null":         encode(`{"v":1,"scope":"channel","channelId":"channel","boundary":{"createAt":123,"id":"post"},"since":10,"safeBeforePostId":null}`),
		"invalid UTF-8":         base64.RawURLEncoding.EncodeToString([]byte{0xff}),
		"oversized decoded":     base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(" ", 1537))),
		"trailing garbage":      encode(`{"v":1,"scope":"channel","channelId":"channel","boundary":{"createAt":123,"id":"post"},"since":10}x`),
	}
	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeChannelHistory(encoded)
			if !errors.Is(err, ErrInvalidCursor) || err.Error() != "invalid cursor" {
				t.Fatalf("error = %v, want generic ErrInvalidCursor", err)
			}
		})
	}
}

func TestChannelHistoryValidationBoundaries(t *testing.T) {
	valid := testCursor()
	longID := strings.Repeat("a", 128)
	valid.ChannelID, valid.Boundary.ID, valid.SafeBeforePostID = longID, "-_AZaz09", "safe_post"
	valid.Boundary.CreateAt = maxDateMillis
	*valid.Since = maxDateMillis
	if _, err := EncodeChannelHistory(valid); err != nil {
		t.Fatalf("boundary cursor rejected: %v", err)
	}

	invalid := []ChannelHistory{testCursor(), testCursor(), testCursor(), testCursor()}
	invalid[0].ChannelID = strings.Repeat("a", 129)
	invalid[1].Boundary.CreateAt = maxDateMillis + 1
	invalid[2].SafeBeforePostID = "not safe!"
	minusOne := int64(-1)
	invalid[3].Since = &minusOne
	for i, value := range invalid {
		if _, err := EncodeChannelHistory(value); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("case %d: error = %v, want ErrInvalidCursor", i, err)
		}
	}
}

func TestDecodeAcceptsJSONSafeIntegerSpellings(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1.0,"scope":"channel","channelId":"channel","boundary":{"createAt":1.23e2,"id":"post"},"since":1e1}`))
	got, err := DecodeChannelHistory(encoded)
	if err != nil || got.Version != 1 || got.Boundary.CreateAt != 123 || got.Since == nil || *got.Since != 10 {
		t.Fatalf("DecodeChannelHistory() = %#v, %v", got, err)
	}
}

func TestComparePostIDs(t *testing.T) {
	values := []string{"-", "A", "_", "z"}
	for i := 1; i < len(values); i++ {
		if ComparePostIDs(values[i-1], values[i]) != -1 || ComparePostIDs(values[i], values[i-1]) != 1 {
			t.Fatalf("unexpected ordering for %q and %q", values[i-1], values[i])
		}
	}
	if ComparePostIDs("same", "same") != 0 {
		t.Fatal("equal IDs did not compare equal")
	}
}

func FuzzDecodeChannelHistory(f *testing.F) {
	encoded, _ := EncodeChannelHistory(testCursor())
	for _, seed := range []string{encoded, "", "e30=", "not_json"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		value, err := DecodeChannelHistory(input)
		if err != nil {
			if !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		roundTrip, err := EncodeChannelHistory(value)
		if err != nil {
			t.Fatalf("decoded cursor failed to encode: %v", err)
		}
		decoded, err := DecodeChannelHistory(roundTrip)
		if err != nil || !reflect.DeepEqual(decoded, value) {
			t.Fatalf("round trip = %#v, %v; want %#v", decoded, err, value)
		}
	})
}
