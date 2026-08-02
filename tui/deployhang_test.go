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
		// One sourcing line in ~/.bashrc, and the block is deleted before being
		// rewritten, so a redeploy cannot leave two behind.
		if !strings.Contains(script, "# >>> AIDT agent env >>>") {
			t.Errorf("%s deploy does not add the sourcing block to ~/.bashrc", a.name)
		}
		if !strings.Contains(script, `sed -i -e '/^# >>> AIDT agent env >>>$/,/^# <<< AIDT agent env <<<$/d'`) {
			t.Errorf("%s deploy does not remove a previous block before writing one", a.name)
		}
		// An agent started outside AIDT must still reach the pool, which means
		// the launch-time wiring in agentOpenCmd has to be persisted too.
		for _, want := range []string{"OPENCODE_CONFIG=", "GROK_HOME=", "CODEX_HOME=", "GOOSE_PROVIDER=olla"} {
			if !strings.Contains(script, want) {
				t.Errorf("%s deploy does not persist %s, so a shell it did not launch talks to the vendor endpoint", a.name, want)
			}
		}
		// The generated file holds sourced tokens; it must not be world readable.
		if !strings.Contains(script, `chmod 600 "$HOME/.config/aidt/agent-env.sh"`) {
			t.Errorf("%s deploy leaves agent-env.sh readable by other users", a.name)
		}
	}
}
