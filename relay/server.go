// Relay HTTP server: device auth, billing proxy, public drop pages with
// server-side premium enforcement, and queued delivery to devices.
package main

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxDropUploadSize = 4 << 30 // 4 GiB, matching the desktop app
	pollTimeout       = 25 * time.Second
)

type relay struct {
	store   *store
	billing *billing
	baseURL string
	// premiumCheck overrides the server-side premium decision (tests inject
	// a fake; production uses billing).
	premiumCheck func(deviceID string) bool
}

// isPremium is the single premium decision point for all public surfaces.
func (r *relay) isPremium(deviceID string) bool {
	if r.premiumCheck != nil {
		return r.premiumCheck(deviceID)
	}
	return r.billing.premium(r.store, deviceID)
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func newAPIKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// --- device auth -----------------------------------------------------------

// deviceFromRequest resolves the API key from the Authorization header.
func (r *relay) deviceFromRequest(h http.Header) (*device, bool) {
	auth := h.Get("Authorization")
	key, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || key == "" {
		return nil, false
	}
	d := r.store.deviceByKey(key)
	return d, d != nil
}

// auth wraps a handler with device-key auth.
func (r *relay) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		dev, ok := r.deviceFromRequest(req.Header)
		if !ok {
			writeJSONStatus(w, http.StatusUnauthorized, map[string]any{"error": "invalid device key"})
			return
		}
		next(w, req.WithContext(contextWithDevice(req, dev)))
	}
}

// routes builds the HTTP mux.
func (r *relay) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/api/device/register", r.handleRegister)
	mux.HandleFunc("/api/device/poll", r.auth(r.handlePoll))
	mux.HandleFunc("/api/device/deliver/", r.auth(r.handleDeliver))
	mux.HandleFunc("/api/billing/status", r.auth(r.handleBillingStatus))
	mux.HandleFunc("/api/billing/refresh", r.auth(r.handleBillingRefresh))
	mux.HandleFunc("/api/billing/checkout", r.auth(r.handleCheckout))
	mux.HandleFunc("/api/billing/portal", r.auth(r.handlePortal))
	mux.HandleFunc("/api/drops", r.auth(r.handleDropLinks))
	mux.HandleFunc("/api/drops/", r.auth(r.handleRevoke))
	mux.HandleFunc("/drop/", r.handleDropPageOrUpload)
	return mux
}

// --- handlers --------------------------------------------------------------

func (r *relay) handleRegister(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d := &device{ID: newToken(), Key: newAPIKey(), Created: time.Now()}
	if err := r.store.addDevice(d); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"deviceId": d.ID, "apiKey": d.Key})
}

func (r *relay) handleBillingStatus(w http.ResponseWriter, req *http.Request) {
	dev := deviceFromContext(req)
	st := r.billing.status(r.store, dev.ID)
	writeJSON(w, map[string]any{
		"configured": st.configured,
		"active":     st.active,
		"status":     map[bool]string{true: st.status, false: "inactive"}[st.active || st.status != ""],
		"priceLabel": r.billing.label(),
		"periodEnd":  st.periodEnd,
	})
}

// handleBillingRefresh clears the cached subscription state and re-checks
// (the Settings page's Refresh button needs an actual round-trip).
func (r *relay) handleBillingRefresh(w http.ResponseWriter, req *http.Request) {
	dev := deviceFromContext(req)
	r.billing.mu.Lock()
	delete(r.billing.statuses, dev.ID)
	r.billing.mu.Unlock()
	r.handleBillingStatus(w, req)
}

func (r *relay) handleCheckout(w http.ResponseWriter, req *http.Request) {
	dev := deviceFromContext(req)
	var body struct {
		SuccessURL string `json:"successUrl"`
		CancelURL  string `json:"cancelUrl"`
	}
	if err := decodeJSON(req, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.SuccessURL == "" || body.CancelURL == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "successUrl and cancelUrl are required"})
		return
	}
	u, err := r.billing.checkoutURL(dev.ID, body.SuccessURL, body.CancelURL)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"url": u})
}

func (r *relay) handlePortal(w http.ResponseWriter, req *http.Request) {
	dev := deviceFromContext(req)
	var body struct {
		ReturnURL string `json:"returnUrl"`
	}
	if err := decodeJSON(req, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.ReturnURL == "" {
		body.ReturnURL = "https://taildrop.app/"
	}
	u, err := r.billing.portalURL(r.store, dev.ID, body.ReturnURL)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"url": u})
}

func (r *relay) handleDropLinks(w http.ResponseWriter, req *http.Request) {
	dev := deviceFromContext(req)
	switch req.Method {
	case http.MethodGet:
		rows := make([]map[string]any, 0)
		for _, l := range r.store.deviceLinks(dev.ID) {
			rows = append(rows, r.linkRow(l))
		}
		writeJSON(w, map[string]any{"links": rows, "baseUrl": r.baseURL + "/"})
	case http.MethodPost:
		// Creating a public link is itself a premium action.
		if !r.isPremium(dev.ID) {
			writeJSONStatus(w, http.StatusPaymentRequired, map[string]any{"error": "public drops are a Premium feature — subscribe first"})
			return
		}
		var reqBody struct {
			Name    string `json:"name"`
			TTLMin  int    `json:"ttlMinutes"`
			MaxUses int    `json:"maxUses"`
		}
		if err := decodeJSON(req, &reqBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if reqBody.TTLMin <= 0 {
			reqBody.TTLMin = 60
		}
		if reqBody.TTLMin > 7*24*60 {
			reqBody.TTLMin = 7 * 24 * 60
		}
		l := &link{
			Token:    newToken(),
			DeviceID: dev.ID,
			Name:     reqBody.Name,
			Expires:  time.Now().Add(time.Duration(reqBody.TTLMin) * time.Minute),
			MaxUses:  reqBody.MaxUses,
			Created:  time.Now(),
		}
		if err := r.store.addLink(l); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"link": r.linkRow(l), "url": r.baseURL + "/drop/" + l.Token, "publicUrl": r.baseURL + "/drop/" + l.Token})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (r *relay) handleRevoke(w http.ResponseWriter, req *http.Request) {
	token := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/api/drops/"), "/revoke")
	if req.Method != http.MethodPost || token == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := r.store.setRevoked(token); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (r *relay) linkRow(l *link) map[string]any {
	return map[string]any{
		"token":     l.Token,
		"name":      l.Name,
		"expires":   l.Expires.Format(time.RFC3339),
		"maxUses":   l.MaxUses,
		"uses":      l.Uses,
		"revoked":   l.Revoked,
		"expired":   !l.Expires.IsZero() && time.Now().After(l.Expires),
		"url":       r.baseURL + "/drop/" + l.Token,
		"publicUrl": r.baseURL + "/drop/" + l.Token,
	}
}

// handleDropPageOrUpload serves the public pages. Premium is enforced HERE,
// server-side, on every request — the owning device's binary cannot bypass
// it (this is the whole point of the relay).
func (r *relay) handleDropPageOrUpload(w http.ResponseWriter, req *http.Request) {
	token := dropTokenFromPath(req.URL.Path)
	l := r.store.linkByToken(token)
	if l == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>Drop link</title>"+
			"<body style='font-family:system-ui;background:#0b0e14;color:#e8ebf3;display:grid;place-items:center;height:100vh;margin:0'>"+
			"<p style='font-size:15px'>This drop link doesn't exist.</p></body>")
		return
	}
	if ok, msg := r.store.usable(token); !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>Drop link</title>"+
			"<body style='font-family:system-ui;background:#0b0e14;color:#e8ebf3;display:grid;place-items:center;height:100vh;margin:0'>"+
			"<p style='font-size:15px'>%s</p></body>", htmlEscape(msg))
		return
	}

	// Server-side premium check for the owning device.
	if !r.isPremium(l.DeviceID) {
		if req.Method == http.MethodPost {
			writeJSONStatus(w, http.StatusPaymentRequired, map[string]any{"error": "public drops are a Premium feature"})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		io.WriteString(w, dropPaywallHTML)
		return
	}

	if req.Method == http.MethodGet {
		html := strings.ReplaceAll(dropPageHTML, "__NAME__", htmlEscape(l.Name))
		html = strings.ReplaceAll(html, "__EXPIRES__", l.Expires.Format("Mon 2 Jan 15:04"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		io.WriteString(w, html)
		return
	}
	if req.Method == http.MethodPost {
		r.receiveUpload(w, req, l)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// receiveUpload stores the multipart upload and queues it for the owner's
// device to poll. Folders (path parts containing "/") are zipped into one
// item, mirroring the desktop app.
func (r *relay) receiveUpload(w http.ResponseWriter, req *http.Request, l *link) {
	req.Body = http.MaxBytesReader(w, req.Body, maxDropUploadSize)
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "upload too large or malformed"})
		return
	}
	parts := req.MultipartForm.File["file"]
	if len(parts) == 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "no file part named \"file\""})
		return
	}
	paths := req.MultipartForm.Value["path"]

	files := make([]*delivery, 0, len(parts))
	if containsSlash(paths) {
		// Folder drop: zip every part into one delivery named after the
		// top-level folder (mirrors the desktop app).
		d, err := r.storeFolderZip(l, parts, paths)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		files = append(files, d)
	} else {
		dir := filepath.Join(r.store.dir, "uploads", l.DeviceID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			writeErr(w, err)
			return
		}
		for _, p := range parts {
			f, err := p.Open()
			if err != nil {
				writeErr(w, err)
				return
			}
			base := filepath.Base(p.Filename)
			if base == "." || base == "" {
				base = "upload.bin"
			}
			d := &delivery{
				ID: newToken(), DeviceID: l.DeviceID, Token: l.Token,
				Name: base, Size: p.Size, Queued: time.Now(),
			}
			out, err := os.OpenFile(filepath.Join(dir, d.ID+"_"+d.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				f.Close()
				writeErr(w, err)
				return
			}
			n, err := io.Copy(out, f)
			f.Close()
			out.Close()
			if err != nil {
				os.Remove(out.Name())
				writeErr(w, err)
				return
			}
			d.Size = n
			files = append(files, d)
		}
	}

	r.store.useOnce(l.Token)
	for _, d := range files {
		r.store.enqueue(d)
	}
	names := make([]string, 0, len(files))
	var total int64
	for _, d := range files {
		names = append(names, d.Name)
		total += d.Size
	}
	writeJSON(w, map[string]any{"ok": true, "names": names, "size": total})
}

func containsSlash(paths []string) bool {
	for _, p := range paths {
		if strings.Contains(p, "/") {
			return true
		}
	}
	return false
}

// storeFolderZip zips dropped folder parts into one delivery named after the
// top-level folder. paths[i] is the relative path for parts[i]; empty falls
// back to the part's basename.
func (r *relay) storeFolderZip(l *link, parts []*multipart.FileHeader, paths []string) (*delivery, error) {
	zipName := "folder.zip"
	for _, p := range paths {
		if top := strings.SplitN(sanitizeZipPath(p), "/", 2)[0]; top != "" {
			zipName = top
			break
		}
	}
	zipName += ".zip"
	dir := filepath.Join(r.store.dir, "uploads", l.DeviceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	d := &delivery{
		ID: newToken(), DeviceID: l.DeviceID, Token: l.Token,
		Name: zipName, Queued: time.Now(),
	}
	out, err := os.OpenFile(filepath.Join(dir, d.ID+"_"+d.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	zw := zip.NewWriter(out)
	var total int64
	for i, p := range parts {
		entry := ""
		if i < len(paths) {
			entry = sanitizeZipPath(paths[i])
		}
		if entry == "" {
			entry = filepath.Base(p.Filename)
		}
		w, err := zw.Create(entry)
		if err != nil {
			out.Close()
			os.Remove(out.Name())
			return nil, err
		}
		f, err := p.Open()
		if err != nil {
			out.Close()
			os.Remove(out.Name())
			return nil, err
		}
		n, err := io.Copy(w, f)
		f.Close()
		if err != nil {
			out.Close()
			os.Remove(out.Name())
			return nil, err
		}
		total += n
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(out.Name())
		return nil, err
	}
	if err := out.Close(); err != nil {
		os.Remove(out.Name())
		return nil, err
	}
	d.Size = total
	return d, nil
}

// --- delivery --------------------------------------------------------------

// handlePoll long-polls for queued uploads. Returns immediately if any are
// pending, otherwise waits up to pollTimeout for one to arrive.
func (r *relay) handlePoll(w http.ResponseWriter, req *http.Request) {
	dev := deviceFromContext(req)
	if n := r.store.pendingCount(dev.ID); n > 0 {
		r.writeManifests(w, dev.ID)
		return
	}
	timeout := time.NewTimer(pollTimeout)
	defer timeout.Stop()
	for {
		select {
		case <-req.Context().Done():
			writeJSON(w, map[string]any{"deliveries": []any{}})
			return
		case <-timeout.C:
			writeJSON(w, map[string]any{"deliveries": []any{}})
			return
		case <-r.store.wake:
			if n := r.store.pendingCount(dev.ID); n > 0 {
				r.writeManifests(w, dev.ID)
				return
			}
		}
	}
}

func (r *relay) writeManifests(w http.ResponseWriter, deviceID string) {
	items := r.store.takePending(deviceID)
	rows := make([]map[string]any, 0, len(items))
	for _, d := range items {
		rows = append(rows, map[string]any{
			"id": d.ID, "token": d.Token, "name": d.Name, "size": d.Size,
		})
	}
	writeJSON(w, map[string]any{"deliveries": rows})
}

// handleDeliver streams one queued file to the device and removes it.
func (r *relay) handleDeliver(w http.ResponseWriter, req *http.Request) {
	dev := deviceFromContext(req)
	id := strings.TrimPrefix(req.URL.Path, "/api/device/deliver/")
	d := r.store.take(dev.ID, id)
	if d == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"error": "delivery not found"})
		return
	}
	path := r.store.deliveryPath(dev.ID, d.ID, d.Name)
	f, err := os.Open(path)
	if err != nil {
		r.store.done(dev.ID, d.ID)
		writeErr(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeHeader(d.Name)+`"`)
	w.Header().Set("X-Drop-Name", sanitizeHeader(d.Name))
	io.Copy(w, f)
	r.store.done(dev.ID, d.ID)
	os.Remove(path)
}

func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, `"`, "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// --- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
}

func decodeJSON(req *http.Request, v any) error {
	defer req.Body.Close()
	return json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(v)
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(s)
}

func sanitizeZipPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}

// dropTokenFromPath extracts the first path segment after /drop/ (the page
// posts to /drop/<token>/upload, so trailing segments are ignored).
func dropTokenFromPath(p string) string {
	rest := strings.TrimPrefix(p, "/drop/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// --- request context ------------------------------------------------------

type ctxKey int

const deviceCtxKey ctxKey = 0

func contextWithDevice(req *http.Request, d *device) context.Context {
	return context.WithValue(req.Context(), deviceCtxKey, d)
}

func deviceFromContext(req *http.Request) *device {
	d, _ := req.Context().Value(deviceCtxKey).(*device)
	return d
}
