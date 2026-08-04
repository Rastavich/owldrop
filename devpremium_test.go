//go:build !production

package main

import "testing"

// TestDevPremiumOverride is compiled only in non-production builds (the
// override itself is excluded from release builds).
func TestDevPremiumOverride(t *testing.T) {
	t.Setenv("TAILDROP_DEV_PREMIUM", "1")
	p := newPremiumState("", "")
	info := p.info()
	if !info.Active || !info.Configured {
		t.Errorf("dev override: got active=%v configured=%v, want both true", info.Active, info.Configured)
	}
	if info.PriceLabel != "development" {
		t.Errorf("dev override: priceLabel = %q, want development", info.PriceLabel)
	}
}
