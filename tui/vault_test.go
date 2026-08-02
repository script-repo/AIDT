package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentIDSlugsCatalogNames(t *testing.T) {
	cases := map[string]string{
		"Crush":       "crush",
		"OpenCode":    "opencode",
		"Grok Build":  "grok-build",
		"Claude Code": "claude-code",
		"  Hermes  ":  "hermes",
	}
	for in, want := range cases {
		if got := agentID(in); got != want {
			t.Errorf("agentID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVaultSeedsSchemaSkillsAndQueue(t *testing.T) {
	_, files, err := vaultSeedFiles()
	if err != nil {
		t.Fatalf("vaultSeedFiles: %v", err)
	}
	// The schema layer, the skill library, the registry, and the queue protocol
	// are what make the vault usable by an agent that has never seen it.
	for _, want := range []string{
		"AGENTS.md",
		"wiki/index.md",
		"wiki/log.md",
		"skills/REGISTRY.md",
		"skills/TEMPLATE.md",
		"agents/REGISTRY.md",
		"tasks/QUEUE.md",
		"bin/aidt-agent",
		"bin/aidt-skill",
		"bin/aidt-task",
		"bin/aidt-common.sh",
	} {
		if len(files[want]) == 0 {
			t.Errorf("vault seed is missing %s", want)
		}
	}
	// Every documented skill must carry the frontmatter the registry renders.
	for path, body := range files {
		if !strings.HasPrefix(path, "skills/") || !strings.HasSuffix(path, "/SKILL.md") {
			continue
		}
		for _, key := range []string{"name:", "summary:", "status:", "owner:"} {
			if !strings.Contains(string(body), key) {
				t.Errorf("%s is missing frontmatter key %q", path, key)
			}
		}
	}
}

func TestVaultScaffoldIsIdempotentAndValidShell(t *testing.T) {
	script := vaultScaffold()

	// Documentation and queue state are seeded once so agent work survives a
	// redeploy; only AIDT's own helpers are overwritten.
	if !strings.Contains(script, `aidt_seed 'AGENTS.md'`) {
		t.Error("AGENTS.md should be seeded, not overwritten")
	}
	if !strings.Contains(script, `aidt_tool 'bin/aidt-task'`) {
		t.Error("bin/aidt-task should be refreshed on every deploy")
	}
	if strings.Contains(script, `aidt_tool 'AGENTS.md'`) {
		t.Error("AGENTS.md must never be overwritten: agents edit the vault between deploys")
	}
	if strings.Contains(script, `aidt_seed 'bin/`) {
		t.Error("helpers must be refreshed, not seeded, or a damaged one is never repaired")
	}

	// The .bashrc guard has to match a string the block actually writes, or
	// every redeploy appends another copy.
	marker := "# AIDT agent vault"
	if strings.Count(script, marker) < 2 {
		t.Errorf("expected the .bashrc guard and the written marker to be the same string %q", marker)
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable for scaffold syntax validation")
	}
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("vault scaffold has invalid shell syntax: %v\n%s", err, out)
	}
}

func TestEveryDeployableAgentRegistersItself(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")

	for _, a := range agentCatalog {
		if !a.deployable {
			continue
		}
		if len(a.capabilities) == 0 {
			t.Errorf("%s advertises no capabilities, so no capability-routed task can reach it", a.name)
		}
		script := m.agentDeployScript(a)
		id := agentID(a.name)
		for _, want := range []string{
			"aidt-agent\" register",
			"--id '" + id + "'",
			"--name '" + shSingle(a.name) + "'",
			"--capabilities '" + strings.Join(a.capabilities, ",") + "'",
			"AIDT_AGENT_ID='" + id + "'",
		} {
			if !strings.Contains(script, want) {
				t.Errorf("%s deploy script missing %q", a.name, want)
			}
		}
		// Registration must not be able to fail an otherwise good install.
		if !strings.Contains(script, "WARN: vault registration failed") {
			t.Errorf("%s deploy script does not tolerate a failed registration", a.name)
		}
		if !strings.Contains(script, "scaffolding the shared agent vault") {
			t.Errorf("%s deploy script does not scaffold the vault", a.name)
		}
	}
}

func TestAgentsEnterVaultWithIdentityAndHelpers(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")

	for _, a := range agentCatalog {
		if a.container {
			continue
		}
		cmd := m.agentOpenCmd(a)
		for _, want := range []string{
			`export AIDT_AGENT_ID="` + agentID(a.name) + `"`,
			`export PATH="$AIDT_AGENT_VAULT/bin:$PATH"`,
			`cd "$HOME/Obsidian/AIDT-Agent-Vault"`,
		} {
			if !strings.Contains(cmd, want) {
				t.Errorf("%s open command missing %q", a.name, want)
			}
		}
	}
}
