package transport

import (
	"context"
	"testing"
)

func TestMessageTypesRemainDistinct(t *testing.T) {
	if MessageText == MessageBinary {
		t.Fatal("text and binary WebSocket messages collapsed")
	}
	if _, err := Dial(context.Background(), "://invalid"); err == nil {
		t.Fatal("invalid dial target accepted")
	}
}
