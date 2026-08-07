package main

import "testing"

// The webview cannot open links itself, so the UI asks the server to open
// them in the system browser. Only allowlisted https URLs may ever reach
// openPath — this is the guard that keeps user-influenced input away from
// the OS command.
func TestValidateExternalURL(t *testing.T) {
	allowed := []string{
		"https://ko-fi.com/owldrop",
		"https://ko-fi.com/",
		"https://github.com/Rastavich/owldrop-install",
		"https://tailscale.com/download",
		"https://owldrop.app/",
		"https://KO-FI.COM/owldrop", // case-insensitive host
	}
	for _, u := range allowed {
		if _, err := validateExternalURL(u); err != nil {
			t.Errorf("validateExternalURL(%q) = error, want allowed", u)
		}
	}
	blocked := []string{
		"https://evil.com/",
		"http://ko-fi.com/owldrop",          // no plaintext
		"javascript:alert(1)",               // scheme injection
		"https://ko-fi.com.evil.com/x",      // host spoof via subdomain trick
		"https://ko-fi.com@evil.com/x",      // userinfo trick
		"https://ko-fi.com:443@evil.com/x",  // userinfo with port
		"https://",                          // empty host
		"",                                  // empty
		"file:///etc/passwd",                // scheme
		"https://evil.com#https://ko-fi.com", // fragment smuggling is harmless but still a bad host
	}
	for _, u := range blocked {
		if _, err := validateExternalURL(u); err == nil {
			t.Errorf("validateExternalURL(%q) = allowed, want blocked", u)
		}
	}
}
