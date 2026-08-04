package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// tailscaleInstalled reports whether the Tailscale client looks installed,
// separately from "tailscaled is reachable" — fresh machines have no client
// at all, and the connect banner should send those users to the download
// page instead of telling them to start a service they don't have.
// Best-effort: a false negative only makes the banner offer the download
// link; nothing is blocked.
func tailscaleInstalled() bool {
	for _, p := range tailscaleInstallPaths() {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	bin := "tailscale"
	if runtime.GOOS == "windows" {
		bin = "tailscale.exe"
	}
	if _, err := exec.LookPath(bin); err == nil {
		return true
	}
	_, err := exec.LookPath("tailscaled")
	return err == nil
}

// tailscaleInstallPaths lists the well-known install locations per platform.
// Fixed paths matter because GUI apps often get a sparse PATH (no /usr/sbin,
// no profile dirs), so LookPath alone misses standard installs.
func tailscaleInstallPaths() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Tailscale", "tailscale.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Tailscale", "tailscale.exe"),
		}
	case "darwin":
		return []string{"/Applications/Tailscale.app"}
	default:
		return []string{
			"/usr/sbin/tailscaled",
			"/usr/bin/tailscaled",
			"/run/current-system/sw/bin/tailscaled", // NixOS system profile
		}
	}
}
