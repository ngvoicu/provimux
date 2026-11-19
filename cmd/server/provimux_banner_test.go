package main

import (
	"bufio"
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The banner shows the same version X-CPA-VERSION reports: the Provimux version
// when one is linked, else the CLIProxyAPI base version (never "dev" or blank).
func TestProvimuxBannerShowsDisplayVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the server binary")
	}
	const pmxFlag = "-X github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo.ProvimuxVersion="
	cases := []struct {
		name, ldflags, want string
	}{
		{"no provimux flag", "-X main.Version=8.0.77",
			"Provimux Version: 8.0.77 (based on CLIProxyAPI 8.0.77), Commit: none, BuiltAt: unknown"},
		{"empty provimux flag", "-X main.Version=8.0.77 " + pmxFlag,
			"Provimux Version: 8.0.77 (based on CLIProxyAPI 8.0.77), Commit: none, BuiltAt: unknown"},
		{"provimux version", "-X main.Version=8.0.77 " + pmxFlag + "8.0.77-pmx.1",
			"Provimux Version: 8.0.77-pmx.1 (based on CLIProxyAPI 8.0.77), Commit: none, BuiltAt: unknown"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "provimux"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			bin := filepath.Join(t.TempDir(), name)
			build := exec.Command("go", "build", "-ldflags", tc.ldflags, "-o", bin, ".")
			if out, errBuild := build.CombinedOutput(); errBuild != nil {
				t.Fatalf("case %d: go build: %v\n%s", i, errBuild, out)
			}
			out, errRun := exec.Command(bin, "--help").CombinedOutput()
			if errRun != nil {
				t.Fatalf("case %d: run %s --help: %v\n%s", i, bin, errRun, out)
			}
			first, _ := bufio.NewReader(bytes.NewReader(out)).ReadString('\n')
			if got := string(bytes.TrimRight([]byte(first), "\n")); got != tc.want {
				t.Fatalf("banner = %q, want %q", got, tc.want)
			}
		})
	}
}
