package main

import (
	"strings"
	"testing"
)

func TestGatewayServiceUsesVMName(t *testing.T) {
	m := newModel("http://10.0.0.10:40114", "rocky", "pw")
	m.vms = []VM{{Name: "aidt-gateway-03", IP: "10.0.0.10", Role: "gateway"}}
	m.refreshServices()

	items := m.servicesList.Items()
	if len(items) == 0 {
		t.Fatal("gateway service missing")
	}
	gateway, ok := items[0].(serviceItem)
	if !ok {
		t.Fatalf("first service has type %T", items[0])
	}
	if gateway.name != "aidt-gateway-03" {
		t.Fatalf("gateway service name = %q", gateway.name)
	}
	if gateway.target != "10.0.0.10" || gateway.kind != "gateway" {
		t.Fatalf("gateway service metadata = %#v", gateway)
	}
}

func TestGatewayServiceUsesHostnameWithoutVMInventory(t *testing.T) {
	m := newModel("http://aidt-gateway-04:40114", "rocky", "pw")
	if got := m.gatewayServiceName(); got != "aidt-gateway-04" {
		t.Fatalf("gateway service name = %q", got)
	}
}

func TestGatewayServiceDoesNotUseUnrelatedVM(t *testing.T) {
	m := newModel("http://external-olla.example.com:40114", "rocky", "pw")
	m.vms = []VM{{Name: "aidt-gateway-99", IP: "10.0.0.99", Role: "gateway"}}
	if got := m.gatewayServiceName(); got != "Olla gateway" {
		t.Fatalf("unrelated gateway VM was used as service name: %q", got)
	}
}

func TestRegisteredOpenCodeHostsAppearAsServices(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.agentReg["OpenCode"] = "10.0.0.2"
	m.agentHosts["OpenCode"] = []string{"10.0.0.2", "worker.example.com"}
	m.refreshServices()

	var services []serviceItem
	for _, item := range m.servicesList.Items() {
		service := item.(serviceItem)
		if service.name == "OpenCode" {
			services = append(services, service)
		}
	}
	if len(services) != 2 {
		t.Fatalf("OpenCode service count = %d, want 2", len(services))
	}
	for _, service := range services {
		if service.kind != "agent server" || !strings.HasSuffix(service.url, ":4096/doc") {
			t.Errorf("unexpected OpenCode service: %#v", service)
		}
	}
}

func TestOpenCodeServiceSupportsIPv6Hosts(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.agentReg["OpenCode"] = "2001:db8::10"
	m.agentHosts["OpenCode"] = []string{"2001:db8::10"}
	m.refreshServices()

	found := false
	for _, item := range m.servicesList.Items() {
		service := item.(serviceItem)
		if service.name == "OpenCode" {
			found = true
			if service.url != "http://[2001:db8::10]:4096/doc" {
				t.Fatalf("IPv6 OpenCode URL = %q", service.url)
			}
		}
	}
	if !found {
		t.Fatal("IPv6 OpenCode service missing")
	}
}
