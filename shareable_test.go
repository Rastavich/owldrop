package main

import "testing"

func TestShareableDropURL(t *testing.T) {
	local := "http://127.0.0.1:8976/drop/abc"
	pub := "https://box.tailnet.ts.net/drop/abc"
	if got := shareableDropURL(local, pub); got != pub {
		t.Fatalf("with public: got %q", got)
	}
	if got := shareableDropURL(local, ""); got != local {
		t.Fatalf("without public: got %q", got)
	}
}

func TestIsTaggedTaildropBlock(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		{"available", false},
		{"", false},
		{"offline", false},
		{"owned by another user", true},
		{"tagged device", true},
		{"missing taildrop capability", false},
		{"os does not support taildrop", false},
	}
	for _, c := range cases {
		if got := isTaggedTaildropBlock(c.reason); got != c.want {
			t.Errorf("isTaggedTaildropBlock(%q) = %v, want %v", c.reason, got, c.want)
		}
	}
}
