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

KUBE_DIR="$HOME/.kube"
mkdir -p "$KUBE_DIR"
INCOMING="$(mktemp)"
MERGED="$(mktemp)"
trap 'rm -f "$INCOMING" "$MERGED"' EXIT
cat > "$INCOMING"
[ -s "$INCOMING" ] || { echo "[bastion] ERROR: empty kubeconfig on stdin" >&2; exit 1; }

` + kubectlInstallFragment + `

if [ -s "$KUBE_DIR/config" ]; then
  # --flatten inlines the certs so the merged file stands alone. Merging keeps
  # any clusters the operator already had.
  KUBECONFIG="$KUBE_DIR/config:$INCOMING" kubectl config view --flatten > "$MERGED"
  [ -s "$MERGED" ] || { echo "[bastion] ERROR: merge produced an empty kubeconfig" >&2; exit 1; }
  cp "$MERGED" "$KUBE_DIR/config"
else
  cp "$INCOMING" "$KUBE_DIR/config"
fi
chmod 600 "$KUBE_DIR/config"

kubectl config use-context "$CTX" >/dev/null
echo "[bastion] context '$CTX' is active in $KUBE_DIR/config"
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
	err     error
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
		return bastionReadyMsg{host: t.gateway, context: t.context, log: out, err: err}
	}
}
