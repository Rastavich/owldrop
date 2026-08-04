//go:build !production

// Dev-only Premium override: with TAILDROP_DEV_PREMIUM=1 a local build
// (plain `go build`, no -tags production) reports Premium as active without
// any Stripe keys — for development only.
//
// Compiled out of release builds (the flake and platform taskfiles build
// with -tags production, see devpremium_prod.go), so shipped binaries can
// never use it. Relay mode is unaffected either way: the relay enforces
// Premium server-side on every public request.
package main

import "os"

func devPremium() bool {
	return os.Getenv("TAILDROP_DEV_PREMIUM") == "1"
}
