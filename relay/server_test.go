package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestRelay builds a relay with an injectable premium decision and a temp
// data dir.
func newTestRelay(t *testing.T, premium func(string) bool) (*relay, *httptest.Server) {
	t.Helper()
	st, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &relay{
		store:        st,
		billing:      newBilling("", ""),
		baseURL:      "https://relay.test",
		premiumCheck: premium,
	}
	ts := httptest.NewServer(r.routes())
	t.Cleanup(ts.Close)
	return r, ts
}

func TestRegisterAndAuth(t *testing.T) {
	_, ts := newTestRelay(t, nil)

	key := register(t, ts)
	if key == "" {
		t.Fatal("empty api key")
	}

	// Unauthenticated access is rejected.
	resp, err := http.Get(ts.URL + "/api/drops")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth drops: %d, want 401", resp.StatusCode)
	}

	// Authenticated access works.
	resp = authed(t, ts, key, "GET", "/api/drops", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("authed drops: %d", resp.StatusCode)
	}
}

func TestDropLifecycle(t *testing.T) {
	premium := map[string]bool{}
	r, ts := newTestRelay(t, func(id string) bool { return premium[id] })
	key := register(t, ts)
	devID := r.store.deviceByKey(key).ID

	// Creating a link requires premium.
	resp := authed(t, ts, key, "POST", "/api/drops", `{"name":"alice","ttlMinutes":60,"maxUses":1}`)
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("create link without premium: %d, want 402", resp.StatusCode)
	}
	resp.Body.Close()

	// Premium now.
	premium[devID] = true
	create := authed(t, ts, key, "POST", "/api/drops", `{"name":"alice","ttlMinutes":60,"maxUses":1}`)
	var created struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	create.Body.Close()
	token := created.URL[strings.LastIndex(created.URL, "/")+1:]
	if token == "" {
		t.Fatal("no token in created url")
	}

	// Public page serves the upload form for a premium owner.
	page, err := http.Get(ts.URL + "/drop/" + token)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if !strings.Contains(string(body), "Send a file") {
		t.Fatalf("expected upload page, got %q", body)
	}

	// Upload a file.
	up := upload(t, ts, token, false)
	if up.StatusCode != 200 {
		t.Fatalf("upload: %d", up.StatusCode)
	}
	up.Body.Close()

	// Device polls and receives the manifest.
	poll := authed(t, ts, key, "GET", "/api/device/poll", "")
	var polled struct {
		Deliveries []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"deliveries"`
	}
	if err := json.NewDecoder(poll.Body).Decode(&polled); err != nil {
		t.Fatal(err)
	}
	poll.Body.Close()
	if len(polled.Deliveries) != 1 || polled.Deliveries[0].Name != "hello.txt" {
		t.Fatalf("poll: %+v", polled.Deliveries)
	}

	// Device downloads the file.
	dl := authed(t, ts, key, "GET", "/api/device/deliver/"+polled.Deliveries[0].ID, "")
	got, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	if string(got) != "hello relay" {
		t.Fatalf("delivered %q", got)
	}

	// Second use of a maxUses=1 link is refused at the page level.
	page2, err := http.Get(ts.URL + "/drop/" + token)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(page2.Body)
	page2.Body.Close()
	if !strings.Contains(string(body2), "already been used") {
		t.Fatalf("expected used-link message, got %q", body2)
	}
}

func TestPaywallWithoutPremium(t *testing.T) {
	r, ts := newTestRelay(t, func(string) bool { return false })
	key := register(t, ts)

	create := authed(t, ts, key, "POST", "/api/drops", `{"name":"x","ttlMinutes":60}`)
	if create.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("create: %d, want 402", create.StatusCode)
	}
	create.Body.Close()

	// Seed a link directly in the store, then verify the page is paywalled.
	dev := r.store.deviceByKey(key)
	l := &link{Token: "tok", DeviceID: dev.ID, Name: "x", Expires: time.Now().Add(time.Hour), Created: time.Now()}
	if err := r.store.addLink(l); err != nil {
		t.Fatal(err)
	}

	page, err := http.Get(ts.URL + "/drop/tok")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if !strings.Contains(string(body), "paused") {
		t.Fatalf("expected paywall, got %q", body)
	}

	up := upload(t, ts, "tok", true)
	upBody, _ := io.ReadAll(up.Body)
	up.Body.Close()
	if up.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("upload without premium: %d %s, want 402", up.StatusCode, upBody)
	}
}

func TestFolderUploadZips(t *testing.T) {
	r, ts := newTestRelay(t, func(string) bool { return true })
	key := register(t, ts)
	devID := r.store.deviceByKey(key).ID
	_ = devID

	create := authed(t, ts, key, "POST", "/api/drops", `{"name":"d","ttlMinutes":60}`)
	var created struct {
		URL string `json:"url"`
	}
	json.NewDecoder(create.Body).Decode(&created)
	create.Body.Close()
	token := created.URL[strings.LastIndex(created.URL, "/")+1:]

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.txt")
	fw.Write([]byte("A"))
	fw2, _ := mw.CreateFormFile("file", "b.txt")
	fw2.Write([]byte("B"))
	mw.WriteField("path", "docs/a.txt")
	mw.WriteField("path", "docs/b.txt")
	mw.Close()
	up, err := http.Post(ts.URL+"/drop/"+token+"/upload", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	upBody, _ := io.ReadAll(up.Body)
	up.Body.Close()
	if up.StatusCode != 200 {
		t.Fatalf("folder upload: %d %s", up.StatusCode, upBody)
	}

	poll := authed(t, ts, key, "GET", "/api/device/poll", "")
	var polled struct {
		Deliveries []struct {
			Name string `json:"name"`
		} `json:"deliveries"`
	}
	json.NewDecoder(poll.Body).Decode(&polled)
	poll.Body.Close()
	if len(polled.Deliveries) != 1 || polled.Deliveries[0].Name != "docs.zip" {
		t.Fatalf("expected zipped folder, got %+v", polled.Deliveries)
	}
}

func TestFormatPrice(t *testing.T) {
	cases := []struct {
		cents    int64
		currency string
		interval string
		want     string
	}{
		{500, "usd", "month", "$5.00/month"},
		{4200, "usd", "year", "$42.00/year"},
		{500, "usd", "", "$5.00"},
		{500, "aud", "month", "$5.00/month"},
		{500, "eur", "month", "500 EUR/month"},
	}
	for _, c := range cases {
		if got := formatPrice(c.cents, c.currency, c.interval); got != c.want {
			t.Errorf("formatPrice(%d,%q,%q) = %q, want %q", c.cents, c.currency, c.interval, got, c.want)
		}
	}
}

// --- helpers ---------------------------------------------------------------

func register(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/device/register", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reg struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		t.Fatal(err)
	}
	return reg.APIKey
}

func authed(t *testing.T, ts *httptest.Server, key, method, path, jsonBody string) *http.Response {
	t.Helper()
	var body io.Reader
	if jsonBody != "" {
		body = strings.NewReader(jsonBody)
	}
	req, _ := http.NewRequest(method, ts.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+key)
	if jsonBody != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// upload posts a small file to the public drop endpoint.
func upload(t *testing.T, ts *httptest.Server, token string, empty bool) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if !empty {
		fw, _ := mw.CreateFormFile("file", "hello.txt")
		fw.Write([]byte("hello relay"))
		mw.WriteField("path", "hello.txt")
	}
	mw.Close()
	resp, err := http.Post(ts.URL+"/drop/"+token+"/upload", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
