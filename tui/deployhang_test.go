package main

import (
	"regexp"
	"strings"
	"testing"
)

// A deploy runs attached to the operator's terminal, so a network call that can
// block forever does not fail — it hangs the TUI with no way to tell what is
// wrong. Every curl in a deploy script must be bounded, and none may be able to
// reach for the terminal to prompt.

// readinessCurls finds curl invocations inside retry loops, which is where an
// unbounded call is most damaging: the loop never gets to iterate.
func TestReadinessLoopCurlsAreBounded(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	scripts := map[string]string{
		"openCodeServer": openCodeServerFragment,
		"obsidian":       obsidianBootstrap,
	}
	for _, a := range agentCatalog {
		scripts["deploy:"+a.name] = m.agentDeployScriptBody(a)
	}

	for name, script := range scripts {
		for i, line := range strings.Split(script, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || !strings.Contains(line, "curl ") {
				continue
			}
			// A download piped straight into a shell is a different concern; what
			// matters here is that the call cannot wait forever.
			bounded := strings.Contains(line, "--max-time") ||
				strings.Contains(line, "--speed-time") ||
				strings.Contains(line, "--connect-timeout")
			// Package-manager lines merely mention curl as a dependency.
			mentionsOnly := strings.Contains(line, "install -y") ||
				strings.Contains(line, "command -v curl") ||
				strings.Contains(line, "need_curl")
			if !bounded && !mentionsOnly {
				t.Errorf("%s:%d curl can block forever, hanging the deploy:\n  %s", name, i+1, trimmed)
			}
		}
	}
}

// The OpenCode readiness probe is the one that actually hung: systemd binds the
// port before the server answers, so an uncapped request waits on a response
// that has not started yet.
func TestOpenCodeReadinessProbeCannotHang(t *testing.T) {
	probe := regexp.MustCompile(`curl[^\n]*global/health`)
	line := probe.FindString(openCodeServerFragment)
	if line == "" {
		// The invocation spans lines; fall back to the whole loop body.
		idx := strings.Index(openCodeServerFragment, "global/health")
		if idx < 0 {
			t.Fatal("readiness probe not found")
		}
		start := strings.LastIndex(openCodeServerFragment[:idx], "curl")
		line = openCodeServerFragment[start : idx+len("global/health")]
	}
	for _, want := range []string{"--max-time", "--connect-timeout"} {
		if !strings.Contains(line, want) {
			t.Errorf("readiness probe lacks %s: %s", want, line)
		}
	}
	// Without this, a credential prompt would wait on the operator's terminal.
	if !strings.Contains(openCodeServerFragment, "</dev/null") {
		t.Error("readiness probe can still read from the terminal")
	}
	// The loop must remain bounded so a genuine failure is reported, not waited on.
	if !strings.Contains(openCodeServerFragment, "OpenCode server did not become ready") {
		t.Error("readiness loop no longer reports a timeout")
	}
}

// AIDT injects the agent bin directories only when it launches an agent itself
// (loginShell), so any other shell on the host — a plain SSH login, or a
// terminal brokered by a deployment like Command Atlas — could not find
// opencode. Every deploy now persists them.
func TestAgentDeploysPersistAgentPathForOtherShells(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	for _, a := range agentCatalog {
		if !a.deployable {
			continue
		}
		script := m.agentDeployScript(a)
		for _, dir := range []string{"$HOME/.opencode/bin", "$HOME/.npm-global/bin", "$HOME/.local/bin"} {
			if !strings.Contains(script, dir) {
				t.Errorf("%s deploy does not put %s on the persisted PATH", a.name, dir)
			}
		}
		// Guarded by a marker the block itself writes, or a redeploy appends
		// another copy every time.
		if strings.Count(script, "# AIDT agent tools") < 2 {
			t.Errorf("%s deploy does not guard the .bashrc block against duplication", a.name)
		}
	}
}
