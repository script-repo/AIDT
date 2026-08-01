package main

import "testing"

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
