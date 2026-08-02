package main

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// The App Services section answers the question App Deploy leaves open: an app
// is installed, but where can it actually be reached?
//
// It is the Kubernetes counterpart to the Services section. Services lists
// gateway, worker, and VM-based custom-deployment URLs; this lists the
// addresses a deployed chart or manifest ended up exposing.
//
// Nothing here is persisted. A Service's external address is assigned by the
// cluster — a LoadBalancer IP can change, an Ingress host can be re-pointed —
// so a remembered URL would eventually send an operator somewhere wrong. The
// list is rebuilt from the cluster on refresh, like the K8S cluster list.

// appService is one reachable (or explicitly unreachable) address exposed by a
// deployed application.
type appService struct {
	App       string
	Context   string
	Namespace string
	Release   string

	Name string // Service or Ingress object name
	Kind string // LoadBalancer | NodePort | ClusterIP | Ingress
	URL  string // best-effort external URL; empty when not externally reachable
	// Detail explains what was found — the ports, or why there is no URL.
	Detail string
}

// reachable reports whether this row gives the operator somewhere to click.
func (s appService) reachable() bool { return s.URL != "" }

// key identifies a row for dedupe.
func (s appService) key() string {
	return strings.Join([]string{s.Context, s.Namespace, s.Name, s.Kind, s.URL}, "\x00")
}

// ---- discovery --------------------------------------------------------------

// appServicesScript asks the gateway what each recorded installation exposes.
//
// One script covers every installation so this stays a single SSH round trip.
// Output is line-oriented and marker-prefixed rather than JSON because the
// per-object shapes differ and a jsonpath template keeps the parsing on this
// side small and testable.
//
// Scoping is best-effort by design. A Helm release *usually* stamps
// app.kubernetes.io/instance on its objects, but that is a convention rather
// than a guarantee — podinfo, for one, does not — so a selector that finds
// nothing falls back to listing the namespace. Being slightly over-inclusive in
// a shared namespace beats reporting that a running app exposes nothing.
func appServicesScript(ds []appDeployment) string {
	const svcJP = `{range .items[*]}AIDT_SVC {.metadata.name}|{.spec.type}|{.status.loadBalancer.ingress[0].ip}|{.status.loadBalancer.ingress[0].hostname}|{.spec.clusterIP}|{range .spec.ports[*]}{.port}:{.nodePort},{end}{"\n"}{end}`
	const ingJP = `{range .items[*]}AIDT_ING {.metadata.name}|{.spec.rules[0].host}|{.spec.rules[0].http.paths[0].path}|{.spec.tls[0].secretName}|{.status.loadBalancer.ingress[0].ip}{"\n"}{end}`

	var b strings.Builder
	b.WriteString(`set -uo pipefail
export PATH="/usr/local/bin:$HOME/.local/bin:$PATH"
AIDT_SVC_JP='` + svcJP + `'
AIDT_ING_JP='` + ingJP + `'

# $1 context, $2 namespace, $3 resource, $4 jsonpath, $5 label selector (may be empty)
aidt_q() {
  local out=""
  if [ -n "$5" ]; then
    out=$(kubectl --context "$1" -n "$2" get "$3" -l "$5" -o jsonpath="$4" 2>/dev/null)
  fi
  if [ -z "$out" ]; then
    out=$(kubectl --context "$1" -n "$2" get "$3" -o jsonpath="$4" 2>/dev/null)
  fi
  [ -n "$out" ] && printf '%s\n' "$out"
  return 0
}
`)
	q := func(s string) string { return "'" + shSingle(s) + "'" }

	// A NodePort is only useful with a node address, and that is per-cluster,
	// so resolve it once per distinct context rather than per installation.
	seenCtx := map[string]bool{}
	for _, d := range ds {
		if seenCtx[d.Context] {
			continue
		}
		seenCtx[d.Context] = true
		b.WriteString("echo \"AIDT_NODE " + shSingle(d.Context) + " $(kubectl --context " + q(d.Context) +
			" get nodes -o jsonpath='{.items[0].status.addresses[?(@.type==\"InternalIP\")].address}' 2>/dev/null)\"\n")
	}

	for _, d := range ds {
		sel := ""
		if d.Kind == appKindHelm {
			sel = "app.kubernetes.io/instance=" + d.Release
		}
		b.WriteString("echo " + q("AIDT_BEGIN "+d.label()) + "\n")
		b.WriteString("aidt_q " + q(d.Context) + " " + q(d.Namespace) + " svc \"$AIDT_SVC_JP\" " + q(sel) + "\n")
		b.WriteString("aidt_q " + q(d.Context) + " " + q(d.Namespace) + " ingress \"$AIDT_ING_JP\" " + q(sel) + "\n")
		b.WriteString("echo " + q("AIDT_END") + "\n")
	}
	return b.String()
}

// schemeForPort picks http or https from a port number. The common TLS ports
// are treated as https so a link opens on the right scheme without having to
// inspect the workload.
func schemeForPort(port int) string {
	switch port {
	case 443, 8443, 9443:
		return "https"
	}
	return "http"
}

// buildURL assembles a URL, returning "" for anything unusable.
func buildURL(scheme, host string, port int, path string) string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || port < 1 || port > 65535 {
		return ""
	}
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// A bare "/" adds nothing to a displayed link.
	if path == "/" {
		path = ""
	}
	return (&url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(port)), Path: path}).String()
}

// portPairs parses the "80:31234,443:0," field into (port, nodePort) pairs.
func portPairs(field string) [][2]int {
	var out [][2]int
	for _, p := range strings.Split(field, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		bits := strings.SplitN(p, ":", 2)
		port, err := strconv.Atoi(strings.TrimSpace(bits[0]))
		if err != nil {
			continue
		}
		node := 0
		if len(bits) == 2 {
			node, _ = strconv.Atoi(strings.TrimSpace(bits[1]))
		}
		out = append(out, [2]int{port, node})
	}
	return out
}

// parseAppServices turns the discovery output into rows.
//
// Every installation contributes at least one row: an app that exposes nothing
// externally still has to appear, because "deployed but only reachable from
// inside the cluster" is a real answer to "where is this?" and silently
// omitting it would read as "not deployed".
func parseAppServices(out string, ds []appDeployment) []appService {
	byLabel := map[string]appDeployment{}
	for _, d := range ds {
		byLabel[d.label()] = d
	}
	nodeIP := map[string]string{}
	found := map[string][]appService{}

	var cur appDeployment
	var curLabel string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "AIDT_NODE "):
			rest := strings.TrimPrefix(line, "AIDT_NODE ")
			// The context name can contain spaces in principle; the address is
			// the last field.
			i := strings.LastIndex(rest, " ")
			if i > 0 {
				nodeIP[strings.TrimSpace(rest[:i])] = strings.TrimSpace(rest[i+1:])
			}
		case strings.HasPrefix(line, "AIDT_BEGIN "):
			curLabel = strings.TrimSpace(strings.TrimPrefix(line, "AIDT_BEGIN "))
			cur = byLabel[curLabel]
		case line == "AIDT_END":
			curLabel = ""
		case strings.HasPrefix(line, "AIDT_SVC "), strings.HasPrefix(line, "AIDT_ING "):
			if curLabel == "" || cur.App == "" {
				continue
			}
			if s, ok := parseServiceLine(line, cur, nodeIP[cur.Context]); ok {
				found[curLabel] = append(found[curLabel], s...)
			}
		}
	}

	var out2 []appService
	seen := map[string]bool{}
	for _, d := range ds {
		rows := found[d.label()]
		if len(rows) == 0 {
			out2 = append(out2, appService{
				App: d.App, Context: d.Context, Namespace: d.Namespace, Release: d.Release,
				Kind: "none", Detail: "no Service or Ingress found — nothing is exposed",
			})
			continue
		}
		for _, r := range rows {
			if seen[r.key()] {
				continue
			}
			seen[r.key()] = true
			out2 = append(out2, r)
		}
	}
	sort.SliceStable(out2, func(i, j int) bool {
		if out2[i].App != out2[j].App {
			return out2[i].App < out2[j].App
		}
		// Reachable rows first: they are what the operator came here for.
		if out2[i].reachable() != out2[j].reachable() {
			return out2[i].reachable()
		}
		return out2[i].Name < out2[j].Name
	})
	return out2
}

// parseServiceLine expands one Service or Ingress record into rows.
func parseServiceLine(line string, d appDeployment, node string) ([]appService, bool) {
	base := appService{App: d.App, Context: d.Context, Namespace: d.Namespace, Release: d.Release}

	if strings.HasPrefix(line, "AIDT_ING ") {
		f := strings.Split(strings.TrimPrefix(line, "AIDT_ING "), "|")
		for len(f) < 5 {
			f = append(f, "")
		}
		name, host, path, tls, ip := f[0], f[1], f[2], f[3], f[4]
		if name == "" {
			return nil, false
		}
		scheme := "http"
		port := 80
		if tls != "" {
			scheme, port = "https", 443
		}
		target := orDefault(host, ip)
		s := base
		s.Name, s.Kind = name, "Ingress"
		s.URL = buildURL(scheme, target, port, path)
		if s.URL == "" {
			s.Detail = "ingress has no host or address yet"
		} else {
			s.Detail = "ingress"
			if host == "" {
				s.Detail = "ingress (no host; using address — DNS may be required)"
			}
		}
		return []appService{s}, true
	}

	f := strings.Split(strings.TrimPrefix(line, "AIDT_SVC "), "|")
	for len(f) < 6 {
		f = append(f, "")
	}
	name, typ, lbIP, lbHost, clusterIP, ports := f[0], f[1], f[2], f[3], f[4], f[5]
	if name == "" {
		return nil, false
	}
	pairs := portPairs(ports)

	s := base
	s.Name, s.Kind = name, orDefault(typ, "ClusterIP")

	switch typ {
	case "LoadBalancer":
		addr := orDefault(lbIP, lbHost)
		if addr == "" {
			// MetalLB and cloud LBs both go through this state; saying so beats
			// showing a blank link.
			s.Detail = "LoadBalancer has no external address yet (pending)"
			return []appService{s}, true
		}
		var rows []appService
		for _, p := range pairs {
			r := s
			r.URL = buildURL(schemeForPort(p[0]), addr, p[0], "")
			r.Detail = fmt.Sprintf("LoadBalancer %s port %d", addr, p[0])
			rows = append(rows, r)
		}
		if len(rows) == 0 {
			s.Detail = "LoadBalancer " + addr + " exposes no ports"
			return []appService{s}, true
		}
		return rows, true

	case "NodePort":
		if node == "" {
			s.Detail = "NodePort, but no node address is known for this cluster"
			return []appService{s}, true
		}
		var rows []appService
		for _, p := range pairs {
			if p[1] == 0 {
				continue
			}
			r := s
			r.URL = buildURL(schemeForPort(p[0]), node, p[1], "")
			r.Detail = fmt.Sprintf("NodePort %d → service port %d on %s", p[1], p[0], node)
			rows = append(rows, r)
		}
		if len(rows) == 0 {
			s.Detail = "NodePort service with no node ports assigned"
			return []appService{s}, true
		}
		return rows, true

	default:
		// ClusterIP (and ExternalName, headless, etc.): not reachable from
		// outside. Give the operator the command that does reach it rather than
		// a dead link.
		port := 0
		if len(pairs) > 0 {
			port = pairs[0][0]
		}
		s.Detail = fmt.Sprintf("cluster-internal only (%s)", dashIf(clusterIP))
		if port > 0 {
			s.Detail += fmt.Sprintf(" — kubectl --context %s -n %s port-forward svc/%s %d:%d",
				d.Context, d.Namespace, name, port, port)
		}
		return []appService{s}, true
	}
}

// appServicesMsg carries a refreshed discovery pass.
type appServicesMsg struct {
	services []appService
	err      error
}

// appServicesCmd discovers where every recorded installation is reachable.
func appServicesCmd(host, user, pass string, ds []appDeployment) tea.Cmd {
	return func() tea.Msg {
		if len(ds) == 0 {
			return appServicesMsg{}
		}
		client, err := dialSSH(host, user, pass)
		if err != nil {
			return appServicesMsg{err: fmt.Errorf("connect to %s: %w", host, err)}
		}
		defer client.Close()
		// Individual kubectl calls can fail (an unreachable cluster among
		// several), and the per-installation markers still make the rest
		// usable, so the exit status is not treated as fatal.
		out, _ := runSSH(client, appServicesScript(ds))
		return appServicesMsg{services: parseAppServices(out, ds)}
	}
}

// ---- section ----------------------------------------------------------------

func (m *model) refreshAppServicesList() {
	items := make([]list.Item, 0, len(m.appServices))
	for _, s := range m.appServices {
		items = append(items, appServiceItem{
			app: s.App, name: s.Name, kind: s.Kind,
			context: s.Context, namespace: s.Namespace,
			url: s.URL, detail: s.Detail,
		})
	}
	m.appSvcList.SetItems(items)
}

func (m *model) selectedAppService() (appServiceItem, bool) {
	i, ok := m.appSvcList.SelectedItem().(appServiceItem)
	return i, ok
}

// refreshAppServicesCmd re-runs discovery against the gateway.
func (m *model) refreshAppServicesCmd() tea.Cmd {
	host := hostFromURL(m.gateway)
	if host == "" {
		m.appSvcErr = "no gateway configured — connect one first (c on the sidebar)"
		return nil
	}
	if m.sshPass == "" && managedKeyPath() == "" {
		m.appSvcErr = "no gateway credentials — reconnect with c on the sidebar"
		return nil
	}
	if len(m.appDeploys) == 0 {
		m.appSvcErr = ""
		m.appServices = nil
		m.refreshAppServicesList()
		return nil
	}
	m.appSvcErr = ""
	m.appSvcLoading = true
	return appServicesCmd(host, orDefault(m.sshUser, "rocky"), m.sshPass, m.appDeploys)
}

// openSelectedAppService opens the highlighted row's URL.
func (m *model) openSelectedAppService() tea.Cmd {
	it, ok := m.selectedAppService()
	if !ok {
		m.notice = "select a service first"
		return nil
	}
	if it.url == "" {
		// The detail already carries the port-forward command for the common
		// case, so point at it rather than repeating it in a transient notice.
		m.notice = it.app + " is not reachable from outside the cluster — see the row for how to reach it"
		return nil
	}
	if err := openBrowser(it.url); err != nil {
		m.notice = "could not open browser: " + err.Error()
		return nil
	}
	m.notice = "opening " + it.url
	return nil
}
