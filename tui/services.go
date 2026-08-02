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

// serviceItem is one directly accessible service exposed by the gateway, an
// Ollama worker, a deployed agent server, or a completed custom deployment.
type serviceItem struct {
	name   string
	target string
	url    string
	kind   string
	detail string // extra context the deployment reported about itself
}

func (i serviceItem) Title() string { return i.name }
func (i serviceItem) Description() string {
	where := i.kind
	if i.target != "" {
		where += " on " + i.target
	}
	desc := where + " · " + i.url
	if i.detail != "" {
		desc += " · " + i.detail
	}
	return desc
}
func (i serviceItem) FilterValue() string { return i.name + " " + i.target + " " + i.url }

func validServiceURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// refreshServices combines live gateway/worker URLs, registered agent servers,
// and persisted custom services. Only successful custom installs are persisted.
func (m *model) refreshServices() {
	items := make([]list.Item, 0, 1+len(m.endpoints)+len(m.agentHosts["OpenCode"])+len(m.services))
	seen := map[string]bool{}
	add := func(i serviceItem) {
		if !validServiceURL(i.url) || seen[i.name+"\x00"+i.target+"\x00"+i.url] {
			return
		}
		seen[i.name+"\x00"+i.target+"\x00"+i.url] = true
		items = append(items, i)
	}
	if m.gateway != "" {
		add(serviceItem{name: m.gatewayServiceName(), target: hostFromURL(m.gateway), url: m.gateway, kind: "gateway"})
	}
	for _, e := range m.endpoints {
		add(serviceItem{name: e.Name, target: hostFromURL(e.URL), url: e.URL, kind: "Ollama worker"})
	}
	for _, host := range m.agentDeployedHosts("OpenCode") {
		host = strings.Trim(strings.TrimSpace(host), "[]")
		if host == "" {
			continue
		}
		add(serviceItem{
			name:   "OpenCode",
			target: host,
			url:    (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, "4096"), Path: "/doc"}).String(),
			kind:   "agent server",
		})
	}
	for _, s := range m.services {
		add(serviceItem{name: s.Name, target: s.Target, url: s.URL, kind: "custom service", detail: s.Detail})
	}
	m.servicesList.SetItems(items)
}

func (m *model) gatewayServiceName() string {
	host := hostFromURL(m.gateway)
	for _, vm := range m.vms {
		if vm.Role != "gateway" {
			continue
		}
		if vm.IP == host || strings.EqualFold(vm.Name, host) {
			return vm.Name
		}
	}
	if strings.HasPrefix(strings.ToLower(host), "aidt-gateway-") {
		return host
	}
	return "Olla gateway"
}

func (m *model) selectedService() (serviceItem, bool) {
	i, ok := m.servicesList.SelectedItem().(serviceItem)
	return i, ok
}

func (m *model) openSelectedService() tea.Cmd {
	i, ok := m.selectedService()
	if !ok {
		m.notice = "select a service first"
		return nil
	}
	if err := openBrowser(i.url); err != nil {
		m.notice = "could not open browser: " + err.Error()
	} else {
		m.notice = "opening " + i.url
	}
	return nil
}

// recordCustomService upserts one successful custom deployment by service name
// and target, allowing the same service to be installed on multiple workers.
func (m *model) recordCustomService(run customRun) {
	if !validServiceURL(run.url) {
		return
	}
	link := serviceLink{Name: run.cfg.Name, Target: run.target, URL: run.url, Detail: run.detail}
	found := false
	for i := range m.services {
		if m.services[i].Name == link.Name && m.services[i].Target == link.Target {
			m.services[i] = link
			found = true
			break
		}
	}
	if !found {
		m.services = append(m.services, link)
	}
	_ = saveServices(m.tokFile, m.services)
	m.lastCustomName = run.cfg.Name + " on " + run.target
	m.lastCustomAccess = run.url
	m.refreshServices()
}

// forgetServices drops persisted custom services that belonged to a deleted VM
// and reports what it removed.
//
// Only custom services need this. Gateway, Ollama worker, and agent-server rows
// are derived live from the pool and the agent registry, so they stop being
// rendered as soon as the endpoint or registration goes away; the entries in
// tui.json are the only ones that would otherwise outlive their host.
//
// Matching is by address or by name because the two deploy paths record
// different targets: installing onto a freshly provisioned VM records the VM
// name, while installing onto an existing worker records that worker's endpoint
// name.
func (m *model) forgetServices(hosts, names []string) []string {
	byHost := map[string]bool{}
	for _, h := range hosts {
		if h != "" && h != "-" {
			byHost[h] = true
		}
	}
	byName := map[string]bool{}
	for _, n := range names {
		if n != "" {
			byName[n] = true
		}
	}
	if len(byHost) == 0 && len(byName) == 0 {
		return nil
	}

	kept := make([]serviceLink, 0, len(m.services))
	var dropped []string
	for _, s := range m.services {
		if byHost[hostFromURL(s.URL)] || byName[s.Target] {
			dropped = append(dropped, s.Name+" on "+s.Target)
			continue
		}
		kept = append(kept, s)
	}
	if len(dropped) == 0 {
		return nil
	}
	m.services = kept
	// The Nutanix view keeps the most recent deploy as a clickable link, and
	// "b" opens it. Pointing that at a VM that no longer exists is worse than
	// showing nothing.
	if byHost[hostFromURL(m.lastCustomAccess)] {
		m.lastCustomAccess, m.lastCustomName = "", ""
	}
	sort.Strings(dropped)
	_ = saveServices(m.tokFile, m.services)
	m.refreshServices()
	return dropped
}

const maxWorkerServicePort = 8543

// allocateServicePort returns the first available port at or above preferred on
// one worker. Redeploying the same service reuses its registered port.
func (m *model) allocateServicePort(name, host, preferred string) (string, error) {
	if preferred == "" {
		return "", nil
	}
	start, err := strconv.Atoi(preferred)
	if err != nil || start < 1 || start > maxWorkerServicePort {
		return "", fmt.Errorf("service port must be between 1 and %d", maxWorkerServicePort)
	}
	used := map[int]bool{}
	for _, s := range m.services {
		if !validServiceURL(s.URL) {
			continue
		}
		su, _ := url.Parse(s.URL)
		if !strings.EqualFold(su.Hostname(), strings.Trim(host, "[]")) {
			continue
		}
		port, err := strconv.Atoi(su.Port())
		if err != nil {
			continue
		}
		if s.Name == name {
			return strconv.Itoa(port), nil
		}
		used[port] = true
	}
	for port := start; port <= maxWorkerServicePort; port++ {
		if !used[port] {
			return strconv.Itoa(port), nil
		}
	}
	return "", fmt.Errorf("no service ports available on %s in range %d-%d", host, start, maxWorkerServicePort)
}
