package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"
)

// The K8S section lists the clusters the gateway can reach and is the source of
// truth for what App Deploy can target.
//
// The listing is derived live from the gateway's kubeconfig rather than kept in
// tui.json. A cached copy would drift the moment anyone ran kubectl by hand on
// the gateway, and this list decides where workloads are installed — a stale
// entry here means deploying into the wrong cluster.
//
// Credentials never reach the TUI: the contexts are read through
// `kubectl config view`, which redacts certificates and tokens but keeps the
// names and server URLs this section displays.

// k8sContext is one entry from the gateway's kubeconfig.
type k8sContext struct {
	Name      string
	Cluster   string
	User      string
	Server    string
	Namespace string
	Current   bool
	// Apps is the number of recorded installations pointing at this context,
	// filled in by refreshK8sList.
	Apps int
}

// helmInstallFragment installs Helm when it is missing, mirroring
// kubectlInstallFragment: the official binary with its published checksum
// verified, so this works on both the Rocky guests AIDT provisions and Ubuntu,
// and falls back to ~/.local/bin when there is no sudo.
const helmInstallFragment = `if command -v helm >/dev/null 2>&1; then
  echo "[k8s] helm already installed: $(command -v helm)"
else
  echo "[k8s] installing helm…"
  case "$(uname -m)" in
    x86_64|amd64)  HARCH=amd64 ;;
    aarch64|arm64) HARCH=arm64 ;;
    *) echo "[k8s] ERROR: unsupported architecture $(uname -m)" >&2; exit 1 ;;
  esac
  HVER="$(curl -fsSL --max-time 30 https://get.helm.sh/helm-latest-version | tr -d '[:space:]')"
  [ -n "$HVER" ] || { echo "[k8s] ERROR: could not resolve the helm version" >&2; exit 1; }
  HTMP="$(mktemp -d)"
  HTGZ="helm-$HVER-linux-$HARCH.tar.gz"
  curl -fsSL --max-time 300 -o "$HTMP/$HTGZ" "https://get.helm.sh/$HTGZ"
  curl -fsSL --max-time 60 -o "$HTMP/$HTGZ.sha256sum" "https://get.helm.sh/$HTGZ.sha256sum"
  # The published file is "<sha>  <name>"; check it from inside the temp dir so
  # the recorded filename resolves.
  ( cd "$HTMP" && sha256sum -c "$HTGZ.sha256sum" >/dev/null ) ||
    { rm -rf "$HTMP"; echo "[k8s] ERROR: helm checksum mismatch" >&2; exit 1; }
  tar -xzf "$HTMP/$HTGZ" -C "$HTMP"
  if [ "$(id -u)" -eq 0 ]; then
    install -m 0755 "$HTMP/linux-$HARCH/helm" /usr/local/bin/helm
  elif sudo -n true 2>/dev/null; then
    sudo -n install -m 0755 "$HTMP/linux-$HARCH/helm" /usr/local/bin/helm
  else
    mkdir -p "$HOME/.local/bin"
    install -m 0755 "$HTMP/linux-$HARCH/helm" "$HOME/.local/bin/helm"
    export PATH="$HOME/.local/bin:$PATH"
  fi
  rm -rf "$HTMP"
  command -v helm >/dev/null 2>&1 || { echo "[k8s] ERROR: helm not on PATH after install" >&2; exit 1; }
  echo "[k8s] helm ready: $(command -v helm)"
fi
`

// k8sToolPrefix is the preamble every cluster script shares: a predictable PATH
// and a kubectl that is definitely present.
const k8sToolPrefix = `set -euo pipefail
export PATH="/usr/local/bin:$HOME/.local/bin:$PATH"
` + kubectlInstallFragment

// ---- reading the gateway's kubeconfig ---------------------------------------

// parseKubeContexts reads `kubectl config view` YAML into the section's rows.
//
// It tolerates a partially broken config: an entry whose cluster is missing
// still lists, with an empty server, because hiding it would make a context the
// operator can see in kubectl invisible here.
func parseKubeContexts(raw string) ([]k8sContext, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("empty kubeconfig")
	}
	var doc struct {
		CurrentContext string `yaml:"current-context"`
		Clusters       []struct {
			Name    string `yaml:"name"`
			Cluster struct {
				Server string `yaml:"server"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
		Contexts []struct {
			Name    string `yaml:"name"`
			Context struct {
				Cluster   string `yaml:"cluster"`
				User      string `yaml:"user"`
				Namespace string `yaml:"namespace"`
			} `yaml:"context"`
		} `yaml:"contexts"`
	}
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}

	server := map[string]string{}
	for _, c := range doc.Clusters {
		server[c.Name] = c.Cluster.Server
	}
	out := make([]k8sContext, 0, len(doc.Contexts))
	for _, c := range doc.Contexts {
		out = append(out, k8sContext{
			Name:      c.Name,
			Cluster:   c.Context.Cluster,
			User:      c.Context.User,
			Namespace: c.Context.Namespace,
			Server:    server[c.Context.Cluster],
			Current:   c.Name == doc.CurrentContext,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// k8sContextsMsg carries the refreshed cluster list.
type k8sContextsMsg struct {
	contexts []k8sContext
	err      error
}

// k8sContextsCmd reads the contexts from the gateway.
//
// `config view` is used rather than `--raw` on purpose: this only needs names
// and server URLs, and the redacted form keeps cluster credentials off the wire
// and out of the TUI's memory.
func k8sContextsCmd(host, user, pass string) tea.Cmd {
	return func() tea.Msg {
		client, err := dialSSH(host, user, pass)
		if err != nil {
			return k8sContextsMsg{err: fmt.Errorf("connect to %s: %w", host, err)}
		}
		defer client.Close()
		out, err := runSSH(client, `export PATH="/usr/local/bin:$HOME/.local/bin:$PATH"; kubectl config view -o yaml 2>/dev/null`)
		if err != nil {
			// A gateway with no kubeconfig yet is a normal empty state, not an
			// error worth showing as a failure.
			if strings.Contains(err.Error(), "command not found") {
				return k8sContextsMsg{err: errors.New("kubectl is not installed on the gateway yet — add a cluster to set it up")}
			}
			return k8sContextsMsg{err: fmt.Errorf("read kubeconfig from %s: %w", host, err)}
		}
		ctxs, err := parseKubeContexts(out)
		if err != nil {
			return k8sContextsMsg{err: err}
		}
		return k8sContextsMsg{contexts: ctxs}
	}
}

// ---- mutating the gateway's kubeconfig --------------------------------------

// deleteContextScript removes one context, plus its cluster and user when no
// other context still references them.
//
// The reference check matters: several AIDT clusters can share a user or, after
// a hand-edited config, a cluster. Deleting those unconditionally would quietly
// break a context the operator did not ask to touch.
func deleteContextScript(ctx k8sContext, all []k8sContext) string {
	clusterUsed, userUsed := false, false
	for _, c := range all {
		if c.Name == ctx.Name {
			continue
		}
		if c.Cluster != "" && c.Cluster == ctx.Cluster {
			clusterUsed = true
		}
		if c.User != "" && c.User == ctx.User {
			userUsed = true
		}
	}

	var b strings.Builder
	b.WriteString(`set -euo pipefail
export PATH="/usr/local/bin:$HOME/.local/bin:$PATH"
unset KUBECONFIG
`)
	// Keep a backup for the same reason the bastion merge does: this rewrites a
	// file the operator may have curated by hand.
	b.WriteString(`if [ -s "$HOME/.kube/config" ]; then cp "$HOME/.kube/config" "$HOME/.kube/config.aidt-bak"; chmod 600 "$HOME/.kube/config.aidt-bak"; fi
`)
	b.WriteString("kubectl config delete-context '" + shSingle(ctx.Name) + "'\n")
	if ctx.Cluster != "" && !clusterUsed {
		b.WriteString("kubectl config delete-cluster '" + shSingle(ctx.Cluster) + "' || true\n")
	} else if ctx.Cluster != "" {
		b.WriteString("echo \"[k8s] cluster '" + shSingle(ctx.Cluster) + "' is still used by another context; left in place\"\n")
	}
	if ctx.User != "" && !userUsed {
		b.WriteString("kubectl config delete-user '" + shSingle(ctx.User) + "' || true\n")
	} else if ctx.User != "" {
		b.WriteString("echo \"[k8s] user '" + shSingle(ctx.User) + "' is still used by another context; left in place\"\n")
	}
	// The standalone copy the bastion flow leaves behind would otherwise be an
	// orphaned credential file for a cluster we just stopped tracking.
	b.WriteString("rm -f \"$HOME/.kube/aidt-" + shSingle(ctx.Name) + ".conf\"\n")
	b.WriteString("echo \"[k8s] removed context '" + shSingle(ctx.Name) + "' from $HOME/.kube/config\"\n")
	return b.String()
}

// useContextScript switches the gateway's active context.
func useContextScript(name string) string {
	return `set -euo pipefail
export PATH="/usr/local/bin:$HOME/.local/bin:$PATH"
unset KUBECONFIG
kubectl config use-context '` + shSingle(name) + `'
echo "[k8s] active context is now '` + shSingle(name) + `'"
`
}

// importKubeconfigScript merges a kubeconfig file that already exists on the
// gateway into ~/.kube/config, reusing the bastion merge so the backup,
// fallback, and verification behaviour are identical.
func importKubeconfigScript(path, ctx string) string {
	return k8sToolPrefix + `
SRC='` + shSingle(path) + `'
[ -s "$SRC" ] || { echo "[k8s] ERROR: $SRC does not exist or is empty" >&2; exit 1; }
bash ` + shSingle(bastionScriptPath) + ` '` + shSingle(ctx) + `' < "$SRC"
`
}

// bastionScriptPath is where the merge helper is staged on the gateway.
const bastionScriptPath = "$HOME/.aidt-bastion.sh"

// ---- deploying an app -------------------------------------------------------

// repoSlug turns a chart repository URL into a stable `helm repo add` name.
// Deriving it from the URL rather than the app name means two apps served by
// one repository add it once and agree on what it is called.
func repoSlug(repo string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(repo, "https://"), "http://")
	return orDefault(slugifyName(s), "aidt-repo")
}

// appDeployScript builds the install for one app on one cluster.
//
// Helm runs as `upgrade --install` so redeploying an app that is already there
// converges instead of failing, which is what makes the d key safe to press
// twice. Manifests are applied rather than created for the same reason.
func appDeployScript(a k8sApp, d appDeployment, secrets map[string]string) (string, error) {
	ns, ctx := strings.TrimSpace(d.Namespace), strings.TrimSpace(d.Context)
	if ns == "" || ctx == "" {
		return "", errors.New("context and namespace are required")
	}
	q := func(s string) string { return "'" + shSingle(s) + "'" }

	var b strings.Builder
	b.WriteString(k8sToolPrefix)

	// Record the node ports already assigned before helm runs. Helm reclaims
	// .spec.type on upgrade, which drops the allocation, and the re-publish
	// below would otherwise be handed a fresh random port on every deploy —
	// changing the app's URL each time and invalidating anything configured
	// with it (Paperclip's publicURL rejects requests on an address it does
	// not expect, so the app would break itself on redeploy).
	if a.exposeMode() == exposeNodePort {
		b.WriteString(capturePortsFragment(ctx, ns, d.Release))
	}

	// Generated secrets travel in a values file rather than on the command
	// line. The script itself reaches the host over stdin, but a `--set` would
	// put the secret into helm's own argv, where any other user on the box can
	// read it out of ps — the same reason bastion.go never passes a kubeconfig
	// as an argument.
	valsFile := ""
	if len(secrets) > 0 {
		if a.kind() != appKindHelm {
			return "", errors.New("generated secrets need a Helm chart; a manifest takes no values")
		}
		doc, err := valuesYAML(secrets)
		if err != nil {
			return "", err
		}
		// The document is hex and YAML only, so it cannot contain the heredoc
		// delimiter; the quoted delimiter also stops the shell expanding it.
		valsFile = "$AIDT_VALS"
		b.WriteString(`
umask 077
AIDT_VALS=$(mktemp /tmp/aidt-values-XXXXXX.yaml)
trap 'rm -f "$AIDT_VALS"' EXIT
cat > "$AIDT_VALS" <<'AIDT_VALUES_EOF'
` + doc + `AIDT_VALUES_EOF
`)
	}

	switch a.kind() {
	case appKindHelm:
		b.WriteString(helmInstallFragment)
		// Publishing a Service with `kubectl patch` makes kubectl the field
		// manager for .spec.type, and Helm 4's server-side apply then refuses
		// the next upgrade with "conflict with kubectl-patch using v1:
		// .spec.type" — every redeploy of a published app would fail. Let helm
		// take ownership back; the publish step re-applies afterwards either
		// way. The flag is Helm 4 only, so it is added only when supported.
		b.WriteString(`AIDT_HELM_FORCE=""
if helm upgrade --help 2>/dev/null | grep -q -- '--force-conflicts'; then
  AIDT_HELM_FORCE="--force-conflicts"
fi
`)
		chart := a.Chart
		if !a.isOCI() {
			if a.Repo == "" {
				return "", errors.New("a chart repository is required for non-OCI charts")
			}
			slug := repoSlug(a.Repo)
			b.WriteString("helm repo add " + q(slug) + " " + q(a.Repo) + " --force-update\n")
			b.WriteString("helm repo update " + q(slug) + "\n")
			chart = slug + "/" + a.Chart
		}
		args := []string{
			"helm", "upgrade", "--install", q(d.Release), q(chart),
			"--kube-context", q(ctx), "--namespace", q(ns), "--create-namespace",
			"--wait", "--timeout", "10m", "$AIDT_HELM_FORCE",
		}
		if v := strings.TrimSpace(a.Version); v != "" {
			args = append(args, "--version", q(v))
		}
		// Generated values go first so an explicit entry in the app's Values can
		// still override one.
		if valsFile != "" {
			args = append(args, "-f", `"`+valsFile+`"`)
		}
		for i, s := range a.valuesArgs() {
			// valuesArgs alternates "--set", "k=v"; only the value needs quoting.
			if i%2 == 0 {
				args = append(args, s)
			} else {
				args = append(args, q(s))
			}
		}
		b.WriteString(strings.Join(args, " ") + "\n")
		b.WriteString("helm status " + q(d.Release) + " --kube-context " + q(ctx) + " --namespace " + q(ns) + " | head -20\n")

	case appKindManifest:
		b.WriteString("kubectl --context " + q(ctx) + " create namespace " + q(ns) +
			" --dry-run=client -o yaml | kubectl --context " + q(ctx) + " apply -f -\n")
		b.WriteString("kubectl --context " + q(ctx) + " --namespace " + q(ns) +
			" apply -f " + q(a.ManifestURL) + "\n")

	default:
		return "", errors.New("app has neither a chart nor a manifest URL")
	}

	if a.exposeMode() == exposeNodePort {
		b.WriteString(exposeFragment(ctx, ns, d.Release))
	}
	b.WriteString("echo " + q("AIDT_APP_DEPLOYED "+a.Name) + "\n")
	b.WriteString("kubectl --context " + q(ctx) + " --namespace " + q(ns) + " get all 2>&1 | head -20 || true\n")
	return b.String(), nil
}

// exposeFragment publishes an application's primary Service on a NodePort so it
// is reachable from outside the cluster and shows up with a URL in App
// Services. Charts overwhelmingly default to ClusterIP, which leaves a
// perfectly healthy app with nowhere to browse to.
//
// This patches the Service after the install rather than passing
// `--set service.type=NodePort`, for two reasons. Value paths are not
// standardised — a chart may use service.type, server.service.type, or no such
// key at all — and a --set against a chart whose schema disagrees fails the
// whole deploy. Patching also works identically for manifest installs, which
// take no values. The patch is re-applied on every deploy, so a helm upgrade
// that resets the type to ClusterIP is corrected immediately after.
//
// Only the primary Service is touched. Exposing everything a release creates
// would publish its dependencies too — an Open WebUI install alone would put
// Redis and an Ollama API on every node address with no authentication.
// capturePortsFragment records the node ports a previous deploy assigned, so
// the re-publish below can ask for the same ones instead of taking whatever
// Kubernetes hands out next. It runs before helm.
func capturePortsFragment(ctx, ns, release string) string {
	q := func(s string) string { return "'" + shSingle(s) + "'" }
	return `
# --- remember the node ports already in use, so the URL is stable ---
AIDT_X_KEEP=$(kubectl --context ` + q(ctx) + ` -n ` + q(ns) + ` get svc ` + q(release) + ` \
  -o jsonpath='{range .spec.ports[*]}{.port}:{.nodePort},{end}' 2>/dev/null || true)
`
}

func exposeFragment(ctx, ns, release string) string {
	q := func(s string) string { return "'" + shSingle(s) + "'" }
	return `
# --- publish the primary service on a NodePort ---
AIDT_X_CTX=` + q(ctx) + `
AIDT_X_NS=` + q(ns) + `
AIDT_X_REL=` + q(release) + `
aidt_expose() {
  local svcs target name type cip
  svcs=$(kubectl --context "$AIDT_X_CTX" -n "$AIDT_X_NS" get svc \
    -o jsonpath='{range .items[*]}{.metadata.name} {.spec.type} {.spec.clusterIP}{"\n"}{end}' 2>/dev/null)
  [ -n "$svcs" ] || { echo "AIDT_EXPOSE_SKIP - no services found"; return 0; }

  # The release's own name is the Helm convention for the primary service. With
  # only one service there is no ambiguity. Anything else is left alone rather
  # than guessed at.
  target=$(printf '%s\n' "$svcs" | awk -v rel="$AIDT_X_REL" '
    $1==rel {print; found=1; exit}
    NF {n++; last=$0}
    END {if (!found && n==1) print last}')
  [ -n "$target" ] || { echo "AIDT_EXPOSE_SKIP - no primary service could be identified"; return 0; }

  set -- $target; name=$1; type=$2; cip=$3
  if [ "$cip" = "None" ]; then
    echo "AIDT_EXPOSE_SKIP $name headless service cannot be published"
    return 0
  fi
  if [ "$type" != "ClusterIP" ]; then
    echo "AIDT_EXPOSE_SKIP $name already $type"
    return 0
  fi

  # Ask for the node ports this service had before helm reset it, so the URL
  # an operator saved (or configured into the app) keeps working. The strategic
  # merge key for Service.spec.ports is "port", so naming each one is enough.
  local keep patch
  keep=$(printf '%s' "${AIDT_X_KEEP:-}" | awk -F, '{
    for (i = 1; i <= NF; i++) {
      if ($i == "") continue
      split($i, a, ":")
      if (a[2] != "" && a[2] != "0") printf "%s{\"port\":%s,\"nodePort\":%s}", (n++ ? "," : ""), a[1], a[2]
    }
  }')
  if [ -n "$keep" ]; then
    patch="{\"spec\":{\"type\":\"NodePort\",\"ports\":[$keep]}}"
  else
    patch='{"spec":{"type":"NodePort"}}'
  fi

  if kubectl --context "$AIDT_X_CTX" -n "$AIDT_X_NS" patch svc "$name" -p "$patch" >/dev/null 2>&1; then
    echo "AIDT_EXPOSED $name"
  elif kubectl --context "$AIDT_X_CTX" -n "$AIDT_X_NS" patch svc "$name" \
        -p '{"spec":{"type":"NodePort"}}' >/dev/null 2>&1; then
    # The remembered port can be taken by something else in the meantime;
    # publishing on a fresh port beats not publishing at all.
    echo "AIDT_EXPOSED $name (new port)"
  else
    echo "AIDT_EXPOSE_SKIP $name patch to NodePort failed"
  fi
  return 0
}
aidt_expose
`
}

// appRemoveScript builds the uninstall for one recorded installation.
//
// The kind comes from the registry entry, not the current catalog definition:
// an app edited from a manifest to a chart after it was installed must still be
// removed the way it was created.
func appRemoveScript(a k8sApp, d appDeployment) (string, error) {
	ns, ctx := strings.TrimSpace(d.Namespace), strings.TrimSpace(d.Context)
	if ns == "" || ctx == "" {
		return "", errors.New("context and namespace are required")
	}
	q := func(s string) string { return "'" + shSingle(s) + "'" }

	var b strings.Builder
	b.WriteString(k8sToolPrefix)

	kind := orDefault(d.Kind, a.kind())
	switch kind {
	case appKindHelm:
		b.WriteString(helmInstallFragment)
		b.WriteString("helm uninstall " + q(d.Release) +
			" --kube-context " + q(ctx) + " --namespace " + q(ns) + " --wait --timeout 5m\n")
	case appKindManifest:
		if a.ManifestURL == "" {
			return "", errors.New("the manifest URL is no longer known, so this cannot be deleted automatically")
		}
		b.WriteString("kubectl --context " + q(ctx) + " --namespace " + q(ns) +
			" delete -f " + q(a.ManifestURL) + " --ignore-not-found\n")
	default:
		return "", errors.New("unknown deployment kind")
	}
	b.WriteString("echo " + q("AIDT_APP_REMOVED "+d.App) + "\n")
	return b.String(), nil
}

// ---- reconciling the registry against the clusters --------------------------

// appReconcileMsg reports which recorded installations are still present.
type appReconcileMsg struct {
	missing map[string]bool // appDeployment.label() -> absent from its cluster
	err     error
}

// appReconcileScript asks the gateway which recorded installations still exist.
//
// One script covering every entry keeps this to a single SSH round trip, and
// each line is prefixed so a partial failure still yields usable answers.
func appReconcileScript(ds []appDeployment) string {
	var b strings.Builder
	b.WriteString(`set -uo pipefail
export PATH="/usr/local/bin:$HOME/.local/bin:$PATH"
`)
	for _, d := range ds {
		q := func(s string) string { return "'" + shSingle(s) + "'" }
		label := q(d.label())
		switch d.Kind {
		case appKindHelm:
			b.WriteString("if helm status " + q(d.Release) + " --kube-context " + q(d.Context) +
				" --namespace " + q(d.Namespace) + " >/dev/null 2>&1; then " +
				"echo \"AIDT_APP_OK \"" + label + "; else echo \"AIDT_APP_GONE \"" + label + "; fi\n")
		default:
			// A manifest install has no release to query; presence of the
			// namespace with any workload in it is the closest cheap signal.
			b.WriteString("if kubectl --context " + q(d.Context) + " --namespace " + q(d.Namespace) +
				" get all >/dev/null 2>&1; then echo \"AIDT_APP_OK \"" + label +
				"; else echo \"AIDT_APP_GONE \"" + label + "; fi\n")
		}
	}
	return b.String()
}

// parseAppReconcile reads the markers back into a missing-set.
func parseAppReconcile(out string) map[string]bool {
	missing := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "AIDT_APP_GONE "):
			missing[strings.TrimSpace(strings.TrimPrefix(line, "AIDT_APP_GONE "))] = true
		case strings.HasPrefix(line, "AIDT_APP_OK "):
			missing[strings.TrimSpace(strings.TrimPrefix(line, "AIDT_APP_OK "))] = false
		}
	}
	return missing
}

// ---- section wiring ---------------------------------------------------------

// refreshK8sList rebuilds the K8S section from the last kubeconfig read,
// annotating each cluster with how many recorded installs point at it.
func (m *model) refreshK8sList() {
	byCtx := map[string]int{}
	for _, d := range m.appDeploys {
		byCtx[d.Context]++
	}
	items := []list.Item{k8sItem{add: true}}
	for _, c := range m.k8sContexts {
		items = append(items, k8sItem{
			name: c.Name, cluster: c.Cluster, server: c.Server,
			apps: byCtx[c.Name], current: c.Current,
		})
	}
	m.k8sList.SetItems(items)
}

func (m *model) selectedK8sItem() (k8sItem, bool) {
	i, ok := m.k8sList.SelectedItem().(k8sItem)
	return i, ok
}

// k8sContextNames lists the contexts the deploy form can target.
func (m *model) k8sContextNames() []string {
	out := make([]string, 0, len(m.k8sContexts))
	for _, c := range m.k8sContexts {
		out = append(out, c.Name)
	}
	return out
}

// refreshK8sCmd re-reads the gateway's kubeconfig.
func (m *model) refreshK8sCmd() tea.Cmd {
	host := hostFromURL(m.gateway)
	if host == "" {
		m.k8sErr = "no gateway configured — connect one first (c on the sidebar)"
		return nil
	}
	if m.sshPass == "" && managedKeyPath() == "" {
		m.k8sErr = "no gateway credentials — reconnect with c on the sidebar"
		return nil
	}
	m.k8sLoading = true
	m.k8sErr = ""
	return k8sContextsCmd(host, orDefault(m.sshUser, "rocky"), m.sshPass)
}

// runK8sScript executes a kubeconfig mutation on the gateway through the shared
// runner, so its output lands in the same Output pane as everything else.
func (m *model) runK8sScript(script, title string) tea.Cmd {
	host := hostFromURL(m.gateway)
	if host == "" {
		m.notice = "no gateway configured"
		return nil
	}
	if m.procBusy || m.appBusy {
		m.notice = "a deploy/remove is already running"
		return nil
	}
	m.procBusy = true
	m.section = secK8s
	m.logLines = append(m.logLines, ">>> "+title)
	m.renderLog()
	m.procCh = make(chan ProcEvent, 128)
	go RunUpdatePlan([]updateStep{{
		title:  title,
		host:   host,
		user:   orDefault(m.sshUser, "rocky"),
		pass:   m.sshPass,
		script: script,
	}}, m.procCh)
	m.notice = title + " — see Output"
	return waitProc(m.procCh)
}

// removeSelectedK8s deletes the highlighted cluster from the gateway's
// kubeconfig and forgets any app installs that pointed at it.
//
// This removes AIDT's way of reaching the cluster; it does not touch the
// cluster itself, and any workload deployed there keeps running. The notice
// says so, because "remove" next to a list of clusters could reasonably be read
// the other way.
func (m *model) removeSelectedK8s() tea.Cmd {
	it, ok := m.selectedK8sItem()
	if !ok || it.add {
		m.notice = "select a cluster first"
		return nil
	}
	var ctx k8sContext
	for _, c := range m.k8sContexts {
		if c.Name == it.name {
			ctx = c
			break
		}
	}
	if ctx.Name == "" {
		m.notice = "cluster not found — press r to refresh"
		return nil
	}
	if dropped := m.forgetAppsForContext(ctx.Name); len(dropped) > 0 {
		m.logLines = append(m.logLines,
			fmt.Sprintf(">>> forgetting %d app install(s) on %s: %s",
				len(dropped), ctx.Name, strings.Join(dropped, ", ")))
	}
	return m.runK8sScript(deleteContextScript(ctx, m.k8sContexts),
		"remove cluster "+ctx.Name+" from the gateway kubeconfig")
}

// useSelectedK8s makes the highlighted cluster the gateway's active context.
func (m *model) useSelectedK8s() tea.Cmd {
	it, ok := m.selectedK8sItem()
	if !ok {
		return nil
	}
	if it.add {
		return m.openK8sAdd()
	}
	return m.runK8sScript(useContextScript(it.name), "set active context to "+it.name)
}

// appReconcileCmd checks every recorded installation in one pass.
func appReconcileCmd(host, user, pass string, ds []appDeployment) tea.Cmd {
	return func() tea.Msg {
		if len(ds) == 0 {
			return appReconcileMsg{missing: map[string]bool{}}
		}
		client, err := dialSSH(host, user, pass)
		if err != nil {
			return appReconcileMsg{err: fmt.Errorf("connect to %s: %w", host, err)}
		}
		defer client.Close()
		// A non-zero exit is expected here — the script reports per-entry
		// results and the last check may legitimately fail — so the output is
		// parsed regardless.
		out, _ := runSSH(client, appReconcileScript(ds))
		return appReconcileMsg{missing: parseAppReconcile(out)}
	}
}
