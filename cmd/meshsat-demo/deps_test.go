package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The demo faces the internet, so it must never carry the Bridge's own API
// or anything that reaches a host or the Hub: those packages answer every
// visitor in full and act on the host (MESHSAT-1845's audit). The demo
// links internal/engine and what it pulls in only for types the
// Community Post names; it calls none of it.
func TestTheDemoLinksNoBridgeAPIOrHostAccess(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	forbidden := []string{
		"meshsat/internal/api", "meshsat/internal/oob", "meshsat/internal/relayclient",
		"meshsat/internal/hubreporter", "meshsat/internal/pair", "meshsat/internal/federation", "meshsat/internal/hubroute",
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, f := range forbidden {
			if dep == f || strings.HasPrefix(dep, f+"/") {
				t.Errorf("meshsat-demo links %s", dep)
			}
		}
	}
	if !strings.Contains(string(out), "meshsat/internal/demo") || !strings.Contains(string(out), "meshsat/internal/post") {
		t.Fatal("the dependency list is not the demo's")
	}
}
