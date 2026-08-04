// Stripe billing for the relay. The seller's secret key lives here (server
// side) — clients never see it. Subscriptions are checked on demand and
// cached for a couple of minutes; the device<->customer link is established
// lazily from the checkout session's client_reference_id, so no webhooks are
// needed.
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
	stripeAPIBase     = "https://api.stripe.com/v1"
	stripeAPIVersion  = "2024-06-20"
	premiumCacheAfter = 2 * time.Minute
	priceCacheAfter   = time.Hour
)

type billing struct {
	secretKey string
	priceID   string

	mu         sync.Mutex
	priceLabel string
	priceAt    time.Time

	// per-device subscription cache
	statuses map[string]billingStatus
}

type billingStatus struct {
	configured bool
	active     bool
	status     string
	periodEnd  int64
	checked    time.Time
}

func newBilling(secretKey, priceID string) *billing {
	return &billing{
		secretKey: secretKey,
		priceID:   priceID,
		statuses:  map[string]billingStatus{},
	}
}

// premium reports whether the device has an active subscription, checking
// Stripe when the cache is stale. Fail-closed: an unverifiable subscription
// is not premium.
func (b *billing) premium(s *store, deviceID string) bool {
	st := b.status(s, deviceID)
	return st.active
}

// status returns the device's subscription state; links the device to a
// Stripe customer on first check (via the checkout session's
// client_reference_id).
func (b *billing) status(s *store, deviceID string) billingStatus {
	if b.secretKey == "" || b.priceID == "" {
		return billingStatus{}
	}
	b.mu.Lock()
	st, ok := b.statuses[deviceID]
	b.mu.Unlock()
	if ok && time.Since(st.checked) < premiumCacheAfter {
		return st
	}
	b.refresh(s, deviceID)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.statuses[deviceID]
}

// refresh performs the Stripe round-trips. Callers must not hold b.mu.
func (b *billing) refresh(s *store, deviceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dev := s.deviceByID(deviceID)
	if dev == nil {
		return
	}
	if dev.Customer == "" {
		if cus := b.findCustomerByDevice(ctx, deviceID); cus != "" {
			if err := s.setCustomer(deviceID, cus); err == nil {
				dev.Customer = cus
			}
		}
	}

	st := billingStatus{configured: true, status: "inactive", checked: time.Now()}
	if dev.Customer != "" {
		if subID, statusStr, _, periodEnd := b.findSubscription(ctx, dev.Customer); subID != "" {
			st.active = true
			st.status = statusStr
			st.periodEnd = periodEnd
		}
	}
	b.mu.Lock()
	b.statuses[deviceID] = st
	b.mu.Unlock()
}

// findCustomerByDevice scans completed checkout sessions carrying the device
// ID as client_reference_id and returns the paying customer, if any.
func (b *billing) findCustomerByDevice(ctx context.Context, deviceID string) string {
	path := "/checkout/sessions?client_reference_id=" + url.QueryEscape(deviceID) + "&limit=10"
	body, err := b.apiCall(ctx, http.MethodGet, path, nil)
	if err != nil {
		return ""
	}
	var resp struct {
		Data []struct {
			PaymentStatus string `json:"payment_status"`
			Customer      string `json:"customer"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return ""
	}
	for _, cs := range resp.Data {
		if cs.PaymentStatus == "paid" && cs.Customer != "" {
			return cs.Customer
		}
	}
	return ""
}

func (b *billing) findSubscription(ctx context.Context, customer string) (subID, status string, active bool, periodEnd int64) {
	path := "/subscriptions?customer=" + url.QueryEscape(customer) + "&limit=100"
	body, err := b.apiCall(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", "", false, 0
	}
	var resp struct {
		Data []struct {
			ID               string `json:"id"`
			Status           string `json:"status"`
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
	if json.Unmarshal(body, &resp) != nil {
		return "", "", false, 0
	}
	for _, sub := range resp.Data {
		if sub.Status != "active" && sub.Status != "trialing" {
			continue
		}
		for _, it := range sub.Items.Data {
			if it.Price.ID == b.priceID {
				return sub.ID, sub.Status, true, sub.CurrentPeriodEnd
			}
		}
	}
	return "", "", false, 0
}

// label returns the formatted price label ("$5.00/month"), cached.
func (b *billing) label() string {
	if b.secretKey == "" || b.priceID == "" {
		return ""
	}
	b.mu.Lock()
	if time.Since(b.priceAt) < priceCacheAfter && b.priceLabel != "" {
		l := b.priceLabel
		b.mu.Unlock()
		return l
	}
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	body, err := b.apiCall(ctx, http.MethodGet, "/prices/"+url.PathEscape(b.priceID), nil)
	if err != nil {
		return b.priceLabel
	}
	var pr struct {
		UnitAmount int64  `json:"unit_amount"`
		Currency   string `json:"currency"`
		Recurring  struct {
			Interval string `json:"interval"`
		} `json:"recurring"`
	}
	if json.Unmarshal(body, &pr) != nil {
		return b.priceLabel
	}
	label := formatPrice(pr.UnitAmount, pr.Currency, pr.Recurring.Interval)
	b.mu.Lock()
	b.priceLabel = label
	b.priceAt = time.Now()
	b.mu.Unlock()
	return label
}

// checkoutURL creates a subscription Checkout session for the device and
// returns the hosted URL. client_reference_id ties the customer back to the
// device later (see findCustomerByDevice).
func (b *billing) checkoutURL(deviceID, successURL, cancelURL string) (string, error) {
	if b.secretKey == "" || b.priceID == "" {
		return "", fmt.Errorf("billing is not configured on the relay")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	form := url.Values{}
	form.Set("mode", "subscription")
	form.Set("line_items[0][price]", b.priceID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("client_reference_id", deviceID)
	form.Set("success_url", successURL)
	form.Set("cancel_url", cancelURL)
	body, err := b.apiCall(ctx, http.MethodPost, "/checkout/sessions", form)
	if err != nil {
		return "", err
	}
	var s struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return "", err
	}
	if s.URL == "" {
		return "", fmt.Errorf("stripe checkout returned no url")
	}
	return s.URL, nil
}

func (b *billing) portalURL(s *store, deviceID, returnURL string) (string, error) {
	if b.secretKey == "" || b.priceID == "" {
		return "", fmt.Errorf("billing is not configured on the relay")
	}
	// A refresh may lazily link the device to its customer.
	b.status(s, deviceID)
	dev := s.deviceByID(deviceID)
	if dev == nil || dev.Customer == "" {
		return "", fmt.Errorf("no active subscription to manage")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	form := url.Values{}
	form.Set("customer", dev.Customer)
	form.Set("return_url", returnURL)
	body, err := b.apiCall(ctx, http.MethodPost, "/billing_portal/sessions", form)
	if err != nil {
		return "", err
	}
	var sess struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &sess); err != nil {
		return "", err
	}
	if sess.URL == "" {
		return "", fmt.Errorf("stripe portal returned no url")
	}
	return sess.URL, nil
}

// --- raw Stripe client ----------------------------------------------------

func (b *billing) apiCall(ctx context.Context, method, path string, form url.Values) ([]byte, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, stripeAPIBase+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.secretKey)
	req.Header.Set("Stripe-Version", stripeAPIVersion)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("stripe: %s: %s", res.Status, strings.TrimSpace(string(buf)))
	}
	return buf, nil
}

func formatPrice(cents int64, currency, interval string) string {
	per := ""
	switch interval {
	case "month":
		per = "/month"
	case "year":
		per = "/year"
	}
	switch currency {
	case "usd", "aud", "cad", "nzd":
		return fmt.Sprintf("$%.2f%s", float64(cents)/100, per)
	}
	return fmt.Sprintf("%d %s%s", cents, strings.ToUpper(currency), per)
}
