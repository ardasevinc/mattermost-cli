package main

import "testing"

func TestRequestedPortIsCanonicalAndBounded(t *testing.T) {
	for _, value := range []string{"", "0", "1", "65535"} {
		if _, err := requestedPort(value); err != nil {
			t.Fatalf("requestedPort(%q): %v", value, err)
		}
	}
	for _, value := range []string{"-1", "01", "+1", "65536", "word", " 1"} {
		if _, err := requestedPort(value); err == nil {
			t.Fatalf("requestedPort(%q) succeeded", value)
		}
	}
}

func TestPublishedBindingMustBeOneLoopbackAddress(t *testing.T) {
	for _, value := range []string{"127.0.0.1:18065", "[::1]:18065\n"} {
		if port, err := parseLoopbackPublishedPort(value); err != nil || port != "18065" {
			t.Fatalf("parseLoopbackPublishedPort(%q) = %q, %v", value, port, err)
		}
	}
	for _, value := range []string{"0.0.0.0:18065", "[::]:18065", "example.test:18065", "127.0.0.1:0", "127.0.0.1:70000", "127.0.0.1:1\n127.0.0.1:2", "garbage"} {
		if _, err := parseLoopbackPublishedPort(value); err == nil {
			t.Fatalf("parseLoopbackPublishedPort(%q) succeeded", value)
		}
	}
}
