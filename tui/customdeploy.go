package main

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	np4mInstall = "curl -fsSL https://raw.githubusercontent.com/script-repo/ntnx-np4m/main/install.sh | sudo bash"
	nrccInstall = "curl -fsSL https://raw.githubusercontent.com/script-repo/ntnx-console-client/main/install.sh | NRCC_NO_OPEN=1 bash"
	nrccLegacy  = "curl -fsSL https://raw.githubusercontent.com/script-repo/ntnx-console-client/main/install.sh | bash"
)

// customRun tracks a custom deployment until its setup command completes. A
// service URL is persisted only after a successful terminal ProcEvent.
type customRun struct {
	cfg    customDeploy
	target string
	url    string
}

// customItem is a row in the Nutanix "custom deployments" submenu: either the
// special "add deployment" action (add=true) or one saved deployment type.
type customItem struct {
	name   string
	url    string
	scheme string
	port   string
	path   string
	add    bool
}

func (i customItem) cfg() customDeploy {
	return customDeploy{Name: i.name, ScriptURL: i.url, Scheme: i.scheme, Port: i.port, Path: i.path}
}

func (i customItem) Title() string {
	if i.add {
		return "+ add deployment"
	}
	title := i.name
	if i.port != "" {
		title += "  :" + i.port
	}
	if i.url != "" {
		title += "  →  " + i.url
	}
	return title
}

func (i customItem) Description() string {
	if i.add {
		return "define a new deployment type"
	}
	return i.url
}

func (i customItem) FilterValue() string { return i.Title() }

// accessURL builds the workload link for a deployed VM IP, or "" if no port.
func (c customDeploy) accessURL(ip string) string {
	if c.Port == "" || ip == "" {
		return ""
	}
	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1 || port > 65535 {
		return ""
	}
	scheme := c.Scheme
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return ""
	}
	path := c.Path
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	host := strings.Trim(strings.TrimSpace(ip), "[]")
	return (&url.URL{Scheme: scheme, Host: net.JoinHostPort(host, c.Port), Path: path}).String()
}

// defaultCustomDeploys are the built-in deployment types seeded on first run.
func defaultCustomDeploys() []customDeploy {
	return []customDeploy{
		{Name: "NP4M", ScriptURL: np4mInstall, Scheme: "https", Port: "8443"},
		{Name: "NRCC", ScriptURL: nrccInstall, Scheme: "https", Port: "8443"},
	}
}

// migrateBuiltinCustomDeploys adds service metadata to exact built-in entries
// seeded by older AIDT releases without overwriting user-authored definitions.
func migrateBuiltinCustomDeploys(in []customDeploy) ([]customDeploy, bool) {
	out := append([]customDeploy(nil), in...)
	changed := false
	for i := range out {
		c := &out[i]
		builtin := c.Name == "NP4M" && c.ScriptURL == np4mInstall
		if c.Name == "NRCC" && (c.ScriptURL == nrccLegacy || c.ScriptURL == nrccInstall) {
			builtin = true
			if c.ScriptURL == nrccLegacy {
				c.ScriptURL = nrccInstall
				changed = true
			}
		}
		if !builtin {
			continue
		}
		if c.Scheme == "" {
			c.Scheme = "https"
			changed = true
		}
		if c.Port == "" {
			c.Port = "8443"
			changed = true
		}
	}
	return out, changed
}

// refreshCustomList rebuilds the submenu: the "add deployment" row first, then
// one row per saved custom deployment type.
func (m *model) refreshCustomList() {
	items := []list.Item{customItem{add: true}}
	for _, c := range m.customDeploys {
		items = append(items, customItem{
			name: c.Name, url: c.ScriptURL,
			scheme: c.Scheme, port: c.Port, path: c.Path,
		})
	}
	m.customList.SetItems(items)
}

// slugifyName turns a friendly deployment name into a DNS-ish VM-name prefix
// (lowercase alphanumerics, single dashes), e.g. "Postgres Node" -> "postgres-node".
func slugifyName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "custom"
	}
	return out
}

// deploySelectedCustom acts on the highlighted submenu row: the "add" row opens
// the new-deployment form; a saved row provisions a VM from the configured image
// and runs its setup script. Names auto-increment from the deployment slug.
func (m *model) deploySelectedCustom() tea.Cmd {
	it, ok := m.customList.SelectedItem().(customItem)
	if !ok {
		return nil
	}
	if it.add {
		return m.openCustomDeploy()
	}
	if m.pcCfg == nil {
		m.notice = "Prism Central not configured"
		return nil
	}
	if m.procBusy {
		m.notice = "a deploy/delete is already running"
		return nil
	}
	dcfg := withDeployDefaults(m.deployCfg)
	var missing []string
	if dcfg.ImageName == "" {
		missing = append(missing, "image")
	}
	if dcfg.ClusterName == "" {
		missing = append(missing, "cluster")
	}
	if dcfg.SubnetName == "" {
		missing = append(missing, "subnet")
	}
	if dcfg.VMPassword == "" {
		missing = append(missing, "VM password")
	}
	if len(missing) > 0 {
		m.notice = "set " + strings.Join(missing, ", ") + " in Nutanix settings first"
		return m.openNutanixCfg()
	}
	cfg := it.cfg()
	args := []string{"pattern-custom", "--script-url", customCommand(cfg), "--name-prefix", slugifyName(it.name) + "-"}
	args = append(args, m.deployFlags()...)
	// Remember the workload's access config so we can show a clickable link once
	// the VM's IP is reported.
	m.pendingCustom = &customRun{cfg: cfg}
	m.notice = "deploying " + it.name + " — provisioning VM, then running setup script (see Output)"
	return m.startProc(args, "deploy "+it.name)
}

// deploySelectedCustomToWorker opens a picker for installing the highlighted
// custom workload on a worker that is already registered with Olla.
func (m *model) deploySelectedCustomToWorker() tea.Cmd {
	it, ok := m.customList.SelectedItem().(customItem)
	if !ok || it.add {
		m.notice = "select a saved custom deployment first"
		return nil
	}
	if m.procBusy {
		m.notice = "a deploy/delete is already running"
		return nil
	}
	return m.openCustomWorkerPick(it.cfg())
}

// startCustomOnWorker runs a custom setup command on an existing worker through
// the managed SSH-key update-plan runner, without provisioning another VM.
func (m *model) startCustomOnWorker(cfg customDeploy, worker workerRef) tea.Cmd {
	if m.procBusy {
		m.notice = "a deploy/delete is already running"
		return nil
	}
	port, err := m.allocateServicePort(cfg.Name, worker.host, cfg.Port)
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	cfg.Port = port
	access := cfg.accessURL(worker.host)
	m.pendingCustom = &customRun{cfg: cfg, target: worker.name, url: access}
	m.procBusy = true
	m.section = secNutanix
	m.nutanixCustom = true
	m.logLines = append(m.logLines, fmt.Sprintf(">>> deploy %s on existing worker %s (%s)", cfg.Name, worker.name, worker.host))
	m.renderLog()
	m.procCh = make(chan ProcEvent, 128)
	step := updateStep{
		title:  "Deploy " + cfg.Name + " on " + worker.name,
		host:   worker.host,
		user:   orDefault(m.sshUser, "rocky"),
		pass:   m.sshPass,
		script: customSetupScript(cfg),
	}
	go RunUpdatePlan([]updateStep{step}, m.procCh)
	m.notice = "deploying " + cfg.Name + " on existing worker " + worker.name + " — see Output"
	return waitProc(m.procCh)
}

// customSetupScript mirrors the Python custom installer for an existing host.
// Bare HTTP(S) URLs are downloaded and run with sudo; full commands run as-is
// with pipefail so a failed download cannot be masked by the final pipeline step.
func customSetupScript(cfg customDeploy) string {
	spec := strings.TrimSpace(cfg.ScriptURL)
	portEnv := ""
	if cfg.Port != "" {
		portEnv = "AIDT_SERVICE_PORT=" + cfg.Port + " PORT=" + cfg.Port + " "
	}
	if isBareHTTPURL(spec) {
		q := shSingle(spec)
		return "set -euo pipefail\n" +
			"tmp=$(mktemp /tmp/aidt-custom-XXXXXX.sh)\n" +
			"trap 'rm -f \"$tmp\"' EXIT\n" +
			"if command -v curl >/dev/null 2>&1; then curl -fsSL '" + q + "' -o \"$tmp\"; " +
			"elif command -v wget >/dev/null 2>&1; then wget -qO \"$tmp\" '" + q + "'; " +
			"else echo '[deploy] ERROR: curl or wget is required' >&2; exit 1; fi\n" +
			"chmod +x \"$tmp\"\n" +
			"sudo -n env " + portEnv + "bash \"$tmp\"\n"
	}
	prefix := ""
	if cfg.Port != "" {
		prefix = "export AIDT_SERVICE_PORT=" + cfg.Port + " PORT=" + cfg.Port + "\n"
	}
	return "set -euo pipefail\n" + prefix + customCommand(cfg) + "\n"
}

// customCommand injects the allocated service port into the built-in installers
// using the environment variables they officially support.
func customCommand(cfg customDeploy) string {
	port := orDefault(cfg.Port, "8443")
	switch {
	case cfg.Name == "NP4M" && cfg.ScriptURL == np4mInstall:
		return "curl -fsSL https://raw.githubusercontent.com/script-repo/ntnx-np4m/main/install.sh | sudo env NP4M_PORT=" + port + " bash"
	case cfg.Name == "NRCC" && (cfg.ScriptURL == nrccInstall || cfg.ScriptURL == nrccLegacy):
		return "curl -fsSL https://raw.githubusercontent.com/script-repo/ntnx-console-client/main/install.sh | NRCC_NO_OPEN=1 NRCC_PORT=" + port + " bash"
	default:
		return cfg.ScriptURL
	}
}

func isBareHTTPURL(spec string) bool {
	if strings.ContainsAny(spec, " \t\r\n") {
		return false
	}
	u, err := url.ParseRequestURI(spec)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// deleteSelectedCustom removes the highlighted saved deployment type (the "add"
// row is ignored) and persists the change.
func (m *model) deleteSelectedCustom() {
	it, ok := m.customList.SelectedItem().(customItem)
	if !ok || it.add {
		return
	}
	var out []customDeploy
	for _, c := range m.customDeploys {
		if c.Name == it.name && c.ScriptURL == it.url {
			continue
		}
		out = append(out, c)
	}
	m.customDeploys = out
	_ = saveCustomDeploys(m.tokFile, m.customDeploys)
	m.refreshCustomList()
	m.notice = "deleted custom deployment: " + it.name
}
