// Stripe-powered Premium: public (Funnel) access to drop links is a
// subscription feature (a recurring Stripe price, typically $5/mo). The app
// talks to Stripe over plain HTTPS — no SDK, no new dependencies — using the
// account's secret key; the price is created in the Stripe dashboard and
// referenced by ID (config: stripe_secret_key / stripe_price_id, overridable
// via TAILDROP_STRIPE_SECRET_KEY / TAILDROP_STRIPE_PRICE_ID).
//
// There are no webhooks: subscription state is polled lazily (on Settings
// load, on funnel/drop requests, and on explicit refresh) and cached for
// premiumStaleAfter. That keeps a desktop app's payment state honest without
// needing a public webhook endpoint — Funnel only exposes /drop/* anyway.
// Gating is fail-closed: if we can't verify an active subscription, public
// links are paused.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	stripeAPIBase          = "https://api.stripe.com/v1"
	stripeAPIVersion       = "2024-06-20" // pinned so response shapes are stable
	premiumStaleAfter      = 10 * time.Minute
	premiumRefreshTimeout  = 15 * time.Second
	premiumPollSubsLimit   = 100
	premiumPriceEnvKey     = "TAILDROP_STRIPE_PRICE_ID"
	premiumSecretEnvKey    = "TAILDROP_STRIPE_SECRET_KEY"
)

// premiumInfo is the JSON snapshot served to the UI.
type premiumInfo struct {
	Configured bool   `json:"configured"`
	Active     bool   `json:"active"`
	Status     string `json:"status"` // active | trialing | inactive | unconfigured
	PriceLabel string `json:"priceLabel,omitempty"`
	PeriodEnd  int64  `json:"periodEnd,omitempty"` // unix seconds
}

// premiumState is the cached Stripe subscription state for this install.
type premiumState struct {
	mu         sync.Mutex
	secretKey  string
	priceID    string
	configured bool

	active     bool
	status     string
	priceLabel string
	periodEnd  int64
	customer   string
	subID      string
	lastFetch  time.Time
}

func newPremiumState(secretKey, priceID string) *premiumState {
	p := &premiumState{secretKey: secretKey, priceID: priceID}
	p.configured = secretKey != "" && priceID != ""
	if !p.configured {
		p.status = "unconfigured"
	}
	return p
}

func (p *premiumState) info() premiumInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return premiumInfo{
		Configured: p.configured,
		Active:     p.active,
		Status:     p.status,
		PriceLabel: p.priceLabel,
		PeriodEnd:  p.periodEnd,
	}
}

// refreshIfStale hits Stripe when the cached state is older than
// premiumStaleAfter (or was never fetched). Best-effort: on failure the old
// state stands.
func (p *premiumState) refreshIfStale() {
	p.mu.Lock()
	stale := p.configured && (p.lastFetch.IsZero() || time.Since(p.lastFetch) > premiumStaleAfter)
	p.mu.Unlock()
	if stale {
		p.refresh()
	}
}

// refresh pulls the price label and the active subscription from Stripe and
// replaces the cached state. The account is searched (not a remembered
// customer ID) so the state survives restarts and subscription changes.
func (p *premiumState) refresh() error {
	if !p.configured {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), premiumRefreshTimeout)
	defer cancel()

	label := ""
	if b, err := p.apiCall(ctx, http.MethodGet, "/prices/"+url.PathEscape(p.priceID), nil); err == nil {
		if cents, currency, interval, perr := parsePrice(b); perr == nil {
			label = formatPrice(cents, currency, interval)
		}
	}
	b, err := p.apiCall(ctx, http.MethodGet, fmt.Sprintf("/subscriptions?limit=%d", premiumPollSubsLimit), nil)
	if err != nil {
		return err
	}
	subID, customer, status, periodEnd := findActiveSubscription(b, p.priceID)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastFetch = time.Now()
	p.priceLabel = label
	p.subID = subID
	p.customer = customer
	p.periodEnd = periodEnd
	if subID == "" {
		p.active = false
		p.status = "inactive"
		return nil
	}
	p.active = true
	p.status = status
	return nil
}

// apiCall is a minimal Stripe REST client. A non-2xx response is surfaced as
// an error carrying the API's error body (which includes a message).
func (p *premiumState) apiCall(ctx context.Context, method, path string, form url.Values) ([]byte, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, stripeAPIBase+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.secretKey)
	req.Header.Set("Stripe-Version", stripeAPIVersion)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("stripe: %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	return b, nil
}

// checkoutSessionURL creates a hosted Checkout session for the subscription
// price and returns its URL. The app machine opens it in the browser.
func (p *premiumState) checkoutSessionURL(baseURL string) (string, error) {
	if !p.configured {
		return "", fmt.Errorf("Stripe is not configured — set %s and %s (or stripe_secret_key / stripe_price_id in the app config)", premiumSecretEnvKey, premiumPriceEnvKey)
	}
	ctx, cancel := context.WithTimeout(context.Background(), premiumRefreshTimeout)
	defer cancel()
	b, err := p.apiCall(ctx, http.MethodPost, "/checkout/sessions", p.checkoutForm(baseURL))
	if err != nil {
		return "", err
	}
	var s struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return "", err
	}
	if s.URL == "" {
		return "", fmt.Errorf("stripe checkout returned no url")
	}
	return s.URL, nil
}

// checkoutForm builds the form body for a subscription Checkout session.
// success_url carries ?premium=success so the app UI can toast + refresh when
// the browser lands back on it after payment.
func (p *premiumState) checkoutForm(baseURL string) url.Values {
	form := url.Values{}
	form.Set("mode", "subscription")
	form.Set("line_items[0][price]", p.priceID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("success_url", baseURL+"?premium=success#/settings")
	form.Set("cancel_url", baseURL+"#/settings")
	return form
}

// portalSessionURL creates a billing portal session for the subscribed
// customer (manage/cancel). Refreshes first if no customer is known yet.
func (p *premiumState) portalSessionURL(baseURL string) (string, error) {
	if !p.configured {
		return "", fmt.Errorf("Stripe is not configured")
	}
	p.mu.Lock()
	customer := p.customer
	p.mu.Unlock()
	if customer == "" {
		if err := p.refresh(); err != nil {
			return "", err
		}
		p.mu.Lock()
		customer = p.customer
		p.mu.Unlock()
	}
	if customer == "" {
		return "", fmt.Errorf("no active subscription to manage")
	}
	ctx, cancel := context.WithTimeout(context.Background(), premiumRefreshTimeout)
	defer cancel()
	form := url.Values{}
	form.Set("customer", customer)
	form.Set("return_url", baseURL+"#/settings")
	b, err := p.apiCall(ctx, http.MethodPost, "/billing_portal/sessions", form)
	if err != nil {
		return "", err
	}
	var s struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return "", err
	}
	if s.URL == "" {
		return "", fmt.Errorf("stripe portal returned no url")
	}
	return s.URL, nil
}

// parsePrice extracts the display bits of a Stripe price object.
func parsePrice(b []byte) (cents int64, currency, interval string, err error) {
	var pr struct {
		UnitAmount int64  `json:"unit_amount"`
		Currency   string `json:"currency"`
		Recurring  struct {
			Interval string `json:"interval"`
		} `json:"recurring"`
	}
	if err := json.Unmarshal(b, &pr); err != nil {
		return 0, "", "", err
	}
	return pr.UnitAmount, pr.Currency, pr.Recurring.Interval, nil
}

// formatPrice renders "$5.00/month" (USD) or a plain "<cents> <CURRENCY>"
// for other currencies; an empty interval leaves the "/…" suffix off.
func formatPrice(cents int64, currency, interval string) string {
	per := ""
	switch interval {
	case "month":
		per = "/month"
	case "year":
		per = "/year"
	}
	if currency == "usd" {
		return fmt.Sprintf("$%.2f%s", float64(cents)/100, per)
	}
	return fmt.Sprintf("%d %s%s", cents, strings.ToUpper(currency), per)
}

// findActiveSubscription scans a Stripe subscriptions list for a subscription
// on priceID that is active or trialing. Empty subID means none found.
func findActiveSubscription(b []byte, priceID string) (subID, customer, status string, periodEnd int64) {
	var resp struct {
		Data []struct {
			ID               string `json:"id"`
			Status           string `json:"status"`
			Customer         string `json:"customer"`
			CurrentPeriodEnd int64  `json:"current_period_end"`
			Items            struct {
				Data []struct {
					Price struct {
						ID string `json:"id"`
					} `json:"price"`
				} `json:"data"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return "", "", "", 0
	}
	for _, s := range resp.Data {
		if s.Status != "active" && s.Status != "trialing" {
			continue
		}
		for _, it := range s.Items.Data {
			if it.Price.ID == priceID {
				return s.ID, s.Customer, s.Status, s.CurrentPeriodEnd
			}
		}
	}
	return "", "", "", 0
}

// dropPaywallHTML is served for public (Funnel) drop links when there is no
// active Premium subscription. Billing happens inside the app, so the page
// carries no links and no pricing.
const dropPaywallHTML = `<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Drop link paused</title>
<body style="margin:0;background:#0b0e14;color:#e8ebf3;font-family:system-ui;display:grid;place-items:center;height:100vh">
<main style="max-width:26rem;text-align:center;padding:2rem">
<p style="font-size:15px;line-height:1.6">This drop link is paused.</p>
<p style="font-size:13px;color:#8b93a8;line-height:1.6">Public drop links are a Premium feature. The owner can enable them from the app's Settings.</p>
</main>`

// --- Premium HTTP handlers -------------------------------------------------

func (s *server) handlePremium(w http.ResponseWriter, r *http.Request) {
	s.premium.refreshIfStale()
	writeJSON(w, s.premium.info())
}

func (s *server) handlePremiumRefresh(w http.ResponseWriter, r *http.Request) {
	if err := s.premium.refresh(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, s.premium.info())
}

func (s *server) handlePremiumCheckout(w http.ResponseWriter, r *http.Request) {
	u, err := s.premium.checkoutSessionURL(s.localBaseURL())
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := openPath(u); err != nil {
		writeErr(w, fmt.Errorf("opening checkout: %w", err))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) handlePremiumPortal(w http.ResponseWriter, r *http.Request) {
	u, err := s.premium.portalSessionURL(s.localBaseURL())
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := openPath(u); err != nil {
		writeErr(w, fmt.Errorf("opening billing portal: %w", err))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// premiumBlocks reports whether a public (Funnel) drop request must be
// paywalled. Fail-closed: no verifiable active subscription -> paused.
func (s *server) premiumBlocks() bool {
	s.premium.refreshIfStale()
	return !s.premium.info().Active
}

// localBaseURL is the UI's loopback URL, used for Stripe success/cancel
// redirects so the browser lands back on the Settings page.
func (s *server) localBaseURL() string {
	port := s.port
	if port == 0 {
		port = 8976
	}
	return fmt.Sprintf("http://127.0.0.1:%d/", port)
}
