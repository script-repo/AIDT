package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// selectServiceRow focuses the Services row with the given name.
func selectServiceRow(t *testing.T, m *model, name string) {
	t.Helper()
	for i, it := range m.servicesList.Items() {
		if si, ok := it.(serviceItem); ok && si.name == name {
			m.servicesList.Select(i)
			return
		}
	}
	t.Fatalf("no Services row named %q", name)
}

func newRemovableServicesModel(t *testing.T) model {
	t.Helper()
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.gateway = "http://10.0.0.1:40114"
	m.endpoints = []endpointEntry{{Name: "aidt-worker-01", URL: "http://10.0.0.2:11434"}}
	m.services = []serviceLink{
		{Name: "MicroK8s", Target: "microk8s-01", URL: "https://10.0.0.6:16443",
			Detail: "cluster 10.0.0.6 · MetalLB pool 10.0.0.81-10.0.0.85"},
		{Name: "NP4M", Target: "np4m-01", URL: "https://10.0.0.5:8443"},
	}
	m.refreshServices()
	return m
}

func TestRemoveSelectedServiceDropsAndPersists(t *testing.T) {
	m := newRemovableServicesModel(t)
	selectServiceRow(t, &m, "MicroK8s")
	m.removeSelectedService()

	for _, s := range m.services {
		if s.Name == "MicroK8s" {
			t.Fatal("service was not removed from the model")
		}
	}
	if len(m.services) != 1 {
		t.Errorf("services = %+v, want 1 remaining", m.services)
	}
	// Removal must survive a restart, or the row returns on next launch.
	reloaded := loadSettings(m.tokFile)
	if len(reloaded.Services) != 1 || reloaded.Services[0].Name != "NP4M" {
		t.Errorf("persisted services = %+v", reloaded.Services)
	}
	// The list itself must no longer render it.
	for _, it := range m.servicesList.Items() {
		if si, ok := it.(serviceItem); ok && si.name == "MicroK8s" {
			t.Error("removed service is still shown in Services")
		}
	}
	// Removing a listing must not read as having destroyed the workload.
	if !strings.Contains(m.notice, "still running") {
		t.Errorf("notice should clarify the workload survives, got %q", m.notice)
	}
}

// Gateway, worker, and agent-server rows come from live state. Removing one
// would do nothing visible, so it must explain rather than silently no-op.
func TestDerivedServiceRowsCannotBeRemoved(t *testing.T) {
	m := newRemovableServicesModel(t)
	before := len(m.services)

	for _, tc := range []struct{ row, wants string }{
		{"aidt-worker-01", "Pool"},
		{"Olla gateway", "sidebar"},
	} {
		var found bool
		for i, it := range m.servicesList.Items() {
			si, ok := it.(serviceItem)
			if !ok {
				continue
			}
			if si.kind == "Ollama worker" && tc.row == "aidt-worker-01" ||
				si.kind == "gateway" && tc.row == "Olla gateway" {
				m.servicesList.Select(i)
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no live row for %q", tc.row)
		}
		m.notice = ""
		m.removeSelectedService()
		if !strings.Contains(m.notice, tc.wants) {
			t.Errorf("%s: notice %q should point at %s", tc.row, m.notice, tc.wants)
		}
		if len(m.services) != before {
			t.Errorf("%s: a live row removed a persisted service", tc.row)
		}
	}
}

// Removing the row whose URL is the Nutanix "last deploy" link must clear that
// link too, otherwise "b" still opens something the operator just dismissed.
func TestRemovingServiceClearsMatchingLastAccessLink(t *testing.T) {
	m := newRemovableServicesModel(t)
	m.lastCustomAccess = "https://10.0.0.6:16443"
	m.lastCustomName = "MicroK8s on microk8s-01"
	selectServiceRow(t, &m, "MicroK8s")
	m.removeSelectedService()
	if m.lastCustomAccess != "" || m.lastCustomName != "" {
		t.Errorf("stale link survived: %q / %q", m.lastCustomAccess, m.lastCustomName)
	}

	// Removing a different row leaves the link alone.
	m2 := newRemovableServicesModel(t)
	m2.lastCustomAccess = "https://10.0.0.6:16443"
	m2.lastCustomName = "MicroK8s on microk8s-01"
	selectServiceRow(t, &m2, "NP4M")
	m2.removeSelectedService()
	if m2.lastCustomAccess == "" {
		t.Error("removing an unrelated row cleared a valid link")
	}
}

// Two services can share a name on different hosts; only the selected one goes.
func TestRemoveOnlyAffectsTheSelectedRow(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.gateway = ""
	m.services = []serviceLink{
		{Name: "NP4M", Target: "np4m-01", URL: "https://10.0.0.5:8443"},
		{Name: "NP4M", Target: "np4m-02", URL: "https://10.0.0.6:8443"},
	}
	m.refreshServices()
	for i, it := range m.servicesList.Items() {
		if si, ok := it.(serviceItem); ok && si.target == "np4m-02" {
			m.servicesList.Select(i)
		}
	}
	m.removeSelectedService()
	if len(m.services) != 1 || m.services[0].Target != "np4m-01" {
		t.Errorf("wrong row removed: %+v", m.services)
	}
}

func TestRemoveServiceWithEmptyListIsSafe(t *testing.T) {
	m := newModel("", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.gateway = ""
	m.endpoints = nil
	m.services = nil
	m.refreshServices()
	m.removeSelectedService() // must not panic
	if !strings.Contains(m.notice, "select a service") {
		t.Errorf("notice = %q", m.notice)
	}
}

// The Services footer must advertise the key, and must not borrow the VM
// binding, whose help text reads "delete VM".
func TestServicesFooterAdvertisesRemoveListing(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.section = secServices
	m.zone = zoneContent

	var found bool
	for _, b := range m.shortHelp() {
		if h := b.Help(); h.Key == "x" {
			found = true
			if h.Desc != "remove listing" {
				t.Errorf("x help = %q, want \"remove listing\"", h.Desc)
			}
		}
	}
	if !found {
		t.Error("Services footer does not advertise x")
	}
}
