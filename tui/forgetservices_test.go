package main

import (
	"github.com/charmbracelet/bubbles/list"
	"path/filepath"
	"strings"
	"testing"
)

func serviceNames(m *model) []string {
	var out []string
	for _, s := range m.services {
		out = append(out, s.Name+"@"+s.Target)
	}
	return out
}

func newServicesModel(t *testing.T) model {
	t.Helper()
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.services = []serviceLink{
		// Installed onto a freshly provisioned VM: recorded against the VM name.
		{Name: "NP4M", Target: "np4m-01", URL: "https://10.0.0.5:8443"},
		{Name: "MicroK8s", Target: "microk8s-01", URL: "https://10.0.0.6:16443",
			Detail: "cluster 10.0.0.6 · MetalLB pool 10.0.0.81-10.0.0.85"},
		// Installed alongside an existing worker: recorded against the endpoint.
		{Name: "NRCC", Target: "aidt-worker-02", URL: "https://10.0.0.7:8443"},
		// Unrelated, must survive.
		{Name: "NP4M", Target: "np4m-99", URL: "https://10.0.0.9:8443"},
	}
	return m
}

// A VM deployed from scratch records the service against its VM name, and the
// delete path knows the VM's address, so either alone must be enough.
func TestDeletedVMDropsItsServicesByAddress(t *testing.T) {
	m := newServicesModel(t)
	dropped := m.forgetServices([]string{"10.0.0.5"}, nil)
	if len(dropped) != 1 || !strings.Contains(dropped[0], "NP4M on np4m-01") {
		t.Fatalf("dropped = %v", dropped)
	}
	if got := serviceNames(&m); len(got) != 3 {
		t.Errorf("remaining services = %v", got)
	}
}

func TestDeletedVMDropsItsServicesByName(t *testing.T) {
	m := newServicesModel(t)
	// A VM whose address was never learned ("-") can still be matched by name.
	dropped := m.forgetServices([]string{"-"}, []string{"microk8s-01"})
	if len(dropped) != 1 || !strings.Contains(dropped[0], "MicroK8s on microk8s-01") {
		t.Fatalf("dropped = %v", dropped)
	}
	for _, s := range m.services {
		if s.Name == "MicroK8s" {
			t.Error("MicroK8s service survived deletion of its VM")
		}
	}
}

// Deleting a worker must take the services installed onto it, which are
// recorded against the worker's endpoint name rather than a VM name.
func TestDeletedWorkerDropsCoLocatedServices(t *testing.T) {
	m := newServicesModel(t)
	dropped := m.forgetServices([]string{"10.0.0.7"}, []string{"aidt-worker-02"})
	if len(dropped) != 1 || !strings.Contains(dropped[0], "NRCC on aidt-worker-02") {
		t.Fatalf("dropped = %v", dropped)
	}
}

func TestUnrelatedServicesSurviveADelete(t *testing.T) {
	m := newServicesModel(t)
	before := len(m.services)
	if dropped := m.forgetServices([]string{"10.0.0.250"}, []string{"some-other-vm"}); dropped != nil {
		t.Errorf("unrelated delete removed %v", dropped)
	}
	if len(m.services) != before {
		t.Errorf("service count changed: %d -> %d", before, len(m.services))
	}
	// An empty request must be a no-op, not a wipe.
	if dropped := m.forgetServices(nil, nil); dropped != nil {
		t.Errorf("empty delete removed %v", dropped)
	}
	if len(m.services) != before {
		t.Error("empty delete changed the service list")
	}
}

// "-" is how pc.go reports an unknown address. Treating it as a host would make
// it match every service whose URL failed to parse.
func TestUnknownAddressIsNotAWildcard(t *testing.T) {
	m := newServicesModel(t)
	m.services = append(m.services, serviceLink{Name: "Broken", Target: "odd", URL: "not a url"})
	before := len(m.services)
	if dropped := m.forgetServices([]string{"-", ""}, nil); dropped != nil {
		t.Errorf("placeholder address matched %v", dropped)
	}
	if len(m.services) != before {
		t.Error("placeholder address removed services")
	}
}

// The Nutanix view keeps the last deploy as a clickable link and "b" opens it.
// It must not survive the VM it points at.
func TestStaleLastAccessLinkIsCleared(t *testing.T) {
	m := newServicesModel(t)
	m.lastCustomAccess = "https://10.0.0.6:16443"
	m.lastCustomName = "MicroK8s on microk8s-01"
	m.forgetServices([]string{"10.0.0.6"}, []string{"microk8s-01"})
	if m.lastCustomAccess != "" || m.lastCustomName != "" {
		t.Errorf("stale access link survived: %q / %q", m.lastCustomAccess, m.lastCustomName)
	}

	// A delete elsewhere must leave a still-valid link alone.
	m2 := newServicesModel(t)
	m2.lastCustomAccess = "https://10.0.0.6:16443"
	m2.lastCustomName = "MicroK8s on microk8s-01"
	m2.forgetServices([]string{"10.0.0.5"}, []string{"np4m-01"})
	if m2.lastCustomAccess == "" {
		t.Error("an unrelated delete cleared a valid access link")
	}
}

// Removal has to reach disk, or the service reappears on the next launch.
func TestForgottenServicesArePersisted(t *testing.T) {
	m := newServicesModel(t)
	m.forgetServices([]string{"10.0.0.5"}, []string{"np4m-01"})
	reloaded := loadSettings(m.tokFile)
	for _, s := range reloaded.Services {
		if s.Target == "np4m-01" {
			t.Error("deleted service was still persisted in tui.json")
		}
	}
	if len(reloaded.Services) != 3 {
		t.Errorf("persisted %d services, want 3", len(reloaded.Services))
	}
}

// The delete path must capture the VM name, since services deployed onto a new
// VM are keyed by it and the address list alone would miss them.
func TestDeleteCapturesVMNameForServiceCleanup(t *testing.T) {
	m := newServicesModel(t)
	m.pcCfg = &PCConfig{Host: "pc.example", User: "admin", Password: "x"}
	m.vmsList.SetItems([]list.Item{
		vmItem{name: "microk8s-01", role: "custom", ip: "10.0.0.6", power: "ON"},
	})
	m.vmsList.Select(0)
	_ = m.deleteSelectedVM()
	if !containsStr(m.pendingDeleteVMs, "microk8s-01") {
		t.Errorf("VM name not captured for cleanup: %v", m.pendingDeleteVMs)
	}
	if !containsStr(m.pendingDeleteHosts, "10.0.0.6") {
		t.Errorf("VM address not captured for cleanup: %v", m.pendingDeleteHosts)
	}
}
