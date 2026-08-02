package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// agentDef describes a terminal AI agent CLI the TUI can launch (and optionally
// deploy) over SSH on one of the managed hosts.
type agentDef struct {
	name       string
	cli        string // command to (re)launch an existing install
	deployable bool   // whether the TUI can install it
	container  bool   // runs as Docker container(s) on the host (multi-instance)
	target     string // placement summary shown in the Agents list
	endpoint   string // how it reaches models (informational)
	desc       string
	// capabilities is the agent's advertised skill set in the shared vault.
	// Task queue entries tagged `for: capability:<name>` route on these, so the
	// vocabulary is deliberately small — see skills/agent-registry/SKILL.md.
	capabilities []string
}

// depsBootstrap installs the prerequisites the npm/Node agents need on
// Rocky/RHEL and Ubuntu/Debian and makes `npm -g` usable as a non-root user:
//   - curl + git (Hermes/OpenClaw installers require them)
//   - Node.js 22 via NodeSource (these are Node tools)
//   - a user-writable npm global prefix (~/.npm-global), because NodeSource sets
//     the prefix to /usr, which makes `npm install -g openclaw` fail with EACCES
//     for a normal user. We also put /usr/local/bin + ~/.local/bin + npm bin on
//     PATH (non-login SSH often misses them → exit 127).
const depsBootstrap = `echo "[deploy] ensuring curl, git, Node.js…"
. /etc/os-release 2>/dev/null || true
# Broad PATH first so later command -v finds ollama/hermes/openclaw where packages put them.
export PATH="/usr/local/bin:/usr/bin:$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"
need_curl=0; need_git=0
command -v curl >/dev/null 2>&1 || need_curl=1
command -v git >/dev/null 2>&1 || need_git=1
if [ "$need_curl$need_git" != "00" ]; then
  case "${ID:-}" in
    ubuntu|debian)
      sudo apt-get update -y >/dev/null 2>&1 || true
      pkgs=""
      [ "$need_curl" = 1 ] && pkgs="$pkgs curl"
      [ "$need_git" = 1 ] && pkgs="$pkgs git"
      sudo DEBIAN_FRONTEND=noninteractive apt-get install -y $pkgs
      ;;
    *)
      pkgs=""
      [ "$need_curl" = 1 ] && pkgs="$pkgs curl"
      [ "$need_git" = 1 ] && pkgs="$pkgs git"
      sudo dnf install -y $pkgs 2>/dev/null || sudo yum install -y $pkgs
      ;;
  esac
fi
command -v curl >/dev/null 2>&1 || { echo "[deploy] ERROR: curl still missing" >&2; exit 1; }
if ! command -v node >/dev/null 2>&1 || ! command -v npm >/dev/null 2>&1; then
  echo "[deploy] installing Node.js 22 + npm via NodeSource (sudo)…"
  case "${ID:-}" in
    ubuntu|debian)
      curl -fsSL --connect-timeout 15 --max-time 300 https://deb.nodesource.com/setup_22.x | sudo -E bash -
      sudo DEBIAN_FRONTEND=noninteractive apt-get install -y nodejs
      ;;
    *)
      curl -fsSL --connect-timeout 15 --max-time 300 https://rpm.nodesource.com/setup_22.x | sudo -E bash -
      sudo dnf install -y nodejs 2>/dev/null || sudo yum install -y nodejs
      ;;
  esac
fi
command -v npm >/dev/null 2>&1 || { echo "[deploy] ERROR: npm still missing after Node install" >&2; exit 1; }
NPM_PREFIX="$HOME/.npm-global"
mkdir -p "$NPM_PREFIX/bin" "$HOME/.local/bin"
npm config set prefix "$NPM_PREFIX" >/dev/null 2>&1 || true
export PATH="/usr/local/bin:$HOME/.local/bin:$NPM_PREFIX/bin:$PATH"
hash -r 2>/dev/null || true
grep -q '.npm-global/bin' "$HOME/.bashrc" 2>/dev/null || echo 'export PATH="$HOME/.npm-global/bin:$PATH"' >> "$HOME/.bashrc"
grep -q '.local/bin' "$HOME/.bashrc" 2>/dev/null || echo 'export PATH="$HOME/.local/bin:$PATH"' >> "$HOME/.bashrc"
grep -q '/usr/local/bin' "$HOME/.bashrc" 2>/dev/null || echo 'export PATH="/usr/local/bin:$PATH"' >> "$HOME/.bashrc"
`

// cliDepsBootstrap installs the small set of tools required by the official
// standalone installers without pulling in Node.js for native CLIs.
const cliDepsBootstrap = `echo "[deploy] ensuring CLI installer dependencies…"
. /etc/os-release 2>/dev/null || true
export PATH="/usr/local/bin:/usr/bin:$HOME/.local/bin:$HOME/.opencode/bin:$HOME/.grok/bin:$PATH"
if [ "$(id -u)" -eq 0 ]; then
  SUDO=""
elif command -v sudo >/dev/null 2>&1; then
  SUDO="sudo"
else
  echo "[deploy] ERROR: installation requires root or sudo for missing dependencies" >&2
  exit 1
fi
case "${ID:-} ${ID_LIKE:-}" in
  *ubuntu*|*debian*)
    $SUDO apt-get update -y >/dev/null
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl git tar gzip bzip2 unzip
    ;;
  *)
    if command -v dnf >/dev/null 2>&1; then
      $SUDO dnf install -y ca-certificates curl git tar gzip bzip2 unzip
    elif command -v yum >/dev/null 2>&1; then
      $SUDO yum install -y ca-certificates curl git tar gzip bzip2 unzip
    else
      echo "[deploy] ERROR: unsupported Linux package manager" >&2
      exit 1
    fi
    ;;
esac
mkdir -p "$HOME/.local/bin" "$HOME/.config/aidt"
grep -q '.local/bin' "$HOME/.bashrc" 2>/dev/null || echo 'export PATH="$HOME/.local/bin:$PATH"' >> "$HOME/.bashrc"
`

// obsidianBootstrap installs the official Obsidian AppImage when needed and
// creates the shared vault that every deployed agent uses as its workspace.
const obsidianBootstrap = `echo "[deploy] checking Obsidian..."
export PATH="$HOME/.local/bin:$PATH"
export AIDT_AGENT_VAULT="$HOME/Obsidian/AIDT-Agent-Vault"
mkdir -p "$AIDT_AGENT_VAULT/.obsidian" "$HOME/.local/bin"
if command -v obsidian >/dev/null 2>&1; then
  echo "[deploy] Obsidian ready: $(command -v obsidian)"
else
  echo "[deploy] Obsidian not found; installing the official AppImage..."
  . /etc/os-release 2>/dev/null || true
  if [ "$(id -u)" -eq 0 ]; then
    SUDO=""
  elif command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
  else
    echo "[deploy] ERROR: Obsidian installation requires root or sudo" >&2
    exit 1
  fi
  if ! command -v curl >/dev/null 2>&1; then
    case "${ID:-} ${ID_LIKE:-}" in
      *ubuntu*|*debian*)
        $SUDO apt-get update -y >/dev/null
        $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl
        ;;
      *)
        if command -v dnf >/dev/null 2>&1; then
          $SUDO dnf install -y ca-certificates curl
        elif command -v yum >/dev/null 2>&1; then
          $SUDO yum install -y ca-certificates curl
        else
          echo "[deploy] ERROR: unsupported Linux package manager" >&2
          exit 1
        fi
        ;;
    esac
  fi
  OBSIDIAN_META="$(mktemp)"
  curl -fsSL --connect-timeout 15 --max-time 60 https://api.github.com/repos/obsidianmd/obsidian-releases/releases/latest -o "$OBSIDIAN_META"
  OBSIDIAN_VERSION="$(sed -n 's/.*"tag_name":[[:space:]]*"v\([^"]*\)".*/\1/p' "$OBSIDIAN_META" | head -1)"
  rm -f "$OBSIDIAN_META"
  [ -n "$OBSIDIAN_VERSION" ] || { echo "[deploy] ERROR: could not resolve the latest Obsidian version" >&2; exit 1; }
  case "$(uname -m)" in
    x86_64|amd64)
      OBSIDIAN_ASSET="Obsidian-$OBSIDIAN_VERSION.AppImage"
      ;;
    aarch64|arm64)
      OBSIDIAN_ASSET="Obsidian-$OBSIDIAN_VERSION-arm64.AppImage"
      ;;
    *)
      echo "[deploy] ERROR: unsupported Obsidian architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
  OBSIDIAN_URL="https://github.com/obsidianmd/obsidian-releases/releases/download/v$OBSIDIAN_VERSION/$OBSIDIAN_ASSET"
  OBSIDIAN_TMP="$(mktemp)"
  # No overall cap: this is ~136MB and a slow link is legitimate. Abort only if
  # the transfer genuinely stalls, so a dead connection cannot hang the deploy.
  curl -fsSL --connect-timeout 15 --speed-limit 1024 --speed-time 60 "$OBSIDIAN_URL" -o "$OBSIDIAN_TMP"
  [ -s "$OBSIDIAN_TMP" ] || { rm -f "$OBSIDIAN_TMP"; echo "[deploy] ERROR: Obsidian download was empty" >&2; exit 1; }
  mv "$OBSIDIAN_TMP" "$HOME/.local/bin/obsidian"
  chmod 0755 "$HOME/.local/bin/obsidian"
  hash -r 2>/dev/null || true
  command -v obsidian >/dev/null 2>&1 || { echo "[deploy] ERROR: obsidian not found after install" >&2; exit 1; }
  echo "[deploy] Obsidian ready: $(command -v obsidian)"
fi
echo "[deploy] agent vault: $AIDT_AGENT_VAULT"
`

// crushDeployScript installs Crush from Charm's official package repository on
// the Olla server. Deployment is intentionally non-interactive; opening Crush
// is a separate action after a successful install has been registered.
const crushDeployScript = `set -e
export PATH="/usr/local/bin:/usr/bin:$HOME/.local/bin:$PATH"
. /etc/os-release 2>/dev/null || true

if [ "$(id -u)" -eq 0 ]; then
  SUDO=""
elif command -v sudo >/dev/null 2>&1; then
  SUDO="sudo"
else
  echo "[deploy] ERROR: Crush installation requires root or sudo" >&2
  exit 1
fi

if ! command -v crush >/dev/null 2>&1; then
  case "${ID:-} ${ID_LIKE:-}" in
    *ubuntu*|*debian*)
      echo "[deploy] installing Crush dependencies with apt…"
      $SUDO apt-get update -y
      $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl git gnupg
      $SUDO mkdir -p /etc/apt/keyrings
      curl -fsSL --connect-timeout 15 --max-time 300 https://repo.charm.sh/apt/gpg.key | $SUDO gpg --dearmor --batch --yes -o /etc/apt/keyrings/charm.gpg
      echo 'deb [signed-by=/etc/apt/keyrings/charm.gpg] https://repo.charm.sh/apt/ * *' | $SUDO tee /etc/apt/sources.list.d/charm.list >/dev/null
      $SUDO apt-get update -y
      $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y crush
      ;;
    *rocky*|*rhel*|*fedora*|*centos*|*almalinux*)
      if command -v dnf >/dev/null 2>&1; then
        PKG=dnf
      elif command -v yum >/dev/null 2>&1; then
        PKG=yum
      else
        echo "[deploy] ERROR: neither dnf nor yum is available" >&2
        exit 1
      fi
      echo "[deploy] installing Crush dependencies with $PKG…"
      $SUDO $PKG install -y ca-certificates curl git
      printf '[charm]\nname=Charm\nbaseurl=https://repo.charm.sh/yum/\nenabled=1\ngpgcheck=1\ngpgkey=https://repo.charm.sh/yum/gpg.key\n' | $SUDO tee /etc/yum.repos.d/charm.repo >/dev/null
      $SUDO $PKG install -y crush
      ;;
    *)
      echo "[deploy] ERROR: unsupported Linux distribution '${ID:-unknown}'" >&2
      echo "[deploy] supported: Rocky/RHEL/Fedora/CentOS/AlmaLinux and Ubuntu/Debian" >&2
      exit 1
      ;;
  esac
fi
hash -r 2>/dev/null || true
CRUSH_BIN="$(command -v crush 2>/dev/null || true)"
if [ -z "$CRUSH_BIN" ] || [ ! -x "$CRUSH_BIN" ]; then
  echo "[deploy] ERROR: Crush binary not found after package installation" >&2
  exit 1
fi
echo "[deploy] Crush ready: $CRUSH_BIN"
"$CRUSH_BIN" --version 2>/dev/null || true
echo "[deploy] Open Crush from Agents (enter/o)."
`

// agentCatalog is the set of agents offered in the Agents section.
//
// All agents deploy to the gateway, one worker, or every selected worker and
// reach models through the Olla gateway, which load-balances the whole pool.
var agentCatalog = []agentDef{
	{
		name:         "Crush",
		cli:          "crush",
		deployable:   true,
		target:       "gateway / worker",
		endpoint:     "Olla OpenAI endpoint (whole pool)",
		desc:         "Charm coding agent",
		capabilities: []string{"code", "knowledge", "shell"},
	},
	{
		name:         "OpenCode",
		cli:          "opencode",
		deployable:   true,
		target:       "gateway / worker",
		endpoint:     "Olla OpenAI endpoint (whole pool)",
		desc:         "Open-source coding agent",
		capabilities: []string{"code", "knowledge", "shell", "serve"},
	},
	{
		name:         "Goose",
		cli:          "goose session",
		deployable:   true,
		target:       "gateway / worker",
		endpoint:     "Olla OpenAI endpoint (whole pool)",
		desc:         "AAIF open-source AI agent",
		capabilities: []string{"code", "knowledge", "shell"},
	},
	{
		name:         "Grok Build",
		cli:          "grok",
		deployable:   true,
		target:       "gateway / worker",
		endpoint:     "Olla OpenAI endpoint (whole pool)",
		desc:         "xAI terminal coding agent",
		capabilities: []string{"code", "knowledge", "shell"},
	},
	{
		name:         "Claude Code",
		cli:          "claude",
		deployable:   true,
		target:       "gateway / worker",
		endpoint:     "Olla Anthropic endpoint (whole pool)",
		desc:         "Anthropic coding agent",
		capabilities: []string{"code", "knowledge", "shell"},
	},
	{
		name:         "Codex",
		cli:          "codex",
		deployable:   true,
		target:       "gateway / worker",
		endpoint:     "Olla OpenAI endpoint (whole pool)",
		desc:         "OpenAI terminal coding agent",
		capabilities: []string{"code", "knowledge", "shell"},
	},
	{
		name:         "Hermes",
		cli:          "hermes",
		deployable:   true,
		target:       "gateway / worker",
		endpoint:     "Olla OpenAI endpoint (whole pool)",
		desc:         "Nous Research self-improving agent",
		capabilities: []string{"code", "knowledge", "shell", "chat", "serve"},
	},
}

const openCodeInstallFragment = `echo "[deploy] installing/updating OpenCode from opencode.ai…"
INSTALLER="$(mktemp)"
curl -fsSL --connect-timeout 15 --max-time 300 https://opencode.ai/install -o "$INSTALLER"
bash "$INSTALLER"
rm -f "$INSTALLER"
export PATH="$HOME/.opencode/bin:$HOME/.local/bin:$PATH"
hash -r 2>/dev/null || true
command -v opencode >/dev/null 2>&1 || { echo "[deploy] ERROR: opencode not found after install" >&2; exit 1; }
echo "[deploy] OpenCode ready: $(command -v opencode)"
`

const gooseInstallFragment = `echo "[deploy] installing/updating Goose from the official AAIF release…"
INSTALLER="$(mktemp)"
curl -fsSL --connect-timeout 15 --max-time 300 https://github.com/aaif-goose/goose/releases/download/stable/download_cli.sh -o "$INSTALLER"
CONFIGURE=false bash "$INSTALLER"
rm -f "$INSTALLER"
export PATH="$HOME/.local/bin:$PATH"
hash -r 2>/dev/null || true
command -v goose >/dev/null 2>&1 || { echo "[deploy] ERROR: goose not found after install" >&2; exit 1; }
echo "[deploy] Goose ready: $(command -v goose)"
`

const grokInstallFragment = `echo "[deploy] installing/updating Grok Build from x.ai…"
INSTALLER="$(mktemp)"
curl -fsSL --connect-timeout 15 --max-time 300 https://x.ai/cli/install.sh -o "$INSTALLER"
bash "$INSTALLER"
rm -f "$INSTALLER"
export PATH="$HOME/.grok/bin:$HOME/.local/bin:$PATH"
hash -r 2>/dev/null || true
command -v grok >/dev/null 2>&1 || { echo "[deploy] ERROR: grok not found after install" >&2; exit 1; }
echo "[deploy] Grok Build ready: $(command -v grok)"
`

const claudeCodeInstallFragment = `echo "[deploy] installing/updating Claude Code from claude.ai…"
INSTALLER="$(mktemp)"
curl -fsSL --connect-timeout 15 --max-time 300 https://claude.ai/install.sh -o "$INSTALLER"
bash "$INSTALLER"
rm -f "$INSTALLER"
export PATH="$HOME/.local/bin:$PATH"
hash -r 2>/dev/null || true
command -v claude >/dev/null 2>&1 || { echo "[deploy] ERROR: claude not found after install" >&2; exit 1; }
echo "[deploy] Claude Code ready: $(command -v claude)"
`

const codexInstallFragment = `echo "[deploy] installing/updating Codex from OpenAI..."
INSTALLER="$(mktemp)"
curl -fsSL --connect-timeout 15 --max-time 300 https://chatgpt.com/codex/install.sh -o "$INSTALLER"
bash "$INSTALLER"
rm -f "$INSTALLER"
export PATH="$HOME/.local/bin:$HOME/.codex/bin:$PATH"
hash -r 2>/dev/null || true
command -v codex >/dev/null 2>&1 || { echo "[deploy] ERROR: codex not found after install" >&2; exit 1; }
echo "[deploy] Codex ready: $(command -v codex)"
`

const openCodeServerFragment = `echo "[deploy] configuring OpenCode server on port 4096..."
OPENCODE_BIN="$(command -v opencode)"
OPENCODE_USER="$(id -un)"
OPENCODE_HOME="$HOME"
if [ "$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi
cat <<EOF | $SUDO tee /etc/systemd/system/aidt-opencode.service >/dev/null
[Unit]
Description=AIDT OpenCode server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$OPENCODE_USER
Environment=HOME=$OPENCODE_HOME
Environment=OPENCODE_CONFIG=$OPENCODE_HOME/.config/aidt/opencode.json
Environment=PATH=/usr/local/bin:/usr/bin:$OPENCODE_HOME/.local/bin:$OPENCODE_HOME/.npm-global/bin:$OPENCODE_HOME/.opencode/bin
EnvironmentFile=$OPENCODE_HOME/.config/aidt/opencode-server.env
WorkingDirectory=$OPENCODE_HOME/Obsidian/AIDT-Agent-Vault
ExecStart=$OPENCODE_BIN serve --hostname 0.0.0.0 --port 4096
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
set -a
. "$OPENCODE_HOME/.config/aidt/opencode-server.env"
set +a
$SUDO systemctl daemon-reload
$SUDO systemctl enable aidt-opencode.service >/dev/null
$SUDO systemctl restart aidt-opencode.service
if command -v firewall-cmd >/dev/null 2>&1 && $SUDO firewall-cmd --state >/dev/null 2>&1; then
  $SUDO firewall-cmd --permanent --add-port=4096/tcp >/dev/null
  $SUDO firewall-cmd --reload >/dev/null
fi
OPENCODE_READY=0
# Every attempt is capped. systemd binds the socket before opencode has finished
# starting, so an uncapped curl connects and then waits for a response that is
# not coming yet — the loop never gets to iterate and the deploy hangs for good
# rather than failing. </dev/null keeps curl from ever reaching for the terminal.
for _ in $(seq 1 30); do
  if curl -fsS --connect-timeout 2 --max-time 5 \
      -u "$OPENCODE_SERVER_USERNAME:$OPENCODE_SERVER_PASSWORD" \
      http://127.0.0.1:4096/global/health >/dev/null 2>&1 </dev/null; then OPENCODE_READY=1; break; fi
  sleep 1
done
if [ "$OPENCODE_READY" -ne 1 ]; then
  $SUDO systemctl --no-pager --full status aidt-opencode.service >&2 || true
  echo "[deploy] ERROR: OpenCode server did not become ready on port 4096" >&2
  exit 1
fi
echo "[deploy] OpenCode server ready on http://0.0.0.0:4096"
`

const crushUpdateScript = `set -e
export PATH="/usr/local/bin:/usr/bin:$HOME/.local/bin:$PATH"
. /etc/os-release 2>/dev/null || true
if [ "$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi
case "${ID:-} ${ID_LIKE:-}" in
  *ubuntu*|*debian*)
    $SUDO apt-get update -y
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y --only-upgrade crush
    ;;
  *)
    if command -v dnf >/dev/null 2>&1; then
      $SUDO dnf upgrade -y crush
    else
      $SUDO yum update -y crush
    fi
    ;;
esac
command -v crush >/dev/null 2>&1 || { echo "[update] ERROR: crush not found" >&2; exit 1; }
echo "[update] Crush ready: $(crush --version 2>/dev/null || command -v crush)"
`

const hermesUpdateFragment = `export PATH="/usr/local/bin:$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"
echo "[update] installing latest Hermes from the official installer…"
INSTALLER="$(mktemp)"
curl -fsSL --connect-timeout 15 --max-time 300 https://hermes-agent.nousresearch.com/install.sh -o "$INSTALLER"
bash "$INSTALLER" --skip-setup
rm -f "$INSTALLER"
hash -r 2>/dev/null || true
command -v hermes >/dev/null 2>&1 || { echo "[update] ERROR: hermes not found after update" >&2; exit 1; }
`

// hermesInstallFragment installs Hermes via the official Nous installer when
// missing (--skip-setup so the wizard cannot hang a headless deploy). ollama
// launch is a last-resort fallback. Fails closed if hermes is still missing
// (avoids exit 127 from a later bare `hermes` call).
const hermesInstallFragment = `export PATH="/usr/local/bin:$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"
hash -r 2>/dev/null || true
if ! command -v hermes >/dev/null 2>&1; then
  echo "[deploy] installing hermes (official installer, skip setup)…"
  # --skip-setup: no interactive wizard. HERMES_NONINTERACTIVE is also set by caller.
  curl -fsSL --connect-timeout 15 --max-time 300 https://hermes-agent.nousresearch.com/install.sh | bash -s -- --skip-setup
  export PATH="/usr/local/bin:$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"
  hash -r 2>/dev/null || true
fi
# Installer may put the wrapper in ~/.local/bin or /usr/local/bin without
# refreshing this shell's hash table.
if ! command -v hermes >/dev/null 2>&1; then
  for c in "$HOME/.local/bin/hermes" /usr/local/bin/hermes "$HOME/.hermes/bin/hermes"; do
    if [ -x "$c" ]; then export PATH="$(dirname "$c"):$PATH"; break; fi
  done
  hash -r 2>/dev/null || true
fi
if ! command -v hermes >/dev/null 2>&1 && command -v ollama >/dev/null 2>&1; then
  echo "[deploy] hermes still missing; trying ollama launch hermes…"
  ollama launch hermes --yes --model __HERMES_MODEL__ </dev/null || true
  export PATH="/usr/local/bin:$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"
  hash -r 2>/dev/null || true
fi
if ! command -v hermes >/dev/null 2>&1; then
  echo "[deploy] ERROR: hermes not found on PATH after install" >&2
  echo "[deploy] tried: official installer --skip-setup, then ollama launch" >&2
  echo "[deploy] PATH=$PATH" >&2
  ls -la "$HOME/.local/bin" /usr/local/bin 2>/dev/null | head -40 >&2 || true
  exit 1
fi
echo "[deploy] hermes: $(command -v hermes)"
`

// openclawInstallFragment installs OpenClaw via npm (user prefix) or the
// official install.sh. Does NOT rely on `ollama launch` first — that fails with
// exit 127 when ollama is missing/off-PATH on the worker.
const openclawInstallFragment = `export PATH="/usr/local/bin:$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"
hash -r 2>/dev/null || true
if ! command -v openclaw >/dev/null 2>&1; then
  echo "[deploy] installing openclaw…"
  if command -v npm >/dev/null 2>&1; then
    echo "[deploy] npm install -g openclaw@latest (prefix $HOME/.npm-global)…"
    npm install -g openclaw@latest
  fi
  export PATH="/usr/local/bin:$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"
  hash -r 2>/dev/null || true
fi
if ! command -v openclaw >/dev/null 2>&1; then
  echo "[deploy] npm path missed; trying official install.sh…"
  curl -fsSL --connect-timeout 15 --max-time 300 https://openclaw.ai/install.sh | bash
  export PATH="/usr/local/bin:$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"
  hash -r 2>/dev/null || true
fi
if ! command -v openclaw >/dev/null 2>&1; then
  for c in "$HOME/.npm-global/bin/openclaw" "$HOME/.local/bin/openclaw" /usr/local/bin/openclaw; do
    if [ -x "$c" ]; then export PATH="$(dirname "$c"):$PATH"; break; fi
  done
  hash -r 2>/dev/null || true
fi
if ! command -v openclaw >/dev/null 2>&1 && command -v ollama >/dev/null 2>&1; then
  echo "[deploy] openclaw still missing; trying ollama launch openclaw…"
  ollama launch openclaw --yes --model __OPENCLAW_MODEL__ </dev/null || true
  export PATH="/usr/local/bin:$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"
  hash -r 2>/dev/null || true
fi
if ! command -v openclaw >/dev/null 2>&1; then
  echo "[deploy] ERROR: openclaw not found on PATH after install" >&2
  echo "[deploy] tried: npm -g, openclaw.ai/install.sh, ollama launch" >&2
  echo "[deploy] PATH=$PATH" >&2
  ls -la "$HOME/.npm-global/bin" "$HOME/.local/bin" 2>/dev/null | head -40 >&2 || true
  exit 1
fi
echo "[deploy] openclaw: $(command -v openclaw)"
`

// agentDeployScript returns the install bootstrap for an agent. Crush uses the
// Charm repo; Hermes/OpenClaw use official installers (not bare ollama launch).
// Headless deploys exit 0 after install so the TUI can register the agent —
// they do NOT exec the interactive CLI (that caused exit 127 when the binary
// was missing/off-PATH and blocked registration).
// agentDeployScript is the full install: the agent itself, then its
// registration in the shared vault so the other agents on this host can see
// what it is and the task queue can route work to it.
func (m *model) agentDeployScript(a agentDef) string {
	return m.agentDeployScriptBody(a) + m.agentRegisterScript(a)
}

func (m *model) agentDeployScriptBody(a agentDef) string {
	if a.name == "Crush" {
		return "set -e\n" + vaultSetup() + strings.TrimPrefix(crushDeployScript, "set -e\n")
	}
	model := m.effDefaultModel()
	gateway := a.name == "Hermes" && m.hermesGatewayWanted()

	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString(vaultSetup())
	b.WriteString(depsBootstrap)
	if a.name == "Hermes" {
		b.WriteString("export HERMES_NONINTERACTIVE=1\n")
		b.WriteString(strings.ReplaceAll(hermesInstallFragment, "__HERMES_MODEL__", model))
		b.WriteString(m.hermesOllaConfigScript(model))
		if gateway {
			b.WriteString(m.hermesGatewayScript())
			b.WriteString("echo \"[deploy] Hermes gateway is ready (Telegram). You can close this window.\"\n")
		} else {
			b.WriteString("echo \"[deploy] Hermes installed and pointed at Olla. Open it from Agents (enter) to chat.\"\n")
		}
		return b.String()
	}
	if a.name == "OpenCode" {
		b.Reset()
		b.WriteString("set -e\n")
		b.WriteString(vaultSetup())
		b.WriteString(cliDepsBootstrap)
		b.WriteString(openCodeInstallFragment)
		b.WriteString(m.agentConfigScript(a))
		b.WriteString(openCodeServerFragment)
		b.WriteString("echo \"[deploy] OpenCode installed, pointed at Olla, and serving on port 4096.\"\n")
		return b.String()
	}
	if a.name == "Goose" {
		b.Reset()
		b.WriteString("set -e\n")
		b.WriteString(vaultSetup())
		b.WriteString(cliDepsBootstrap)
		b.WriteString(gooseInstallFragment)
		b.WriteString(m.agentConfigScript(a))
		b.WriteString("echo \"[deploy] Goose installed and pointed at Olla. Open it from Agents (enter).\"\n")
		return b.String()
	}
	if a.name == "Grok Build" {
		b.Reset()
		b.WriteString("set -e\n")
		b.WriteString(vaultSetup())
		b.WriteString(cliDepsBootstrap)
		b.WriteString(grokInstallFragment)
		b.WriteString(m.agentConfigScript(a))
		b.WriteString("echo \"[deploy] Grok Build installed and pointed at Olla. Open it from Agents (enter).\"\n")
		return b.String()
	}
	if a.name == "Claude Code" {
		b.Reset()
		b.WriteString("set -e\n")
		b.WriteString(vaultSetup())
		b.WriteString(cliDepsBootstrap)
		b.WriteString(claudeCodeInstallFragment)
		b.WriteString(m.agentConfigScript(a))
		b.WriteString("echo \"[deploy] Claude Code installed. AIDT will open it through Olla's Anthropic endpoint.\"\n")
		return b.String()
	}
	if a.name == "Codex" {
		b.Reset()
		b.WriteString("set -e\n")
		b.WriteString(vaultSetup())
		b.WriteString(cliDepsBootstrap)
		b.WriteString(codexInstallFragment)
		b.WriteString(m.agentConfigScript(a))
		b.WriteString("echo \"[deploy] Codex installed and pointed at Olla. Open it from Agents (enter).\"\n")
		return b.String()
	}
	if a.name == "OpenClaw" {
		b.WriteString(strings.ReplaceAll(openclawInstallFragment, "__OPENCLAW_MODEL__", model))
		b.WriteString("echo \"[deploy] OpenClaw installed. Open it from Agents (enter) to chat / onboard.\"\n")
		return b.String()
	}
	// Generic fallback (should not hit for catalog agents).
	b.WriteString(fmt.Sprintf("if ! command -v %s >/dev/null 2>&1; then\n", a.cli))
	b.WriteString(fmt.Sprintf("  echo \"[deploy] installing %s…\"\n", a.cli))
	b.WriteString(fmt.Sprintf("  if command -v ollama >/dev/null 2>&1; then ollama launch %s --yes --model %s </dev/null || true; fi\n", a.cli, model))
	b.WriteString("fi\n")
	b.WriteString(fmt.Sprintf("if ! command -v %s >/dev/null 2>&1; then\n", a.cli))
	b.WriteString(fmt.Sprintf("  echo \"[deploy] ERROR: %s not found on PATH after install\" >&2\n", a.cli))
	b.WriteString("  exit 1\nfi\n")
	b.WriteString(fmt.Sprintf("echo \"[deploy] %s ready: $(command -v %s)\"\n", a.cli, a.cli))
	return b.String()
}

func (m *model) agentConfigScript(a agentDef) string {
	model := m.effDefaultModel()
	switch a.name {
	case "OpenCode":
		return m.openCodeConfigScript(model)
	case "Goose":
		return m.gooseConfigScript(model)
	case "Grok Build":
		return m.grokConfigScript(model)
	case "Claude Code":
		return m.claudeCodeConfigScript(model)
	case "Codex":
		return m.codexConfigScript(model)
	default:
		return ""
	}
}

func (m *model) agentUpdateScript(a agentDef) string {
	switch a.name {
	case "Crush":
		return crushUpdateScript
	case "Hermes":
		return "set -e\nexport HERMES_NONINTERACTIVE=1\n" + depsBootstrap + hermesUpdateFragment + m.hermesOllaConfigScript(m.effDefaultModel())
	default:
		return m.agentDeployScript(a)
	}
}

// hermesGatewayWanted reports whether a Hermes deploy should also set up the
// messaging gateway (needs the feature enabled and a bot token configured).
func (m *model) hermesGatewayWanted() bool {
	return m.hermesCfg.GatewayEnabled && strings.TrimSpace(m.hermesCfg.TelegramBotToken) != ""
}

// hermesEnvPy upserts the Telegram keys in ~/.hermes/.env, replacing any
// existing (commented or live) lines and writing the file 0600. Only non-empty
// values are written. Inputs arrive via env vars so secrets stay out of code.
const hermesEnvPy = `import os, pathlib, re
p = pathlib.Path(os.path.expanduser("~/.hermes/.env"))
p.parent.mkdir(parents=True, exist_ok=True)
text = p.read_text() if p.exists() else ""
updates = {
    "TELEGRAM_BOT_TOKEN": os.environ.get("TG_TOKEN", ""),
    "TELEGRAM_ALLOWED_USERS": os.environ.get("TG_ALLOWED", ""),
    "TELEGRAM_HOME_CHANNEL": os.environ.get("TG_HOME", ""),
}
keys = [k for k, v in updates.items() if v]
out = []
for ln in text.splitlines():
    s = ln.lstrip()
    if any(re.match(r"^#?\s*" + re.escape(k) + r"\s*=", s) for k in keys):
        continue
    out.append(ln)
for k in ("TELEGRAM_BOT_TOKEN", "TELEGRAM_ALLOWED_USERS", "TELEGRAM_HOME_CHANNEL"):
    if updates[k]:
        out.append(k + "=" + updates[k])
p.write_text("\n".join(out).rstrip("\n") + "\n")
os.chmod(p, 0o600)
print("[deploy] .env updated:", ",".join(keys) or "(none)")
`

// hermesGatewayScript writes the Telegram credentials to ~/.hermes/.env and
// installs + starts the messaging gateway unattended (user-level by default, or
// a root system service). It ends by printing gateway status for verification.
func (m *model) hermesGatewayScript() string {
	h := m.hermesCfg
	b64 := base64.StdEncoding.EncodeToString([]byte(hermesEnvPy))
	var b strings.Builder
	b.WriteString(`echo "[deploy] configuring Telegram credentials…"
PY="$HOME/.hermes/hermes-agent/venv/bin/python"; [ -x "$PY" ] || PY=python3
echo ` + b64 + ` | base64 -d > /tmp/aidt-hermes-env.py
`)
	b.WriteString(fmt.Sprintf("TG_TOKEN='%s' TG_ALLOWED='%s' TG_HOME='%s' \"$PY\" /tmp/aidt-hermes-env.py\n",
		shSingle(h.TelegramBotToken), shSingle(h.TelegramAllowedUsers), shSingle(h.TelegramHomeChannel)))
	b.WriteString(`export PATH="$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"
HERMES_BIN="$(command -v hermes)"
if [ -z "$HERMES_BIN" ] || [ ! -x "$HERMES_BIN" ]; then
  echo "[deploy] ERROR: hermes not executable for gateway install" >&2
  exit 1
fi
`)
	if h.gatewayMode() == "system" {
		b.WriteString(`echo "[deploy] installing gateway (system service)…"
sudo HERMES_NONINTERACTIVE=1 HOME="$HOME" "$HERMES_BIN" gateway install --system --run-as-user "$USER" </dev/null
`)
	} else {
		b.WriteString(`echo "[deploy] installing gateway (user service)…"
HERMES_NONINTERACTIVE=1 "$HERMES_BIN" gateway install </dev/null
loginctl enable-linger "$USER" >/dev/null 2>&1 || true
HERMES_NONINTERACTIVE=1 "$HERMES_BIN" gateway start </dev/null || true
`)
	}
	b.WriteString(`echo "[deploy] gateway status:"
HERMES_NONINTERACTIVE=1 "$HERMES_BIN" gateway status </dev/null 2>&1 | head -20 || true
`)
	return b.String()
}

// hermesOllaPy rewrites ~/.hermes/config.yaml's model block to use the Olla
// OpenAI endpoint as a custom provider, preserving every other key. Run with
// Hermes' own venv python (which ships PyYAML). Inputs come from env vars so no
// shell quoting of user values is needed.
const hermesOllaPy = `import os, pathlib, yaml
p = pathlib.Path(os.path.expanduser("~/.hermes/config.yaml"))
cfg = yaml.safe_load(p.read_text()) if p.exists() else {}
cfg = cfg or {}
m = cfg.get("model")
if not isinstance(m, dict):
    m = {}
m["provider"] = "custom"
m["base_url"] = os.environ["OLLA_BASE"]
m["api_key"] = os.environ["OLLA_KEY"]
m["default"] = os.environ["OLLA_MODEL"]
m.pop("api_mode", None)
cfg["model"] = m
p.parent.mkdir(parents=True, exist_ok=True)
p.write_text(yaml.safe_dump(cfg, sort_keys=False))
print("[deploy] hermes -> Olla", m["base_url"], m["default"])
`

// hermesOllaConfigScript produces the shell that points Hermes at the Olla
// OpenAI endpoint. Hermes trusts a remote base_url when provider == "custom".
func (m *model) hermesOllaConfigScript(model string) string {
	base := strings.TrimRight(m.gateway, "/") + "/olla/openai/v1"
	key := orDefault(m.token, "olla")
	b64 := base64.StdEncoding.EncodeToString([]byte(hermesOllaPy))
	return fmt.Sprintf(`echo "[deploy] pointing hermes at Olla (%s)…"
PY="$HOME/.hermes/hermes-agent/venv/bin/python"; [ -x "$PY" ] || PY=python3
echo %s | base64 -d > /tmp/aidt-hermes-olla.py
OLLA_BASE='%s' OLLA_KEY='%s' OLLA_MODEL='%s' "$PY" /tmp/aidt-hermes-olla.py
`, base, b64, shSingle(base), shSingle(key), shSingle(model))
}

// openCodeConfigScript writes an AIDT-owned configuration and leaves any normal
// OpenCode config untouched. OPENCODE_CONFIG selects it when AIDT launches the
// agent. The same token protects the HTTP server using OpenCode Basic Auth.
func (m *model) openCodeConfigScript(defaultModel string) string {
	models := map[string]any{}
	add := func(name string) {
		if strings.TrimSpace(name) != "" {
			models[name] = map[string]any{"name": name}
		}
	}
	add(defaultModel)
	for _, md := range m.models {
		add(md.Name)
	}
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"model":   "olla/" + defaultModel,
		"provider": map[string]any{
			"olla": map[string]any{
				"npm":  "@ai-sdk/openai-compatible",
				"name": "Olla",
				"options": map[string]any{
					"baseURL": strings.TrimRight(m.gateway, "/") + "/olla/openai/v1",
					"apiKey":  orDefault(m.token, "olla"),
				},
				"models": models,
			},
		},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	serverPassword := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\r", "", "\n", "").Replace(orDefault(m.token, "olla"))
	serverEnv := "OPENCODE_SERVER_USERNAME=opencode\nOPENCODE_SERVER_PASSWORD=\"" + serverPassword + "\"\n"
	encoded := base64.StdEncoding.EncodeToString(b)
	serverEnvEncoded := base64.StdEncoding.EncodeToString([]byte(serverEnv))
	return fmt.Sprintf(`echo "[deploy] writing AIDT OpenCode configuration…"
(
umask 077
mkdir -p "$HOME/.config/aidt"
echo %s | base64 -d > "$HOME/.config/aidt/opencode.json"
chmod 600 "$HOME/.config/aidt/opencode.json"
echo %s | base64 -d > "$HOME/.config/aidt/opencode-server.env"
chmod 600 "$HOME/.config/aidt/opencode-server.env"
)
`, encoded, serverEnvEncoded)
}

// gooseConfigScript registers a named OpenAI-compatible provider using Goose's
// documented custom-provider format. A separate mode-0600 env file supplies
// the API key without putting it in the provider JSON or launch argv.
func (m *model) gooseConfigScript(defaultModel string) string {
	models := []map[string]any{}
	seen := map[string]bool{}
	add := func(name string) {
		if strings.TrimSpace(name) != "" && !seen[name] {
			seen[name] = true
			models = append(models, map[string]any{"name": name, "context_limit": 128000})
		}
	}
	add(defaultModel)
	for _, md := range m.models {
		add(md.Name)
	}
	cfg := map[string]any{
		"name":               "olla",
		"engine":             "openai",
		"display_name":       "Olla",
		"description":        "AIDT Olla gateway",
		"api_key_env":        "OLLA_API_KEY",
		"base_url":           strings.TrimRight(m.gateway, "/") + "/olla/openai/v1/chat/completions",
		"models":             models,
		"supports_streaming": true,
		"requires_auth":      true,
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	encoded := base64.StdEncoding.EncodeToString(b)
	env := fmt.Sprintf("export OLLA_API_KEY='%s'\n", shSingle(orDefault(m.token, "olla")))
	envEncoded := base64.StdEncoding.EncodeToString([]byte(env))
	return fmt.Sprintf(`echo "[deploy] writing Olla provider for Goose…"
(
umask 077
mkdir -p "$HOME/.config/goose/custom_providers"
echo %s | base64 -d > "$HOME/.config/goose/custom_providers/olla.json"
chmod 600 "$HOME/.config/goose/custom_providers/olla.json"
echo %s | base64 -d > "$HOME/.config/aidt/goose.env"
chmod 600 "$HOME/.config/aidt/goose.env"
)
`, encoded, envEncoded)
}

// grokConfigScript creates a separate GROK_HOME for AIDT so selecting Grok
// Build does not overwrite a user's normal Grok configuration.
func (m *model) grokConfigScript(model string) string {
	base := strings.TrimRight(m.gateway, "/") + "/olla/openai/v1"
	quote := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	config := fmt.Sprintf(`[model.olla]
model = %s
base_url = %s
name = "Olla"
api_key = %s
api_backend = "chat_completions"

[models]
default = "olla"
`, quote(model), quote(base), quote(orDefault(m.token, "olla")))
	encoded := base64.StdEncoding.EncodeToString([]byte(config))
	return fmt.Sprintf(`echo "[deploy] writing AIDT Grok Build configuration…"
(
umask 077
mkdir -p "$HOME/.config/aidt/grok"
echo %s | base64 -d > "$HOME/.config/aidt/grok/config.toml"
chmod 600 "$HOME/.config/aidt/grok/config.toml"
)
`, encoded)
}

// claudeCodeConfigScript stores the documented Olla environment in a protected
// AIDT env file. Sourcing it at launch keeps credentials out of ssh argv.
func (m *model) claudeCodeConfigScript(model string) string {
	base := strings.TrimRight(m.gateway, "/") + "/olla/anthropic"
	key := orDefault(m.token, "olla")
	var env strings.Builder
	for _, item := range []struct{ name, value string }{
		{"ANTHROPIC_BASE_URL", base},
		{"ANTHROPIC_AUTH_TOKEN", key},
		{"ANTHROPIC_MODEL", model},
		{"ANTHROPIC_DEFAULT_HAIKU_MODEL", model},
		{"ANTHROPIC_DEFAULT_SONNET_MODEL", model},
		{"ANTHROPIC_DEFAULT_OPUS_MODEL", model},
		{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1"},
		{"API_TIMEOUT_MS", "3000000"},
	} {
		fmt.Fprintf(&env, "export %s='%s'\n", item.name, shSingle(item.value))
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(env.String()))
	return fmt.Sprintf(`echo "[deploy] writing AIDT Claude Code environment…"
(
umask 077
mkdir -p "$HOME/.config/aidt"
echo %s | base64 -d > "$HOME/.config/aidt/claude-code.env"
chmod 600 "$HOME/.config/aidt/claude-code.env"
)
`, encoded)
}

// codexConfigScript keeps AIDT's provider and token separate from the user's
// normal Codex state. Current Codex releases use the Responses API for custom
// providers, which Olla exposes below its OpenAI-compatible base URL.
func (m *model) codexConfigScript(model string) string {
	quote := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	base := strings.TrimRight(m.gateway, "/") + "/olla/openai/v1"
	config := fmt.Sprintf(`model = %s
model_provider = "aidt_olla"

[model_providers.aidt_olla]
name = "Olla"
base_url = %s
env_key = "OLLA_API_KEY"
wire_api = "responses"
`, quote(model), quote(base))
	env := fmt.Sprintf("export OLLA_API_KEY='%s'\n", shSingle(orDefault(m.token, "olla")))
	configEncoded := base64.StdEncoding.EncodeToString([]byte(config))
	envEncoded := base64.StdEncoding.EncodeToString([]byte(env))
	return fmt.Sprintf(`echo "[deploy] writing AIDT Codex configuration..."
(
umask 077
mkdir -p "$HOME/.config/aidt/codex"
echo %s | base64 -d > "$HOME/.config/aidt/codex/config.toml"
chmod 600 "$HOME/.config/aidt/codex/config.toml"
echo %s | base64 -d > "$HOME/.config/aidt/codex.env"
chmod 600 "$HOME/.config/aidt/codex.env"
)
`, configEncoded, envEncoded)
}

// agentUninstallScript returns the shell that removes an agent install from a
// host. Best-effort by design: each step tolerates a partial/older install, and
// user data that isn't clearly the agent's own install tree is left alone
// (Crush keeps ~/.config/crush so a later reinstall finds its providers).
func agentUninstallScript(a agentDef) string {
	switch a.name {
	case "Crush":
		return `echo "[remove] uninstalling crush…"
if [ "$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi
. /etc/os-release 2>/dev/null || true
case "${ID:-} ${ID_LIKE:-}" in
  *ubuntu*|*debian*) $SUDO apt-get remove -y crush 2>/dev/null || true ;;
  *)
    if command -v dnf >/dev/null 2>&1; then $SUDO dnf remove -y crush 2>/dev/null || true
    elif command -v yum >/dev/null 2>&1; then $SUDO yum remove -y crush 2>/dev/null || true
    fi
    ;;
esac
export PATH="$HOME/.npm-global/bin:$PATH"
npm uninstall -g crush >/dev/null 2>&1 || true
rm -f "$HOME/.local/bin/crush"
echo "[remove] crush removed (config kept in ~/.config/crush)"
`
	case "Hermes":
		return `echo "[remove] uninstalling hermes…"
HERMES_BIN="$(command -v hermes || echo "$HOME/.local/bin/hermes")"
if [ -x "$HERMES_BIN" ]; then
  HERMES_NONINTERACTIVE=1 "$HERMES_BIN" gateway stop </dev/null >/dev/null 2>&1 || true
  HERMES_NONINTERACTIVE=1 "$HERMES_BIN" gateway uninstall </dev/null >/dev/null 2>&1 || true
fi
systemctl --user disable --now hermes-gateway >/dev/null 2>&1 || true
sudo systemctl disable --now hermes-gateway >/dev/null 2>&1 || true
export PATH="$HOME/.npm-global/bin:$PATH"
npm uninstall -g hermes >/dev/null 2>&1 || true
rm -f "$HOME/.local/bin/hermes" "$HOME/.npm-global/bin/hermes"
rm -rf "$HOME/.hermes"
echo "[remove] hermes removed"
`
	case "OpenCode":
		return `echo "[remove] uninstalling opencode…"
if [ "$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi
$SUDO systemctl disable --now aidt-opencode.service >/dev/null 2>&1 || true
$SUDO rm -f /etc/systemd/system/aidt-opencode.service
$SUDO systemctl daemon-reload >/dev/null 2>&1 || true
if command -v firewall-cmd >/dev/null 2>&1 && $SUDO firewall-cmd --state >/dev/null 2>&1; then
  $SUDO firewall-cmd --permanent --remove-port=4096/tcp >/dev/null 2>&1 || true
  $SUDO firewall-cmd --reload >/dev/null 2>&1 || true
fi
rm -f "$HOME/.local/bin/opencode" "$HOME/.opencode/bin/opencode"
rm -rf "$HOME/.opencode" "$HOME/.config/aidt/opencode.json" "$HOME/.config/aidt/opencode-server.env"
echo "[remove] OpenCode removed"
`
	case "Goose":
		return `echo "[remove] uninstalling goose…"
rm -f "$HOME/.local/bin/goose"
rm -f "$HOME/.config/goose/custom_providers/olla.json"
rm -f "$HOME/.config/aidt/goose.env"
echo "[remove] Goose removed"
`
	case "Grok Build":
		return `echo "[remove] uninstalling Grok Build…"
for LINK in "$HOME/.local/bin/grok" "$HOME/.local/bin/agent"; do
  case "$(readlink "$LINK" 2>/dev/null || true)" in
    *"/.grok/bin/grok"|*"/.grok/bin/agent") rm -f "$LINK" ;;
  esac
done
rm -f "$HOME/.grok/bin/grok" "$HOME/.grok/bin/agent"
rm -rf "$HOME/.grok/downloads" "$HOME/.grok/completions" "$HOME/.config/aidt/grok"
echo "[remove] Grok Build removed (user auth and settings kept in ~/.grok)"
`
	case "Claude Code":
		return `echo "[remove] uninstalling Claude Code…"
rm -f "$HOME/.local/bin/claude"
rm -rf "$HOME/.local/share/claude"
rm -f "$HOME/.config/aidt/claude-code.env"
echo "[remove] Claude Code removed (user settings kept in ~/.claude)"
`
	case "Codex":
		return `echo "[remove] uninstalling Codex..."
rm -f "$HOME/.local/bin/codex" "$HOME/.codex/bin/codex"
rm -rf "$HOME/.config/aidt/codex" "$HOME/.config/aidt/codex.env"
echo "[remove] Codex removed (user settings kept in ~/.codex)"
`
	}
	return ""
}

// agentOpenCmd launches each CLI with Olla selected in the shared Obsidian vault.
// AIDT-owned mode-0600 files keep credentials out of long-lived ssh argv.
func (m *model) agentOpenCmd(a agentDef) string {
	model := m.effDefaultModel()
	// Enter the shared vault with the agent's identity and the aidt-* helpers on
	// PATH, so it can answer "who am I" and claim queue work without setup.
	// Double quotes here on purpose: loginShell wraps the whole command in a
	// single-quoted `bash -lc '…'`, so single quotes would come out escaped as
	// '\''…'\''. agentID only ever emits [a-z0-9-], so this stays unambiguous.
	workspace := fmt.Sprintf(`export AIDT_AGENT_VAULT="$HOME/Obsidian/AIDT-Agent-Vault"; `+
		`export AIDT_AGENT_ID="%s"; export PATH="$AIDT_AGENT_VAULT/bin:$PATH"; `+
		`mkdir -p "$HOME/Obsidian/AIDT-Agent-Vault/.obsidian" && `+
		`cd "$HOME/Obsidian/AIDT-Agent-Vault" && `,
		shSingle(agentID(a.name)))

	var cmd string
	switch a.name {
	case "Crush":
		cmd = workspace + "exec crush"
	case "OpenCode":
		cmd = fmt.Sprintf("export OPENCODE_CONFIG=\"$HOME/.config/aidt/opencode.json\"; %sexec opencode", workspace)
	case "Goose":
		cmd = fmt.Sprintf(". \"$HOME/.config/aidt/goose.env\"; export GOOSE_PROVIDER=olla GOOSE_MODEL='%s'; %sexec goose session", shSingle(model), workspace)
	case "Grok Build":
		cmd = fmt.Sprintf("export GROK_HOME=\"$HOME/.config/aidt/grok\"; %sexec grok", workspace)
	case "Claude Code":
		cmd = fmt.Sprintf(". \"$HOME/.config/aidt/claude-code.env\"; %sexec claude", workspace)
	case "Codex":
		cmd = fmt.Sprintf(". \"$HOME/.config/aidt/codex.env\"; export CODEX_HOME=\"$HOME/.config/aidt/codex\"; %sexec codex", workspace)
	default:
		cmd = workspace + "exec " + a.cli
	}
	return loginShell(cmd)
}

// agentRemovedMsg reports the outcome of removing an agent's deployment(s):
// per-host uninstalls for host agents, or container deletions for Nanoclaw.
type agentRemovedMsg struct {
	agent          string
	container      bool
	removed        int      // uninstalled hosts, or deleted containers
	attemptedHosts []string // selected hosts to forget, even if remote cleanup failed
	okHosts        []string // hosts whose uninstall succeeded (host agents)
	errs           []string
}

// agentUninstallCmd runs an agent's uninstall script on every listed host in
// parallel (locally when a host is this machine) and reports which succeeded.
func agentUninstallCmd(a agentDef, hosts []string, user, pass string) tea.Cmd {
	script := agentUninstallScript(a)
	if script == "" {
		return func() tea.Msg { return notifyMsg(a.name + " has no uninstall routine") }
	}
	return func() tea.Msg {
		msg := agentRemovedMsg{agent: a.name, attemptedHosts: append([]string(nil), hosts...)}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, h := range hosts {
			wg.Add(1)
			go func(h string) {
				defer wg.Done()
				var out string
				var err error
				switch {
				case isLocalHost(h):
					b, e := exec.Command("bash", "-lc", script).CombinedOutput()
					out, err = string(b), e
				default:
					client, e := dialSSH(h, user, pass)
					if e != nil {
						err = e
					} else {
						out, err = runSSH(client, script)
						client.Close()
					}
				}
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					msg.errs = append(msg.errs, h+": "+strings.TrimSpace(err.Error()+" "+lastNonEmptyLine(out)))
					return
				}
				msg.okHosts = append(msg.okHosts, h)
			}(h)
		}
		wg.Wait()
		sort.Strings(msg.attemptedHosts)
		sort.Strings(msg.okHosts)
		sort.Strings(msg.errs)
		msg.removed = len(msg.okHosts)
		return msg
	}
}

// agentDeployManyCmd runs the headless deployment on every selected host in
// parallel. Successful hosts are registered even when another host fails.
type agentBatchDeployOptions struct {
	scripts         map[string]string
	crushConfig     string
	gatewayHost     string
	gatewayFallback string
}

func runAgentDeployScript(host, user, pass, script string) (string, error) {
	if isLocalHost(host) {
		cmd := exec.Command("bash", "-l", "-s")
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	client, err := dialSSH(host, user, pass)
	if err != nil {
		return "", err
	}
	defer client.Close()
	return runSSHInput(client, "bash -l -s", script)
}

func agentDeployManyCmd(a agentDef, hosts []string, user, pass string, opts agentBatchDeployOptions) tea.Cmd {
	return func() tea.Msg {
		msg := agentBatchDeployedMsg{agent: a.name}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, host := range hosts {
			host := host
			script := opts.scripts[host]
			wg.Add(1)
			go func() {
				defer wg.Done()
				var out string
				var err error
				if a.name == "Crush" {
					if isLocalHost(host) {
						err = localMergeCrushConfig(opts.crushConfig)
					} else {
						err = sshMergeCrushConfig(host, user, pass, opts.crushConfig)
					}
				}
				if err == nil && script == "" {
					err = fmt.Errorf("no deployment script generated")
				}
				if err == nil {
					out, err = runAgentDeployScript(host, user, pass, script)
				}
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					msg.errs = append(msg.errs, host+": "+strings.TrimSpace(err.Error()+" "+lastNonEmptyLine(out)))
					return
				}
				msg.okHosts = append(msg.okHosts, host)
			}()
		}
		wg.Wait()
		sort.Strings(msg.okHosts)
		if opts.gatewayFallback != "" {
			if containsStr(msg.okHosts, opts.gatewayHost) {
				msg.gatewayHost = opts.gatewayHost
			} else if len(msg.okHosts) > 0 {
				fallbackHost := msg.okHosts[0]
				out, err := runAgentDeployScript(fallbackHost, user, pass, opts.gatewayFallback)
				if err != nil {
					msg.errs = append(msg.errs, fallbackHost+" Telegram gateway: "+strings.TrimSpace(err.Error()+" "+lastNonEmptyLine(out)))
				} else {
					msg.gatewayHost = fallbackHost
				}
			}
		}
		sort.Strings(msg.errs)
		return msg
	}
}

func agentByName(name string) (agentDef, bool) {
	for _, a := range agentCatalog {
		if a.name == name {
			return a, true
		}
	}
	return agentDef{}, false
}

// forgetAgentHost removes every agent registration that points at any alias of
// a deleted machine. It returns the affected agent names for user-facing status.
func (m *model) forgetAgentHost(hosts ...string) []string {
	remove := map[string]bool{}
	for _, host := range hosts {
		if host != "" {
			remove[host] = true
		}
	}
	if len(remove) == 0 {
		return nil
	}
	var forgotten []string
	seen := map[string]bool{}
	for agent, hosts := range m.agentHosts {
		var kept []string
		for _, h := range hosts {
			if !remove[h] {
				kept = append(kept, h)
			}
		}
		if len(kept) == len(hosts) {
			continue
		}
		seen[agent] = true
		forgotten = append(forgotten, agent)
		if len(kept) == 0 {
			delete(m.agentHosts, agent)
			delete(m.agentReg, agent)
			continue
		}
		m.agentHosts[agent] = kept
		if remove[m.agentReg[agent]] {
			m.agentReg[agent] = kept[0]
		}
	}
	for agent, h := range m.agentReg {
		if remove[h] && !seen[agent] {
			forgotten = append(forgotten, agent)
			delete(m.agentReg, agent)
		}
	}
	if len(forgotten) > 0 {
		sort.Strings(forgotten)
		_ = saveAgentReg(m.tokFile, m.agentReg, m.agentHosts)
		m.refreshAgents()
	}
	return forgotten
}

// loginShell wraps a simple command so it runs in a login shell, picking up the
// user's full PATH (ollama in /usr/local/bin, crush/npm bins in ~/.local/bin,
// etc.). Without this, `ssh host crush` runs in a minimal non-login PATH and
// fails with 127 even when the tool is installed.
func loginShell(cmd string) string {
	// Prepend common install dirs inside the login shell too — some images'
	// .bash_profile never sources .bashrc, so npm-global/local bins stay hidden.
	inner := `export PATH="/usr/local/bin:$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/.opencode/bin:$HOME/.grok/bin:$HOME/.codex/bin:$PATH"; ` + cmd
	return "bash -lc '" + shSingle(inner) + "'"
}

// crushConfigJSON builds a crush.json that registers the Olla gateway as an
// OpenAI-type provider named "olla" (so model names show through), pointing at
// the OpenAI-compatible base URL with the configured token and discovered
// models.
func (m *model) crushConfigJSON() string {
	base := strings.TrimRight(m.gateway, "/") + "/olla/openai/v1"
	key := orDefault(m.token, "olla")

	type cmodel struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		ContextWindow    int    `json:"context_window"`
		DefaultMaxTokens int    `json:"default_max_tokens"`
	}
	var models []cmodel
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		models = append(models, cmodel{ID: name, Name: name, ContextWindow: 32000, DefaultMaxTokens: 4096})
	}
	add(m.effDefaultModel())
	for _, md := range m.models {
		add(md.Name)
	}

	cfg := map[string]any{
		"$schema": "https://charm.land/crush.json",
		"providers": map[string]any{
			"olla": map[string]any{
				"type":     "openai",
				"base_url": base,
				"api_key":  key,
				"models":   models,
			},
		},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return string(b)
}
