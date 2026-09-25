// Package rnsenv locates (and, when asked, creates) the version-pinned Python
// virtualenv that the Reticulum interoperability tests run upstream RNS and
// LXMF from. It imports nothing from the bridge so that any test package,
// including external test packages of internal/reticulum, can use it without
// an import cycle.
//
// Resolution order:
//
//  1. $MESHSAT_RNS_VENV/bin/python3
//  2. $XDG_CACHE_HOME/meshsat/rns-venv-<RNSVersion>/bin/python3 (or ~/.cache)
//  3. when MESHSAT_INTEROP=1 and neither exists: create 2 with
//     python3 -m venv + pip install rns==<RNSVersion> lxmf==<LXMFVersion>
//
// In CI (CI set) without MESHSAT_INTEROP=1 the tests skip without touching
// the network. A venv whose RNS or LXMF version differs from the pin fails
// loudly instead of silently testing against the wrong release.
package rnsenv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Pinned upstream versions. Bump them together with the interop matrix.
const (
	RNSVersion  = "1.5.4"
	LXMFVersion = "1.1.0"
)

var (
	once       sync.Once
	pythonBin  string
	resolveErr error
)

// Python returns the venv interpreter, skipping the test when the venv is not
// available and must not be created, and failing when it is the wrong version.
func Python(t testing.TB) string {
	t.Helper()
	once.Do(resolve)
	if resolveErr != nil {
		if skip, ok := resolveErr.(skipError); ok {
			t.Skip(string(skip))
		}
		t.Fatalf("rnsenv: %v", resolveErr)
	}
	return pythonBin
}

// Bin returns a console script from the venv (rnsd, rnodeconf, rnpath, ...).
func Bin(t testing.TB, name string) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(Python(t)), name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("rnsenv: %s not in venv: %v", name, err)
	}
	return p
}

type skipError string

func (s skipError) Error() string { return string(s) }

func venvDir() string {
	if v := os.Getenv("MESHSAT_RNS_VENV"); v != "" {
		return v
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.TempDir()
		}
		cache = filepath.Join(home, ".cache")
	}
	return filepath.Join(cache, "meshsat", "rns-venv-"+RNSVersion)
}

func resolve() {
	dir := venvDir()
	py := filepath.Join(dir, "bin", "python3")
	if _, err := os.Stat(py); err != nil {
		if os.Getenv("MESHSAT_INTEROP") != "1" {
			resolveErr = skipError(fmt.Sprintf("Python RNS %s venv not present at %s (set MESHSAT_INTEROP=1 to create it)", RNSVersion, dir))
			return
		}
		if err := create(dir); err != nil {
			resolveErr = err
			return
		}
	}
	if err := checkVersions(py); err != nil {
		resolveErr = err
		return
	}
	pythonBin = py
}

func create(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if out, err := exec.Command("python3", "-m", "venv", dir).CombinedOutput(); err != nil {
		return fmt.Errorf("create venv: %v: %s", err, out)
	}
	pip := filepath.Join(dir, "bin", "pip")
	out, err := exec.Command(pip, "install", "--disable-pip-version-check", "-q",
		"rns=="+RNSVersion, "lxmf=="+LXMFVersion).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pip install: %v: %s", err, out)
	}
	return nil
}

func checkVersions(py string) error {
	cmd := exec.Command(py, "-c", `import json, RNS, LXMF; print(json.dumps({"rns": RNS.__version__, "lxmf": LXMF.__version__}))`)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("venv python cannot import RNS/LXMF: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var v struct {
		RNS  string `json:"rns"`
		LXMF string `json:"lxmf"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &v); err != nil {
		return fmt.Errorf("parse versions: %v", err)
	}
	if v.RNS != RNSVersion || v.LXMF != LXMFVersion {
		return fmt.Errorf("venv has RNS %s / LXMF %s, tests are pinned to RNS %s / LXMF %s (recreate %s)",
			v.RNS, v.LXMF, RNSVersion, LXMFVersion, venvDir())
	}
	return nil
}

// Run executes an inline Python script with args in the venv and returns its
// stdout. Stderr is included in the error on failure.
func Run(t testing.TB, script string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(Python(t), append([]string{"-c", script}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("python: %v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}
	return stdout.Bytes()
}

// RunJSON is Run followed by json.Unmarshal into out.
func RunJSON(t testing.TB, out any, script string, args ...string) {
	t.Helper()
	raw := Run(t, script, args...)
	if err := json.Unmarshal(bytes.TrimSpace(raw), out); err != nil {
		t.Fatalf("parse python JSON: %v\nraw: %s", err, raw)
	}
}
