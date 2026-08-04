// Relay client: in relay mode the app is key-less — all Stripe calls and
// public-drop enforcement happen on the seller's relay, and the app polls it
// for files uploaded through public drop links. The relay enforces Premium
// server-side, so a patched client cannot get free public drops.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultRelayURL is the relay distributed builds point at. Overridden at
// build time via -ldflags "-X main.defaultRelayURL=…" so dev builds stay
// self-host (empty) while release builds default to the seller's relay.
// Config relay_url / TAILDROP_RELAY_URL always win.
var defaultRelayURL = ""

type relayClient struct {
	baseURL string
	key     string
	http    *http.Client
}

func newRelayClient(baseURL, key string) *relayClient {
	return &relayClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		key:     key,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *relayClient) setKey(key string) { c.key = key }

// relayAPIError carries the relay's HTTP status so handlers can re-raise 4xx
// (402 for the premium gate) instead of flattening everything to 500.
type relayAPIError struct {
	Status int
	Msg    string
}

func (e *relayAPIError) Error() string { return e.Msg }

// api performs an authenticated JSON request and returns the response body.
func (c *relayClient) api(ctx context.Context, method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(buf, &e)
		if e.Error != "" {
			return nil, &relayAPIError{Status: res.StatusCode, Msg: e.Error}
		}
		return nil, &relayAPIError{Status: res.StatusCode, Msg: fmt.Sprintf("relay %s: %s", res.Status, strings.TrimSpace(string(buf)))}
	}
	return buf, nil
}

// register creates a device on the relay and returns its API key.
func (c *relayClient) register(ctx context.Context) (string, error) {
	buf, err := c.api(ctx, http.MethodPost, "/api/device/register", struct{}{})
	if err != nil {
		return "", err
	}
	var reg struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.Unmarshal(buf, &reg); err != nil {
		return "", err
	}
	if reg.APIKey == "" {
		return "", fmt.Errorf("relay register returned no key")
	}
	return reg.APIKey, nil
}

// premiumInfo mirrors the relay's /api/billing/status response.
func (c *relayClient) premiumInfo(ctx context.Context) (premiumInfo, error) {
	buf, err := c.api(ctx, http.MethodGet, "/api/billing/status", nil)
	if err != nil {
		return premiumInfo{}, err
	}
	var info premiumInfo
	if err := json.Unmarshal(buf, &info); err != nil {
		return premiumInfo{}, err
	}
	return info, nil
}

// refreshBilling forces the relay to re-check the subscription.
func (c *relayClient) refreshBilling(ctx context.Context) (premiumInfo, error) {
	buf, err := c.api(ctx, http.MethodPost, "/api/billing/refresh", struct{}{})
	if err != nil {
		return premiumInfo{}, err
	}
	var info premiumInfo
	if err := json.Unmarshal(buf, &info); err != nil {
		return premiumInfo{}, err
	}
	return info, nil
}

func (c *relayClient) checkoutURL(ctx context.Context, successURL, cancelURL string) (string, error) {
	buf, err := c.api(ctx, http.MethodPost, "/api/billing/checkout", map[string]any{
		"successUrl": successURL,
		"cancelUrl":  cancelURL,
	})
	if err != nil {
		return "", err
	}
	var s struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(buf, &s); err != nil {
		return "", err
	}
	if s.URL == "" {
		return "", fmt.Errorf("relay checkout returned no url")
	}
	return s.URL, nil
}

func (c *relayClient) portalURL(ctx context.Context, returnURL string) (string, error) {
	buf, err := c.api(ctx, http.MethodPost, "/api/billing/portal", map[string]any{"returnUrl": returnURL})
	if err != nil {
		return "", err
	}
	var s struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(buf, &s); err != nil {
		return "", err
	}
	if s.URL == "" {
		return "", fmt.Errorf("relay portal returned no url")
	}
	return s.URL, nil
}

// --- drop links (relay-hosted) --------------------------------------------

// relayLink is the wire shape of a relay drop link (matches the app's
// DropLink JSON so the Settings UI is unchanged).
type relayLink struct {
	Token     string `json:"token"`
	Name      string `json:"name"`
	Expires   string `json:"expires"`
	MaxUses   int    `json:"maxUses"`
	Uses      int    `json:"uses"`
	Revoked   bool   `json:"revoked"`
	Expired   bool   `json:"expired"`
	URL       string `json:"url"`
	PublicURL string `json:"publicUrl"`
}

func (c *relayClient) createLink(ctx context.Context, name string, ttlMin, maxUses int) (relayLink, error) {
	buf, err := c.api(ctx, http.MethodPost, "/api/drops", map[string]any{
		"name": name, "ttlMinutes": ttlMin, "maxUses": maxUses,
	})
	if err != nil {
		return relayLink{}, err
	}
	var l relayLink
	if err := json.Unmarshal(buf, &l); err != nil {
		return relayLink{}, err
	}
	return l, nil
}

func (c *relayClient) linkList(ctx context.Context) ([]relayLink, error) {
	buf, err := c.api(ctx, http.MethodGet, "/api/drops", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Links []relayLink `json:"links"`
	}
	if err := json.Unmarshal(buf, &resp); err != nil {
		return nil, err
	}
	return resp.Links, nil
}

func (c *relayClient) revokeLink(ctx context.Context, token string) error {
	_, err := c.api(ctx, http.MethodPost, "/api/drops/"+token+"/revoke", struct{}{})
	return err
}

// --- delivery --------------------------------------------------------------

// deliveryManifest describes one queued upload waiting on the relay.
type deliveryManifest struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
}

// pollDeliveries long-polls the relay for queued uploads.
func (c *relayClient) pollDeliveries(ctx context.Context) ([]deliveryManifest, error) {
	buf, err := c.api(ctx, http.MethodGet, "/api/device/poll", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Deliveries []deliveryManifest `json:"deliveries"`
	}
	if err := json.Unmarshal(buf, &resp); err != nil {
		return nil, err
	}
	return resp.Deliveries, nil
}

// openDelivery streams one queued upload's bytes from the relay.
func (c *relayClient) openDelivery(ctx context.Context, id string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/device/deliver/"+id, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if res.StatusCode >= 400 {
		res.Body.Close()
		return nil, 0, fmt.Errorf("relay deliver: HTTP %d", res.StatusCode)
	}
	return res.Body, res.ContentLength, nil
}
