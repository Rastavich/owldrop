// probe-files-api dumps the raw Tailscale LocalAPI inbox response.
//
// Purpose: sender identity for waiting Taildrop files is NOT exposed by the
// daemon (apitype.WaitingFile = {Name, Size} on every version through
// v1.102.x, confirmed 2026-08-08). Tailscale occasionally refactors Taildrop
// without fanfare (it became feature/taildrop in 1.102) — re-run this probe
// after daemon upgrades to see whether the wire response gained fields.
//
// Usage: go run ./tools/probe-files-api   (respects OWLDROP_TS_SOCKET)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func rawClient() *http.Client {
	sock := os.Getenv("OWLDROP_TS_SOCKET")
	if sock == "" {
		sock = "/var/run/tailscale/tailscaled.sock"
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}
}

func get(c *http.Client, path string) (string, error) {
	res, err := c.Get("http://local-tailscaled.sock" + path)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return fmt.Sprintf("status: %s\n%s", res.Status, b), nil
}

func main() {
	c := rawClient()
	out, err := get(c, "/localapi/v0/files/")
	if err != nil {
		fmt.Fprintln(os.Stderr, "files GET failed:", err)
		os.Exit(1)
	}
	fmt.Println("=== /localapi/v0/files/ ===")
	fmt.Println(out)

	if out, err := get(c, "/localapi/v0/status"); err == nil {
		var st map[string]any
		if json.Unmarshal([]byte(out[strings.Index(out, "\n")+1:]), &st) == nil {
			fmt.Printf("daemon version: %v\n", st["Version"])
		}
	} else {
		fmt.Fprintln(os.Stderr, "status GET failed:", err)
	}
}
