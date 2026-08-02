package main

import (
	"path/filepath"
	"testing"
)

func vmRows(m *model) map[string]string {
	out := map[string]string{}
	for _, it := range m.vmsList.Items() {
		if vi, ok := it.(vmItem); ok {
			out[vi.name] = vi.role
		}
	}
	return out
}

// A worker given a name that matches no AIDT convention still has to appear in
// the Nutanix list. It shows up under Load already, because Load renders live
// gateway endpoints and never consults the name.
func TestCustomNamedWorkerAppearsInNutanixList(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.vms = []VM{
		{Name: "aidt-gateway-01", IP: "10.0.0.1", Role: vmRole("aidt-gateway-01")},
		{Name: "aidt-worker-01", IP: "10.0.0.2", Role: vmRole("aidt-worker-01")},
		{Name: "bigrig", IP: "10.0.0.3", Role: vmRole("bigrig")},
		{Name: "unrelated-db", IP: "10.0.0.9", Role: vmRole("unrelated-db")},
	}

	// Before the pool is known, the freely named worker is indistinguishable
	// from any other VM in Prism.
	m.refreshVMs()
	if _, ok := vmRows(&m)["bigrig"]; ok {
		t.Fatal("precondition: bigrig should not be classifiable before pool membership is known")
	}

	// Once it is registered with Olla, it is ours.
	m.endpoints = []endpointEntry{{Name: "bigrig", URL: "http://10.0.0.3:11434"}}
	m.refreshVMs()
	rows := vmRows(&m)
	if rows["bigrig"] != "worker" {
		t.Errorf("registered worker with a custom name: role = %q, want \"worker\"", rows["bigrig"])
	}
	// The conventional VMs keep working, and unrelated Prism VMs stay hidden.
	if rows["aidt-worker-01"] != "worker" || rows["aidt-gateway-01"] != "gateway" {
		t.Errorf("conventional roles regressed: %+v", rows)
	}
	if _, ok := rows["unrelated-db"]; ok {
		t.Error("an unrelated Prism VM leaked into the managed list")
	}
}

// The gateway's own status is populated on connect, before any SSH read of
// olla.yaml, so it must count as a membership source too.
func TestStatusEndpointsAlsoIdentifyManagedVMs(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.vms = []VM{{Name: "oddly-named", IP: "10.0.0.4", Role: vmRole("oddly-named")}}
	m.status = Status{Endpoints: []Endpoint{{Name: "oddly-named", URL: "http://10.0.0.4:11434"}}}
	m.refreshVMs()
	if vmRows(&m)["oddly-named"] != "worker" {
		t.Error("a worker known only from live gateway status was dropped")
	}
}

// pc.go reports an unknown address as "-". Treating that as a host would make
// every address-less VM match whatever the map happened to contain.
func TestUnknownAddressNeverMatchesThePool(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.vms = []VM{
		{Name: "no-address", IP: "-", Role: vmRole("no-address")},
		{Name: "blank-address", IP: "", Role: vmRole("blank-address")},
	}
	m.endpoints = []endpointEntry{{Name: "w", URL: "http://10.0.0.3:11434"}}
	m.refreshVMs()
	if rows := vmRows(&m); len(rows) != 0 {
		t.Errorf("address-less VMs must not be treated as managed, got %+v", rows)
	}
}

// A single box acting as both gateway and worker should read as the gateway.
func TestCombinedGatewayWorkerPrefersGatewayRole(t *testing.T) {
	m := newModel("http://10.0.0.7:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.vms = []VM{{Name: "solo", IP: "10.0.0.7", Role: vmRole("solo")}}
	m.endpoints = []endpointEntry{{Name: "solo", URL: "http://10.0.0.7:11434"}}
	m.refreshVMs()
	if got := vmRows(&m)["solo"]; got != "gateway" {
		t.Errorf("combined host role = %q, want \"gateway\"", got)
	}
}

// The list is rebuilt when membership changes, but not on every status poll —
// rebuilding resets the operator's selection and filter.
func TestManagedVMSyncOnlyFiresOnMembershipChange(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.vms = []VM{{Name: "bigrig", IP: "10.0.0.3", Role: vmRole("bigrig")}}
	m.endpoints = []endpointEntry{{Name: "bigrig", URL: "http://10.0.0.3:11434"}}
	m.refreshVMs()

	sig := m.managedSig
	if sig == "" {
		t.Fatal("signature was not recorded")
	}
	// An identical poll must not invalidate the list.
	m.syncManagedVMs()
	if m.managedSig != sig {
		t.Error("signature changed without a membership change")
	}
	// A new worker must.
	m.endpoints = append(m.endpoints, endpointEntry{Name: "second", URL: "http://10.0.0.8:11434"})
	m.syncManagedVMs()
	if m.managedSig == sig {
		t.Error("adding a worker did not rebuild the managed list")
	}
}
