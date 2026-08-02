package main

import (
	"embed"
	"encoding/base64"
	"fmt"
	"io/fs"
	"strings"
)

// vaultFS holds the seed content for the shared Obsidian agent vault: the
// LLM-Wiki scaffolding (AGENTS.md schema + raw/ + wiki/), the shared skill
// library, the agent registry, the task queue, and the aidt-* helper tools.
//
// These are real files rather than Go string constants because the markdown is
// full of fenced code blocks, and Go raw strings cannot contain backticks.
//
//go:embed all:vaultfs
var vaultFS embed.FS

// vaultRoot is the embedded prefix stripped from every seeded path.
const vaultRoot = "vaultfs"

// vaultDirs are created on every deploy so the layout is complete even before
// any agent has written anything. Directories that only ever hold seeded files
// are covered by the seeding itself and are not repeated here.
var vaultDirs = []string{
	"raw/articles",
	"raw/papers",
	"raw/repos",
	"raw/data",
	"raw/transcripts",
	"raw/assets",
	"wiki/concepts",
	"wiki/entities",
	"wiki/sources",
	"wiki/comparisons",
	"outputs",
	"agents",
	"skills",
	"tasks/open",
	"tasks/claimed",
	"tasks/done",
	"tasks/failed",
	"bin",
	".obsidian",
}

// isVaultTool reports whether an embedded path is an AIDT-owned executable
// helper. Tools are refreshed on every deploy; documentation is seeded once so
// agent edits survive a redeploy.
func isVaultTool(rel string) bool { return strings.HasPrefix(rel, "bin/") }

// vaultSeedFiles returns the embedded vault content keyed by vault-relative
// path, in a deterministic order.
func vaultSeedFiles() ([]string, map[string][]byte, error) {
	var names []string
	out := map[string][]byte{}
	err := fs.WalkDir(vaultFS, vaultRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := vaultFS.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, vaultRoot), "/")
		names = append(names, rel)
		out[rel] = data
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return names, out, nil
}

// vaultScaffold returns the shell fragment that materializes the shared vault.
//
// It is idempotent by design: agents redeploy often, and the vault accumulates
// real work between deploys. Documentation and queue state are written only
// when absent; only the bin/ helpers are overwritten, because those are AIDT's
// code rather than the agents' data.
func vaultScaffold() string {
	names, files, err := vaultSeedFiles()
	if err != nil {
		// The content is compiled in, so this cannot fail at runtime in a real
		// build. Degrade to a no-op rather than breaking an agent deploy.
		return fmt.Sprintf("echo \"[deploy] WARN: vault content unavailable: %s\" >&2\n", errStr(err))
	}

	var b strings.Builder
	b.WriteString(`echo "[deploy] scaffolding the shared agent vault…"
AIDT_VAULT="$AIDT_AGENT_VAULT"
`)
	for _, d := range vaultDirs {
		b.WriteString(`mkdir -p "$AIDT_VAULT/` + d + `"` + "\n")
	}

	// Seed writes only when the file is absent. Tool writes to a temp name and
	// renames, so an agent never observes a half-written helper.
	b.WriteString(`aidt_seed() {
  if [ -f "$AIDT_VAULT/$1" ]; then return 0; fi
  mkdir -p "$(dirname "$AIDT_VAULT/$1")"
  printf %s "$2" | base64 -d > "$AIDT_VAULT/$1"
}
aidt_tool() {
  mkdir -p "$(dirname "$AIDT_VAULT/$1")"
  printf %s "$2" | base64 -d > "$AIDT_VAULT/$1.new"
  chmod 0755 "$AIDT_VAULT/$1.new"
  mv "$AIDT_VAULT/$1.new" "$AIDT_VAULT/$1"
}
`)

	for _, rel := range names {
		enc := base64.StdEncoding.EncodeToString(files[rel])
		fn := "aidt_seed"
		if isVaultTool(rel) {
			fn = "aidt_tool"
		}
		b.WriteString(fn + " '" + shSingle(rel) + "' '" + enc + "'\n")
	}

	// Put the helpers on PATH for this script and for every future login shell,
	// so an agent that opens a plain terminal still finds aidt-task.
	// The guard must match a string this block actually writes, or every
	// redeploy appends another copy. A literal comment marker is the stable
	// choice: the exported values themselves contain shell variables that are
	// written unexpanded.
	b.WriteString(`export PATH="$AIDT_VAULT/bin:$PATH"
grep -q '# AIDT agent vault' "$HOME/.bashrc" 2>/dev/null || {
  printf '\n# AIDT agent vault\nexport AIDT_AGENT_VAULT="%s"\nexport PATH="$AIDT_AGENT_VAULT/bin:$PATH"\n' "$AIDT_VAULT" >> "$HOME/.bashrc"
}
"$AIDT_VAULT/bin/aidt-skill" register >/dev/null 2>&1 || echo "[deploy] WARN: could not rebuild the skill registry" >&2
echo "[deploy] vault ready: $AIDT_VAULT (schema: AGENTS.md)"
`)
	return b.String()
}

// vaultSetup is the full workspace bootstrap: install Obsidian, then scaffold
// the shared vault. Every agent deploy path runs this.
func vaultSetup() string {
	// agentPathBootstrap goes here because every deploy path runs vaultSetup
	// exactly once, so the agent bin directories reach ~/.bashrc no matter which
	// agent is being installed.
	return obsidianBootstrap + vaultScaffold() + agentPathBootstrap
}

// agentID is the vault identity for an agent: kebab-case of its catalog name.
// Task routing matches `for:` values against exactly this string.
func agentID(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// agentRegisterScript records the agent in the vault registry so the other
// agents on this host can see what it is and so task routing can match it.
//
// Registration is best-effort: a vault problem must not fail an otherwise good
// agent install.
func (m *model) agentRegisterScript(a agentDef) string {
	id := agentID(a.name)
	caps := strings.Join(a.capabilities, ",")
	q := func(s string) string { return "'" + shSingle(s) + "'" }

	var b strings.Builder
	b.WriteString("\n# --- register this agent in the shared vault ---\n")
	b.WriteString("export AIDT_AGENT_ID=" + q(id) + "\n")
	b.WriteString(`if [ -x "$AIDT_VAULT/bin/aidt-agent" ]; then
  "$AIDT_VAULT/bin/aidt-agent" register \
`)
	b.WriteString("    --id " + q(id) + " \\\n")
	b.WriteString("    --name " + q(a.name) + " \\\n")
	b.WriteString("    --cli " + q(a.cli) + " \\\n")
	b.WriteString("    --endpoint " + q(a.endpoint) + " \\\n")
	b.WriteString("    --model " + q(m.effDefaultModel()) + " \\\n")
	b.WriteString("    --desc " + q(a.desc) + " \\\n")
	b.WriteString("    --capabilities " + q(caps) + " ||\n")
	b.WriteString(`    echo "[deploy] WARN: vault registration failed" >&2
else
  echo "[deploy] WARN: aidt-agent helper missing; skipping registration" >&2
fi
`)
	return b.String()
}
