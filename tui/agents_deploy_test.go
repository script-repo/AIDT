package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestHermesDeployScriptHeadless(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.token = "tok"
	script := m.agentDeployScript(mustAgent(t, "Hermes"))
	for _, want := range []string{
		"ensuring curl, git, Node.js",
		"--skip-setup",
		"hermes-agent.nousresearch.com/install.sh",
		"pointing hermes at Olla",
		// Must not end by execing interactive hermes (exit 127 when missing).
	} {
		if !strings.Contains(script, want) {
			t.Errorf("hermes deploy missing %q", want)
		}
	}
	// Last meaningful action should not be a bare "hermes" line as the only launch.
	if strings.HasSuffix(strings.TrimSpace(script), "\nhermes") {
		t.Error("hermes deploy should not exec interactive hermes at the end")
	}
}

func mustAgent(t *testing.T, name string) agentDef {
	t.Helper()
	a, ok := agentByName(name)
	if !ok {
		t.Fatalf("agent %q missing from catalog", name)
	}
	return a
}
