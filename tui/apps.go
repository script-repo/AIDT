package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// The App Deploy section installs workloads onto the Kubernetes clusters listed
// in the K8S section. It is the cluster-native counterpart to the custom
// deployments in the Nutanix section: those provision a VM and run a setup
// script, these install a chart or manifest into an existing cluster.
//
// Everything runs on the Olla gateway, not on the machine running the TUI. The
// gateway is already the operator's bastion (see bastion.go): kubectl lives
// there and every cluster AIDT knows about is merged into its kubeconfig. That
// keeps credentials on one host and means the TUI needs no local kubectl.

// k8sApp is a deployable workload: either a Helm chart or a plain manifest URL.
//
// The two forms exist because real catalogs are split between them, and the
// distinction is not cosmetic — it decides how the workload is removed again.
// A Helm release tracks its own resources and uninstalls cleanly; a manifest
// can only be deleted by re-applying the same URL in reverse, which is why the
// deployment registry records which one produced it.
type k8sApp struct {
	Name string `json:"name"`
	Desc string `json:"desc,omitempty"`

	// Helm source. Repo is empty for OCI charts, where Chart carries the full
	// oci:// reference and no `helm repo add` is needed.
	Repo    string `json:"repo,omitempty"`
	Chart   string `json:"chart,omitempty"`
	Version string `json:"version,omitempty"`
	// Values are extra `--set key=value` pairs, comma-separated, applied in order.
	Values string `json:"values,omitempty"`

	// ManifestURL is set instead of Repo/Chart for `kubectl apply -f` workloads.
	ManifestURL string `json:"manifest_url,omitempty"`

	// Namespace is the suggested default; the deploy form can override it.
	Namespace string `json:"namespace,omitempty"`

	// Expose controls whether the primary Service is published on a NodePort
	// after deploying, so the app is reachable from App Services. Empty means
	// the default (publish); "none" opts out.
	//
	// Opting out matters for charts with no user-facing endpoint — an operator's
	// webhook or metrics Service is not something to put on every node address.
	Expose string `json:"expose,omitempty"`
}

// expose modes.
const (
	exposeNodePort = "nodeport"
	exposeNone     = "none"
)

// exposeMode reports how this app is published after a deploy. Publishing is
// the default because a chart's stock ClusterIP leaves a healthy install with
// no address an operator can open.
func (a k8sApp) exposeMode() string {
	if strings.EqualFold(strings.TrimSpace(a.Expose), exposeNone) {
		return exposeNone
	}
	return exposeNodePort
}

// app kinds, recorded on each deployment so removal picks the right command.
const (
	appKindHelm     = "helm"
	appKindManifest = "manifest"
)

// kind reports how this app is installed. A manifest URL wins only when no
// chart is configured, so a half-filled definition still deploys predictably.
func (a k8sApp) kind() string {
	if a.Chart != "" {
		return appKindHelm
	}
	if a.ManifestURL != "" {
		return appKindManifest
	}
	return ""
}

// isOCI reports whether the chart is pulled straight from an OCI registry,
// which needs no repo to be added first.
func (a k8sApp) isOCI() bool { return strings.HasPrefix(a.Chart, "oci://") }

// sourceKey identifies an app definition for the seed ledger and for dedupe.
// The source is used rather than the name so renaming a built-in locally does
// not cause the next launch to re-add it.
func (a k8sApp) sourceKey() string {
	if a.kind() == appKindManifest {
		return a.ManifestURL
	}
	if a.isOCI() || a.Repo == "" {
		return a.Chart
	}
	return strings.TrimSuffix(a.Repo, "/") + "|" + a.Chart
}

// valuesArgs splits the comma-separated Values into `--set` arguments. Empty
// segments are skipped so a trailing comma is harmless.
func (a k8sApp) valuesArgs() []string {
	var out []string
	for _, v := range strings.Split(a.Values, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, "--set", v)
		}
	}
	return out
}

// defaultNamespace is the namespace pre-filled in the deploy form.
func (a k8sApp) defaultNamespace() string {
	return orDefault(strings.TrimSpace(a.Namespace), "default")
}

// appDeployment records one installation of an app onto one cluster.
//
// The registry is a slice rather than a map because a single app is expected to
// be installed several times — the whole point of the K8S section is that the
// same workload can land on several clusters — and the identity of an install
// is the (app, context, namespace, release) tuple, not the app alone.
type appDeployment struct {
	App       string `json:"app"`
	Context   string `json:"context"`
	Namespace string `json:"namespace"`
	Release   string `json:"release"`
	// Kind is captured at deploy time. The app definition can be edited or
	// deleted afterwards, and an uninstall still has to know what it is undoing.
	Kind string `json:"kind"`
	// Missing is set by a reconcile that could not find the workload in the
	// cluster. The entry is kept rather than dropped so the operator can see
	// that something they deployed has gone away.
	Missing bool `json:"missing,omitempty"`
}

// same reports whether two registry entries describe the same installation.
func (d appDeployment) same(o appDeployment) bool {
	return d.App == o.App && d.Context == o.Context &&
		d.Namespace == o.Namespace && d.Release == o.Release
}

// label renders an install target for pickers and notices.
func (d appDeployment) label() string {
	return fmt.Sprintf("%s · %s/%s", d.Context, d.Namespace, d.Release)
}

// ---- built-in catalog -------------------------------------------------------

// builtinApps is the AIDT-provided starter catalog.
//
// Every http chart repository here was resolved against its live index.yaml
// when this list was written, and no chart carries a pinned Version: the
// clusters AIDT deploys are new, so tracking the current chart is more useful
// than freezing a version that will be stale within weeks. Pin one per app in
// the edit form when a deployment needs to be reproducible.
//
// Bitnami is deliberately absent. Broadcom deleted the public Bitnami image
// catalog in 2025, so those charts install and then fail to pull; PostgreSQL is
// served by CloudNativePG and Redis by the ot-container-kit operator instead.
func builtinApps() []k8sApp {
	return []k8sApp{
		// --- AI serving stack ---
		{
			Name: "Open WebUI", Desc: "chat UI for the Olla pool",
			Repo: "https://helm.openwebui.com", Chart: "open-webui", Namespace: "ai",
		},
		{
			Name: "LiteLLM", Desc: "OpenAI-compatible proxy / router",
			Chart: "oci://ghcr.io/berriai/litellm-helm", Namespace: "ai",
		},
		{
			Name: "Langfuse", Desc: "LLM tracing & evaluation",
			Repo: "https://langfuse.github.io/langfuse-k8s", Chart: "langfuse", Namespace: "ai",
		},
		{
			Name: "AnythingLLM", Desc: "private document chat & RAG",
			Repo: "https://mintplex-labs.github.io/helm-charts", Chart: "anythingllm", Namespace: "ai",
		},
		{
			Name: "LangGraph", Desc: "agent runtime (LangGraph Platform)",
			Repo: "https://langchain-ai.github.io/helm", Chart: "langgraph-cloud", Namespace: "ai",
		},
		{
			Name: "Paperclip", Desc: "AI agent orchestration platform",
			Repo: "https://ileonelperea.github.io/paperclip-helm", Chart: "paperclip", Namespace: "ai",
		},

		// --- data services ---
		// The two operators are not published: they expose webhook and metrics
		// endpoints rather than anything an operator would browse to, and the
		// databases they manage should not be reachable from every node address.
		{
			Name: "CloudNativePG", Desc: "PostgreSQL operator",
			Repo: "https://cloudnative-pg.github.io/charts", Chart: "cloudnative-pg", Namespace: "data",
			Expose: exposeNone,
		},
		{
			Name: "Redis", Desc: "Redis operator (ot-container-kit)",
			Repo: "https://ot-container-kit.github.io/helm-charts", Chart: "redis", Namespace: "data",
			Expose: exposeNone,
		},
		{
			Name: "Qdrant", Desc: "vector database",
			Repo: "https://qdrant.github.io/qdrant-helm", Chart: "qdrant", Namespace: "data",
		},

		// --- ops / automation ---
		{
			Name: "Grafana", Desc: "dashboards & metrics",
			Repo: "https://grafana.github.io/helm-charts", Chart: "grafana", Namespace: "ops",
		},
		{
			Name: "n8n", Desc: "workflow automation",
			Chart: "oci://8gears.container-registry.com/library/n8n", Namespace: "ops",
		},
	}
}

// seedBuiltinApps appends any built-in app this install has never been offered
// and returns the updated ledger.
//
// This mirrors seedBuiltinCustomDeploys: recording each built-in separately is
// what lets a catalog addition in a later AIDT release reach existing configs
// exactly once, while still letting a delete stick.
func seedBuiltinApps(in []k8sApp, ledger []string, legacySeeded bool) ([]k8sApp, []string, bool) {
	seen := map[string]bool{}
	for _, s := range ledger {
		seen[s] = true
	}
	// Anything already in the list counts as offered, however it got there.
	for _, a := range in {
		seen[a.sourceKey()] = true
	}
	// A config written before the ledger existed has already seen the original
	// catalog; treat it as offered so deletes are not undone on upgrade.
	if legacySeeded && len(ledger) == 0 {
		for _, b := range builtinApps() {
			seen[b.sourceKey()] = true
		}
		return in, ledger, false
	}

	out := append([]k8sApp(nil), in...)
	changed := false
	for _, b := range builtinApps() {
		if seen[b.sourceKey()] {
			continue
		}
		out = append(out, b)
		ledger = append(ledger, b.sourceKey())
		seen[b.sourceKey()] = true
		changed = true
	}
	return out, ledger, changed
}

// ---- deployment registry ----------------------------------------------------

// appDeploymentsFor returns every recorded installation of one app, in a stable
// order so the list and the removal picker agree.
func (m *model) appDeploymentsFor(name string) []appDeployment {
	var out []appDeployment
	for _, d := range m.appDeploys {
		if d.App == name {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label() < out[j].label() })
	return out
}

// appContextsFor lists the distinct contexts an app is installed on.
func (m *model) appContextsFor(name string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range m.appDeploymentsFor(name) {
		if !seen[d.Context] {
			seen[d.Context] = true
			out = append(out, d.Context)
		}
	}
	return out
}

// recordAppDeployment upserts one installation and persists the registry.
func (m *model) recordAppDeployment(d appDeployment) {
	for i := range m.appDeploys {
		if m.appDeploys[i].same(d) {
			m.appDeploys[i] = d
			_ = saveAppDeploys(m.tokFile, m.appDeploys)
			m.refreshAppsList()
			return
		}
	}
	m.appDeploys = append(m.appDeploys, d)
	_ = saveAppDeploys(m.tokFile, m.appDeploys)
	m.refreshAppsList()
}

// forgetAppDeployment drops one installation from the registry. This is the
// bookkeeping half of a removal; the uninstall itself runs on the cluster.
func (m *model) forgetAppDeployment(d appDeployment) {
	kept := make([]appDeployment, 0, len(m.appDeploys))
	for _, e := range m.appDeploys {
		if e.same(d) {
			continue
		}
		kept = append(kept, e)
	}
	m.appDeploys = kept
	_ = saveAppDeploys(m.tokFile, m.appDeploys)
	m.refreshAppsList()
}

// forgetAppsForContext drops every registry entry pointing at a context that no
// longer exists, so removing a cluster does not leave apps looking deployed.
// Returns what it dropped, for the notice.
func (m *model) forgetAppsForContext(ctx string) []string {
	kept := make([]appDeployment, 0, len(m.appDeploys))
	var dropped []string
	for _, d := range m.appDeploys {
		if d.Context == ctx {
			dropped = append(dropped, d.App+" ("+d.label()+")")
			continue
		}
		kept = append(kept, d)
	}
	if len(dropped) == 0 {
		return nil
	}
	m.appDeploys = kept
	sort.Strings(dropped)
	_ = saveAppDeploys(m.tokFile, m.appDeploys)
	m.refreshAppsList()
	return dropped
}

// appByName looks up a definition in the catalog.
func (m *model) appByName(name string) (k8sApp, bool) {
	for _, a := range m.apps {
		if a.Name == name {
			return a, true
		}
	}
	return k8sApp{}, false
}

// deleteApp removes a catalog definition. Recorded installations are left
// alone: the workloads are still running, and dropping the registry would
// strand them with no way to uninstall from the TUI.
func (m *model) deleteApp(name string) {
	kept := make([]k8sApp, 0, len(m.apps))
	for _, a := range m.apps {
		if a.Name == name {
			continue
		}
		kept = append(kept, a)
	}
	m.apps = kept
	_ = saveApps(m.tokFile, m.apps)
	m.refreshAppsList()
}

// ---- list ------------------------------------------------------------------

// refreshAppsList rebuilds the section: the "add application" row first, then
// one row per catalog entry carrying its deployment state.
func (m *model) refreshAppsList() {
	items := []list.Item{appItem{add: true}}
	for _, a := range m.apps {
		ds := m.appDeploymentsFor(a.Name)
		missing := 0
		for _, d := range ds {
			if d.Missing {
				missing++
			}
		}
		src := a.Chart
		if a.kind() == appKindManifest {
			src = a.ManifestURL
		}
		items = append(items, appItem{
			name: a.Name, desc: a.Desc, kind: a.kind(), source: src,
			contexts: m.appContextsFor(a.Name),
			count:    len(ds), missing: missing,
		})
	}
	m.appsList.SetItems(items)
}

func (m *model) selectedAppItem() (appItem, bool) {
	i, ok := m.appsList.SelectedItem().(appItem)
	return i, ok
}

// ---- deploy / remove --------------------------------------------------------

// appPreflight returns the gateway address to run cluster commands on, or an
// error explaining what is missing. Both the deploy and remove paths need the
// same three things, so they ask once here.
func (m *model) appPreflight() (string, error) {
	host := hostFromURL(m.gateway)
	if host == "" {
		return "", errors.New("no gateway configured — connect one first (c on the sidebar)")
	}
	if m.sshPass == "" && managedKeyPath() == "" {
		return "", errors.New("no gateway credentials — reconnect with c on the sidebar")
	}
	if m.appBusy || m.procBusy {
		return "", errors.New("a deploy/remove is already running")
	}
	return host, nil
}

// deploySelectedApp acts on the highlighted row: the "add" row opens the
// new-application form, a catalog row opens the deploy form.
func (m *model) deploySelectedApp() tea.Cmd {
	it, ok := m.selectedAppItem()
	if !ok {
		return nil
	}
	if it.add {
		return m.openAppAdd()
	}
	if _, err := m.appPreflight(); err != nil {
		m.notice = err.Error()
		return nil
	}
	if len(m.k8sContexts) == 0 {
		m.notice = "no clusters known — add one in K8S first"
		return nil
	}
	a, ok := m.appByName(it.name)
	if !ok {
		m.notice = "application not found"
		return nil
	}
	if a.kind() == "" {
		m.notice = a.Name + " has neither a chart nor a manifest URL — edit it with e"
		return nil
	}
	return m.openAppDeploy(a)
}

// removeSelectedApp opens the picker of this app's recorded installations.
//
// Unlike the Services section's x (which only forgets a listing), this really
// uninstalls the workload — matching the Agents section, where x is also a real
// removal. The picker lists existing installs rather than asking for free text
// so an operator cannot typo their way into deleting the wrong release.
func (m *model) removeSelectedApp() tea.Cmd {
	it, ok := m.selectedAppItem()
	if !ok || it.add {
		m.notice = "select an application first"
		return nil
	}
	if _, err := m.appPreflight(); err != nil {
		m.notice = err.Error()
		return nil
	}
	ds := m.appDeploymentsFor(it.name)
	if len(ds) == 0 {
		m.notice = it.name + " is not deployed anywhere"
		return nil
	}
	return m.openAppRemove(it.name, ds)
}

// startAppRun executes a deploy or remove on the gateway.
//
// It reuses the update-plan runner that the custom deployments already use, so
// output streams into the same Output pane and the same ProcEvent plumbing
// reports completion.
func (m *model) startAppRun(script, title, act string, d appDeployment) tea.Cmd {
	host, err := m.appPreflight()
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	m.appBusy = true
	m.procBusy = true
	m.pendingApp = &d
	m.pendingAppAct = act
	m.section = secApps
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

// startAppDeploy builds and runs the install described by the deploy form.
func (m *model) startAppDeploy(a k8sApp, d appDeployment) tea.Cmd {
	script, err := appDeployScript(a, d)
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	return m.startAppRun(script,
		fmt.Sprintf("deploy %s to %s", a.Name, d.label()), "deploy", d)
}

// startAppRemove builds and runs the uninstall for one recorded installation.
func (m *model) startAppRemove(a k8sApp, d appDeployment) tea.Cmd {
	script, err := appRemoveScript(a, d)
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	return m.startAppRun(script,
		fmt.Sprintf("remove %s from %s", d.App, d.label()), "remove", d)
}

// finishAppRun updates the registry once a deploy or remove has exited.
//
// The registry is only written on success. A failed deploy that had already
// been recorded would show the app as green while nothing is running, which is
// exactly the state this section exists to make visible.
func (m *model) finishAppRun(code int) {
	d, act := m.pendingApp, m.pendingAppAct
	m.appBusy = false
	m.pendingApp, m.pendingAppAct = nil, ""
	if d == nil {
		return
	}
	if code != 0 {
		m.notice = fmt.Sprintf("%s %s failed (exit %d) — see Output", act, d.App, code)
		return
	}
	switch act {
	case "deploy":
		d.Missing = false
		m.recordAppDeployment(*d)
		m.notice = fmt.Sprintf("%s deployed to %s", d.App, d.label())
	case "remove":
		m.forgetAppDeployment(*d)
		m.notice = fmt.Sprintf("%s removed from %s", d.App, d.label())
	}
}

// applyAppReconcile marks registry entries the cluster no longer has.
func (m *model) applyAppReconcile(missing map[string]bool) int {
	gone := 0
	for i := range m.appDeploys {
		if v, ok := missing[m.appDeploys[i].label()]; ok {
			m.appDeploys[i].Missing = v
		}
		if m.appDeploys[i].Missing {
			gone++
		}
	}
	_ = saveAppDeploys(m.tokFile, m.appDeploys)
	m.refreshAppsList()
	return gone
}

// upsertApp saves a catalog definition from the add/edit form.
func (m *model) upsertApp(a k8sApp, replacing string) {
	target := orDefault(replacing, a.Name)
	for i := range m.apps {
		if m.apps[i].Name == target {
			m.apps[i] = a
			_ = saveApps(m.tokFile, m.apps)
			m.refreshAppsList()
			return
		}
	}
	m.apps = append(m.apps, a)
	_ = saveApps(m.tokFile, m.apps)
	m.refreshAppsList()
}
