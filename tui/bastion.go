package main

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"
)

// The bastion flow makes the Olla gateway the operator's way into a deployed
// MicroK8s cluster: kubectl is installed there and the cluster is merged into
// the gateway user's ~/.kube/config.
//
// The kubeconfig carries admin credentials, so it is never written to the
// deploy log, never passed as a command argument, and never surfaced in the
// TUI. It moves in memory from the node to the gateway and lands in a
// mode-0600 file (see sshWriteFile).

// kubectlInstallFragment installs kubectl when it is missing. The official
// binary is used rather than a package so this works on both the Rocky guests
// AIDT provisions by default and Ubuntu, and the published checksum is
// verified before anything is installed.
const kubectlInstallFragment = `if command -v kubectl >/dev/null 2>&1; then
  echo "[bastion] kubectl already installed: $(command -v kubectl)"
else
  echo "[bastion] installing kubectl…"
  case "$(uname -m)" in
    x86_64|amd64)  KARCH=amd64 ;;
    aarch64|arm64) KARCH=arm64 ;;
    *) echo "[bastion] ERROR: unsupported architecture $(uname -m)" >&2; exit 1 ;;
  esac
  KVER="$(curl -fsSL --max-time 30 https://dl.k8s.io/release/stable.txt)"
  [ -n "$KVER" ] || { echo "[bastion] ERROR: could not resolve the kubectl version" >&2; exit 1; }
  KTMP="$(mktemp -d)"
  curl -fsSL --max-time 300 -o "$KTMP/kubectl" "https://dl.k8s.io/release/$KVER/bin/linux/$KARCH/kubectl"
  curl -fsSL --max-time 60 -o "$KTMP/kubectl.sha256" "https://dl.k8s.io/release/$KVER/bin/linux/$KARCH/kubectl.sha256"
  echo "$(cat "$KTMP/kubectl.sha256")  $KTMP/kubectl" | sha256sum -c - >/dev/null ||
    { rm -rf "$KTMP"; echo "[bastion] ERROR: kubectl checksum mismatch" >&2; exit 1; }
  if [ "$(id -u)" -eq 0 ]; then
    install -m 0755 "$KTMP/kubectl" /usr/local/bin/kubectl
  elif sudo -n true 2>/dev/null; then
    sudo -n install -m 0755 "$KTMP/kubectl" /usr/local/bin/kubectl
  else
    mkdir -p "$HOME/.local/bin"
    install -m 0755 "$KTMP/kubectl" "$HOME/.local/bin/kubectl"
    export PATH="$HOME/.local/bin:$PATH"
    grep -q '.local/bin' "$HOME/.bashrc" 2>/dev/null ||
      echo 'export PATH="$HOME/.local/bin:$PATH"' >> "$HOME/.bashrc"
  fi
  rm -rf "$KTMP"
  command -v kubectl >/dev/null 2>&1 || { echo "[bastion] ERROR: kubectl not on PATH after install" >&2; exit 1; }
  echo "[bastion] kubectl ready: $(command -v kubectl)"
fi
`

// bastionMergeScript installs kubectl, then merges a kubeconfig arriving on
// stdin into the user's ~/.kube/config. It takes the context name as $1 so no
// credential is ever an argument.
const bastionMergeScript = `#!/usr/bin/env bash
set -euo pipefail
umask 077
export PATH="/usr/local/bin:$HOME/.local/bin:$PATH"

CTX="${1:-}"
[ -n "$CTX" ] || { echo "[bastion] ERROR: no context name given" >&2; exit 1; }

# Ignore any KUBECONFIG inherited from the environment. This script always
# targets the user's default config, and an inherited value would redirect both
# the reads and the use-context write — which modifies a file — to somewhere we
# never backed up. Per-command KUBECONFIG below is set explicitly.
unset KUBECONFIG

KUBE_DIR="$HOME/.kube"
mkdir -p "$KUBE_DIR"
INCOMING="$(mktemp)"
MERGED="$(mktemp)"
ERRLOG="$(mktemp)"
trap 'rm -f "$INCOMING" "$MERGED" "$ERRLOG"' EXIT
cat > "$INCOMING"
[ -s "$INCOMING" ] || { echo "[bastion] ERROR: empty kubeconfig on stdin" >&2; exit 1; }

` + kubectlInstallFragment + `

# Always keep a standalone copy. If the operator's existing kubeconfig turns out
# to be unmergeable, this is still a working way to reach the cluster.
STANDALONE="$KUBE_DIR/aidt-$CTX.conf"
cp "$INCOMING" "$STANDALONE"
chmod 600 "$STANDALONE"

if [ ! -s "$KUBE_DIR/config" ]; then
  cp "$INCOMING" "$KUBE_DIR/config"
  chmod 600 "$KUBE_DIR/config"
  kubectl config use-context "$CTX" >/dev/null
  echo "AIDT_BASTION_MERGED $CTX"
  echo "[bastion] context '$CTX' is active in $KUBE_DIR/config"
else
  # Never replace a config we have not backed up.
  cp "$KUBE_DIR/config" "$KUBE_DIR/config.aidt-bak"
  chmod 600 "$KUBE_DIR/config.aidt-bak"

  merged=0
  # Preferred: --flatten inlines every referenced cert so the result stands
  # alone. It reads those files, though, so one stale entry pointing at a
  # deleted cert is enough to fail the whole merge.
  if KUBECONFIG="$KUBE_DIR/config:$INCOMING" kubectl config view --flatten > "$MERGED" 2>"$ERRLOG"; then
    merged=1
  elif KUBECONFIG="$KUBE_DIR/config:$INCOMING" kubectl config view --raw > "$MERGED" 2>>"$ERRLOG"; then
    # Fall back to keeping file references as-is. --raw still emits real
    # credentials (a plain "config view" would redact them into a broken file),
    # but it never opens the files an existing entry points at.
    merged=1
    echo "[bastion] NOTE: an existing kubeconfig entry references a missing file;"
    echo "[bastion]       merged without inlining certificates."
  fi

  # A merge that dropped our context is worse than no merge at all.
  if [ "$merged" = 1 ] && [ -s "$MERGED" ] &&
     kubectl --kubeconfig="$MERGED" config get-contexts "$CTX" >/dev/null 2>&1; then
    cp "$MERGED" "$KUBE_DIR/config"
    chmod 600 "$KUBE_DIR/config"
    kubectl config use-context "$CTX" >/dev/null
    echo "AIDT_BASTION_MERGED $CTX"
    echo "[bastion] context '$CTX' is active in $KUBE_DIR/config"
    echo "[bastion] previous kubeconfig saved as $KUBE_DIR/config.aidt-bak"
  else
    echo "[bastion] WARNING: could not merge into $KUBE_DIR/config, which is unchanged." >&2
    sed 's/^/[bastion]   /' "$ERRLOG" >&2 || true
    echo "AIDT_BASTION_STANDALONE $STANDALONE"
    echo "[bastion] The cluster is still reachable from this host:"
    echo "[bastion]   export KUBECONFIG=$STANDALONE"
    echo "[bastion]   kubectl get nodes"
    KUBECONFIG="$STANDALONE" kubectl get nodes 2>&1 | head -5 ||
      echo "[bastion] NOTE: kubectl could not reach the cluster yet"
    exit 0
  fi
fi

kubectl get nodes 2>&1 | head -5 || echo "[bastion] NOTE: kubectl could not reach the cluster yet"
`

// retargetKubeconfig renames a MicroK8s kubeconfig's cluster, user, and context
// so several clusters can coexist in one file, and points it at server.
//
// microk8s config always emits the same three names (microk8s-cluster / admin /
// microk8s). Merging a second cluster without renaming would silently overwrite
// the first, and "admin" in particular is certain to collide. The document is
// rewritten through a YAML round-trip rather than by pattern substitution so a
// certificate that happens to contain a matching string cannot be corrupted.
func retargetKubeconfig(raw, slug, server string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("empty kubeconfig")
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return "", fmt.Errorf("parse kubeconfig: %w", err)
	}
	if slug == "" {
		slug = "microk8s"
	}
	clusterName, userName := slug+"-cluster", slug+"-admin"

	clusters, _ := doc["clusters"].([]any)
	if len(clusters) == 0 {
		return "", errors.New("kubeconfig declares no clusters")
	}
	for _, c := range clusters {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		cm["name"] = clusterName
		if inner, ok := cm["cluster"].(map[string]any); ok && server != "" {
			// microk8s can emit a loopback address, which is useless from the
			// bastion. Pin the node's routable address instead.
			inner["server"] = server
		}
	}

	users, _ := doc["users"].([]any)
	if len(users) == 0 {
		return "", errors.New("kubeconfig declares no users")
	}
	for _, u := range users {
		if um, ok := u.(map[string]any); ok {
			um["name"] = userName
		}
	}

	contexts, _ := doc["contexts"].([]any)
	if len(contexts) == 0 {
		return "", errors.New("kubeconfig declares no contexts")
	}
	for _, c := range contexts {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		cm["name"] = slug
		if inner, ok := cm["context"].(map[string]any); ok {
			inner["cluster"] = clusterName
			inner["user"] = userName
		}
	}
	doc["current-context"] = slug

	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render kubeconfig: %w", err)
	}
	return string(out), nil
}

// fetchMicroK8sKubeconfig reads the admin kubeconfig from a MicroK8s node.
func fetchMicroK8sKubeconfig(host, user, pass string) (string, error) {
	client, err := dialSSH(host, user, pass)
	if err != nil {
		return "", fmt.Errorf("connect to %s: %w", host, err)
	}
	defer client.Close()
	out, err := runSSH(client, "sudo -n microk8s config 2>/dev/null || microk8s config")
	if err != nil {
		return "", fmt.Errorf("read kubeconfig from %s: %w", host, err)
	}
	// The output starts at the document root; anything before it is sudo noise.
	if i := strings.Index(out, "apiVersion:"); i > 0 {
		out = out[i:]
	}
	if !strings.Contains(out, "clusters:") {
		return "", fmt.Errorf("unexpected kubeconfig from %s", host)
	}
	return out, nil
}

// installBastionKubeconfig installs kubectl on the bastion and merges the
// cluster in. The kubeconfig travels over stdin, never as an argument.
func installBastionKubeconfig(host, user, pass, ctx, kubeconfig string) (string, error) {
	const remote = "$HOME/.aidt-bastion.sh"
	if err := uploadRemoteScript(host, user, pass, remote, bastionMergeScript); err != nil {
		return "", fmt.Errorf("stage bastion script on %s: %w", host, err)
	}
	client, err := dialSSH(host, user, pass)
	if err != nil {
		return "", fmt.Errorf("connect to %s: %w", host, err)
	}
	defer client.Close()
	cmd := "bash " + remote + " '" + shSingle(ctx) + "'; rc=$?; rm -f " + remote + "; exit $rc"
	out, err := runSSHInput(client, cmd, kubeconfig)
	if err != nil {
		return out, fmt.Errorf("configure bastion on %s: %w", host, err)
	}
	return out, nil
}

// bastionReadyMsg reports the outcome of wiring a cluster into the bastion.
type bastionReadyMsg struct {
	host    string
	context string
	log     string
	// standalone is set when the cluster could not be merged into the operator's
	// existing kubeconfig and was installed as its own file instead. Reporting
	// the wrong one of these two would send the operator to a context that does
	// not exist.
	standalone string
	err        error
}

// parseBastionOutcome reads the markers the merge script prints to say whether
// the cluster landed in the default kubeconfig or in a standalone file.
func parseBastionOutcome(log string) (standalone string) {
	const marker = "AIDT_BASTION_STANDALONE "
	for _, line := range strings.Split(log, "\n") {
		if i := strings.Index(line, marker); i >= 0 {
			return strings.TrimSpace(line[i+len(marker):])
		}
	}
	return ""
}

// microk8sBastionCmd wires a freshly deployed MicroK8s cluster into the Olla
// gateway, so the operator has one host with kubectl and a working kubeconfig
// instead of having to SSH to the cluster node itself.
//
// Returns nil when there is nothing to wire up: no gateway configured, or the
// installer never reported the cluster address.
func (m *model) microk8sBastionCmd(run customRun) tea.Cmd {
	node := hostFromURL(run.url)
	gateway := hostFromURL(m.gateway)
	if node == "" || gateway == "" {
		return nil
	}
	ctx := slugifyName(run.target)
	if ctx == "" {
		ctx = "microk8s"
	}
	// The cluster node is a guest AIDT provisioned, so it uses the deploy
	// credentials; the gateway uses the connection credentials. They differ
	// whenever the MicroK8s image's user is not the gateway's (Ubuntu vs Rocky).
	dcfg := withDeployDefaults(m.deployCfg)
	m.logLines = append(m.logLines,
		fmt.Sprintf(">>> bastion: installing kubectl on %s and adding context %q", gateway, ctx))
	return bastionSetupCmd(bastionTargets{
		node:        node,
		nodeUser:    orDefault(dcfg.VMUser, "rocky"),
		nodePass:    dcfg.VMPassword,
		gateway:     gateway,
		gatewayUser: orDefault(m.sshUser, "rocky"),
		gatewayPass: m.sshPass,
		context:     ctx,
		server:      run.url,
	})
}

// bastionTargets is the two-host input to the bastion flow. The cluster node
// and the gateway can legitimately need different credentials.
type bastionTargets struct {
	node        string
	nodeUser    string
	nodePass    string
	gateway     string
	gatewayUser string
	gatewayPass string
	context     string
	server      string
}

// bastionSetupCmd installs kubectl on the gateway and merges the freshly
// deployed cluster into its kubeconfig.
//
// This is best effort: MicroK8s is already running and registered by the time
// it runs, so a bastion failure is reported but never retracts the deployment.
func bastionSetupCmd(t bastionTargets) tea.Cmd {
	return func() tea.Msg {
		fail := func(err error) tea.Msg {
			return bastionReadyMsg{host: t.gateway, context: t.context, err: err}
		}
		raw, err := fetchMicroK8sKubeconfig(t.node, t.nodeUser, t.nodePass)
		if err != nil {
			return fail(err)
		}
		cfg, err := retargetKubeconfig(raw, t.context, t.server)
		if err != nil {
			return fail(err)
		}
		out, err := installBastionKubeconfig(t.gateway, t.gatewayUser, t.gatewayPass, t.context, cfg)
		return bastionReadyMsg{
			host: t.gateway, context: t.context, log: out,
			standalone: parseBastionOutcome(out), err: err,
		}
	}
}
