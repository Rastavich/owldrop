// Command buildtailcat builds the pinned tailcat CLI that Owldrop bundles
// (https://github.com/tailscale/tailcat), the same way tailcat's own
// releases are built (CGO off, release build tags, stripped).
//
//	go run ./tools/buildtailcat -o bin/tailcat                      # host OS/arch
//	go run ./tools/buildtailcat -o bin/tailcat.exe -goos windows -goarch amd64
//
// Why build instead of download: tailcat publishes no macOS binaries, and a
// source build gives one path for every platform. Why a throwaway module:
// tailcat requires a pre-release tailscale.com, and adding it to Owldrop's
// go.mod would drag the whole app onto it. Building it in its own module
// keeps the two dependency graphs apart; the module proxy + go.sum checksum
// database still verify every byte.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// TailcatVersion is the tailcat release Owldrop ships. Keep flake.nix's
// tailcat input on the same tag.
const TailcatVersion = "v0.7.0"

const tailcatModule = "github.com/tailscale/tailcat"

// securityOverrides raise tailcat's dependencies past known vulnerabilities
// (govulncheck -mode=binary on the built tailcat) until a tailcat release
// picks the fixes up. Minimal version selection only ever moves these up.
// Drop entries once TailcatVersion's own go.mod requires these versions.
var securityOverrides = []string{
	"golang.org/x/net@v0.62.0",              // GO-2026-6866, -6860, -6617, -6612, -6611, -6610
	"golang.org/x/crypto@v0.59.0",           // advisory fixed after v0.58.0
	"github.com/klauspost/compress@v1.20.2", // GO-2026-6865
}

func main() {
	out := flag.String("o", "", "output binary path (required)")
	goos := flag.String("goos", envOr("GOOS", runtime.GOOS), "target OS")
	goarch := flag.String("goarch", envOr("GOARCH", runtime.GOARCH), "target architecture")
	flag.Parse()
	if *out == "" {
		log.Fatal("buildtailcat: -o is required")
	}
	abs, err := filepath.Abs(*out)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		log.Fatal(err)
	}

	tmp, err := os.MkdirTemp("", "owldrop-tailcat-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	gomod := "module owldrop.local/tailcatbuild\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte(gomod), 0o644); err != nil {
		log.Fatal(err)
	}

	hostEnv := []string{"GOWORK=off", "GOFLAGS=", "GOOS=", "GOARCH="}
	run(tmp, hostEnv, "go", "get", tailcatModule+"/cmd/tailcat@"+TailcatVersion)
	run(tmp, hostEnv, "go", append([]string{"get"}, securityOverrides...)...)

	// tailcat's releases build with a long list of ts_omit_* tags (smaller
	// binary, less attack surface); the module ships the list.
	var dl struct{ Dir string }
	if err := json.Unmarshal(output(tmp, hostEnv, "go", "mod", "download", "-json", tailcatModule+"@"+TailcatVersion), &dl); err != nil {
		log.Fatalf("buildtailcat: go mod download: %v", err)
	}
	tags, err := os.ReadFile(filepath.Join(dl.Dir, "build-tags.txt"))
	if err != nil {
		log.Fatalf("buildtailcat: reading tailcat's build tags: %v", err)
	}

	targetEnv := []string{"GOWORK=off", "GOFLAGS=", "CGO_ENABLED=0", "GOOS=" + *goos, "GOARCH=" + *goarch}
	run(tmp, targetEnv, "go", "build", "-trimpath",
		"-tags="+strings.TrimSpace(string(tags)),
		"-ldflags=-s -w -X main.version="+TailcatVersion,
		"-o", abs, tailcatModule+"/cmd/tailcat")
	fmt.Printf("built tailcat %s for %s/%s → %s\n", TailcatVersion, *goos, *goarch, abs)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func command(dir string, env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stderr = os.Stderr
	return cmd
}

func run(dir string, env []string, name string, args ...string) {
	cmd := command(dir, env, name, args...)
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		log.Fatalf("buildtailcat: %s %s: %v", name, strings.Join(args, " "), err)
	}
}

func output(dir string, env []string, name string, args ...string) []byte {
	var b bytes.Buffer
	cmd := command(dir, env, name, args...)
	cmd.Stdout = &b
	if err := cmd.Run(); err != nil {
		log.Fatalf("buildtailcat: %s %s: %v", name, strings.Join(args, " "), err)
	}
	return b.Bytes()
}
