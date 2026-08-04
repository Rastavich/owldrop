// Taildrop relay — the seller-controlled server that makes public drops a
// paid, server-enforced feature:
//
//   - device registration + API-key auth for installed apps
//   - billing proxy (Stripe secret key lives here, never in clients)
//   - public /drop/<token> pages with a server-side premium check on every
//     request (a patched client cannot bypass this)
//   - queued delivery of uploads to the owning device via long-polling
//
// Deploy: fly.io (fly.toml) or any container host. Set STRIPE_SECRET_KEY,
// STRIPE_PRICE_ID and BASE_URL via the host's secrets/env.
package main

import (
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:" + port
	}

	st, err := newStore(dataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	r := &relay{
		store:   st,
		billing: newBilling(os.Getenv("STRIPE_SECRET_KEY"), stripePriceID()),
		baseURL: strings.TrimSuffix(baseURL, "/"),
	}

	log.Printf("taildrop relay on :%s (data %s, base %s)", port, dataDir, baseURL)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r.routes(),
		ReadHeaderTimeout: 5e9, // 5s
		IdleTimeout:       120e9,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// stripePriceID returns the Stripe price ID env var, accepting both
// STRIPE_PRICE_ID and STRIPE_PRICE (the deployed Fly secret was created with
// the shorter name).
func stripePriceID() string {
	if v := os.Getenv("STRIPE_PRICE_ID"); v != "" {
		return v
	}
	return os.Getenv("STRIPE_PRICE")
}
