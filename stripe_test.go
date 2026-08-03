package main

import (
	"strings"
	"testing"
)

func TestFormatPrice(t *testing.T) {
	cases := []struct {
		cents    int64
		currency string
		interval string
		want     string
	}{
		{500, "usd", "month", "$5.00/month"},
		{499, "usd", "month", "$4.99/month"},
		{4200, "usd", "year", "$42.00/year"},
		{500, "usd", "", "$5.00"},
		{500, "eur", "month", "500 EUR/month"},
	}
	for _, c := range cases {
		if got := formatPrice(c.cents, c.currency, c.interval); got != c.want {
			t.Errorf("formatPrice(%d, %q, %q) = %q, want %q", c.cents, c.currency, c.interval, got, c.want)
		}
	}
}

func TestParsePrice(t *testing.T) {
	b := []byte(`{"id":"price_123","unit_amount":500,"currency":"usd","recurring":{"interval":"month"}}`)
	cents, currency, interval, err := parsePrice(b)
	if err != nil {
		t.Fatal(err)
	}
	if cents != 500 || currency != "usd" || interval != "month" {
		t.Errorf("got %d %s %s", cents, currency, interval)
	}
	if _, _, _, err := parsePrice([]byte(`{not json`)); err == nil {
		t.Error("expected error for malformed price JSON")
	}
}

func TestFindActiveSubscription(t *testing.T) {
	body := `{"data":[
		{"id":"sub_other_prod","status":"active","customer":"cus_1","current_period_end":1760000000,
		 "items":{"data":[{"price":{"id":"price_other"}}]}},
		{"id":"sub_canceled","status":"canceled","customer":"cus_2",
		 "items":{"data":[{"price":{"id":"price_premium"}}]}},
		{"id":"sub_premium","status":"active","customer":"cus_3","current_period_end":1761234567,
		 "items":{"data":[{"price":{"id":"price_premium"}}]}},
		{"id":"sub_trial","status":"trialing","customer":"cus_4",
		 "items":{"data":[{"price":{"id":"price_premium"}}]}}
	]}`
	sub, customer, status, periodEnd := findActiveSubscription([]byte(body), "price_premium")
	if sub != "sub_premium" {
		t.Errorf("subID = %q, want sub_premium (first active on our price; canceled/trialing must not shadow it)", sub)
	}
	if customer != "cus_3" || status != "active" || periodEnd != 1761234567 {
		t.Errorf("got customer=%q status=%q periodEnd=%d", customer, status, periodEnd)
	}

	// No subscription on our price -> empty result.
	if sub, _, _, _ := findActiveSubscription([]byte(body), "price_nonexistent"); sub != "" {
		t.Errorf("subID = %q, want empty", sub)
	}

	// Only a trialing subscription -> still active (trial grants access).
	onlyTrial := `{"data":[{"id":"sub_t","status":"trialing","customer":"cus_x",
		"items":{"data":[{"price":{"id":"price_premium"}}]}}]}`
	sub, _, status, _ = findActiveSubscription([]byte(onlyTrial), "price_premium")
	if sub != "sub_t" || status != "trialing" {
		t.Errorf("trialing: got sub=%q status=%q", sub, status)
	}

	// Malformed JSON -> empty, not a panic.
	if sub, _, _, _ := findActiveSubscription([]byte(`{broken`), "price_premium"); sub != "" {
		t.Errorf("malformed: subID = %q, want empty", sub)
	}
}

func TestPremiumStateUnconfigured(t *testing.T) {
	p := newPremiumState("", "")
	info := p.info()
	if info.Configured {
		t.Error("Configured = true with empty keys")
	}
	if info.Active {
		t.Error("Active = true with empty keys")
	}
	if info.Status != "unconfigured" {
		t.Errorf("Status = %q, want unconfigured", info.Status)
	}
}

func TestPremiumStateConfiguredButInactive(t *testing.T) {
	p := newPremiumState("sk_test_x", "price_123")
	info := p.info()
	if !info.Configured {
		t.Error("Configured = false with keys set")
	}
	if info.Active {
		t.Error("Active = true before any fetch")
	}
	if info.Status != "" {
		t.Errorf("Status = %q before fetch, want empty", info.Status)
	}
	// The unconfigured path is a no-op: refresh must never touch the network.
	if err := newPremiumState("", "").refresh(); err != nil {
		t.Errorf("refresh on unconfigured state: %v", err)
	}
}

func TestCheckoutSessionForm(t *testing.T) {
	p := newPremiumState("sk_test_x", "price_123")
	form := p.checkoutForm("http://127.0.0.1:8976/")
	if got := form.Get("mode"); got != "subscription" {
		t.Errorf("mode = %q", got)
	}
	if got := form.Get("line_items[0][price]"); got != "price_123" {
		t.Errorf("price = %q", got)
	}
	if got := form.Get("success_url"); !strings.HasSuffix(got, "?premium=success#/settings") {
		t.Errorf("success_url = %q", got)
	}
	if got := form.Get("cancel_url"); !strings.HasSuffix(got, "#/settings") {
		t.Errorf("cancel_url = %q", got)
	}
}
