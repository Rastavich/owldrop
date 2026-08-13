package main

import "strings"

// shareableDropURL returns the URL a recipient can actually open: the Funnel
// public URL when one exists, otherwise the tailnet/local URL. Creating a
// link used to copy the local URL even when Funnel was on.
func shareableDropURL(local, public string) string {
	if public != "" {
		return public
	}
	return local
}

// isTaggedTaildropBlock reports whether a Taildrop unavailability reason is
// the tagged-node / other-user case (Tailscale will not Taildrop to tagged
// devices). Used to offer a drop-link workaround instead of a dead picker row.
func isTaggedTaildropBlock(reason string) bool {
	r := strings.ToLower(reason)
	if r == "" || r == "available" {
		return false
	}
	if strings.Contains(r, "tag") {
		return true
	}
	// Daemon wording when the peer is tagged (no user owner).
	return r == "owned by another user"
}
