package main

import (
	"strings"
	"testing"
)

func svcDeploys() []appDeployment {
	return []appDeployment{
		{App: "Open WebUI", Context: "lab", Namespace: "ai", Release: "openwebui", Kind: appKindHelm},
		{App: "Thing", Context: "lab", Namespace: "ops", Release: "thing", Kind: appKindManifest},
	}
}

func findSvc(t *testing.T, got []appService, app, kind string) appService {
	t.Helper()
	for _, s := range got {
		if s.App == app && s.Kind == kind {
			return s
		}
	}
	t.Fatalf("no %s row for %s in %+v", kind, app, got)
	return appService{}
}

func TestParseAppServicesLoadBalancer(t *testing.T) {
	ds := svcDeploys()
	out := strings.Join([]string{
		"AIDT_NODE lab 10.42.156.24",
		"AIDT_BEGIN lab · ai/openwebui",
		"AIDT_SVC openwebui|LoadBalancer|10.42.156.80||10.152.183.5|80:0,443:0,",
		"AIDT_END",
		"AIDT_BEGIN lab · ops/thing",
		"AIDT_END",
	}, "\n")
	got := parseAppServices(out, ds)

	var urls []string
	for _, s := range got {
		if s.App == "Open WebUI" && s.URL != "" {
			urls = append(urls, s.URL)
		}
	}
	// Both published ports should be offered, and 443 must come back as https
	// so the link opens on the right scheme.
	if len(urls) != 2 {
		t.Fatalf("got %d URLs, want 2: %v", len(urls), urls)
	}
	if !containsStr(urls, "http://10.42.156.80:80") {
		t.Errorf("missing http URL: %v", urls)
	}
	if !containsStr(urls, "https://10.42.156.80:443") {
		t.Errorf("port 443 did not map to https: %v", urls)
	}

	// An installation that exposes nothing must still appear, or it reads as
	// "not deployed".
	none := findSvc(t, got, "Thing", "none")
	if none.URL != "" || !strings.Contains(none.Detail, "nothing is exposed") {
		t.Errorf("bare installation row is wrong: %+v", none)
	}
}

func TestParseAppServicesNodePort(t *testing.T) {
	ds := svcDeploys()
	out := strings.Join([]string{
		"AIDT_NODE lab 10.42.156.24",
		"AIDT_BEGIN lab · ai/openwebui",
		"AIDT_SVC openwebui|NodePort|||10.152.183.5|8080:31234,",
		"AIDT_END",
	}, "\n")
	got := parseAppServices(out, ds)
	s := findSvc(t, got, "Open WebUI", "NodePort")
	// The URL must use the node address and the *node* port, not the service
	// port — using the service port would produce a link that never connects.
	if s.URL != "http://10.42.156.24:31234" {
		t.Errorf("NodePort URL = %q, want http://10.42.156.24:31234", s.URL)
	}
}

func TestParseAppServicesNodePortWithoutNodeAddress(t *testing.T) {
	ds := svcDeploys()
	out := strings.Join([]string{
		"AIDT_BEGIN lab · ai/openwebui",
		"AIDT_SVC openwebui|NodePort|||10.152.183.5|8080:31234,",
		"AIDT_END",
	}, "\n")
	got := parseAppServices(out, ds)
	s := findSvc(t, got, "Open WebUI", "NodePort")
	if s.URL != "" {
		t.Errorf("invented a URL with no node address: %q", s.URL)
	}
	if !strings.Contains(s.Detail, "no node address") {
		t.Errorf("unhelpful detail: %q", s.Detail)
	}
}

func TestParseAppServicesClusterIPGivesPortForward(t *testing.T) {
	ds := svcDeploys()
	out := strings.Join([]string{
		"AIDT_BEGIN lab · ai/openwebui",
		"AIDT_SVC openwebui|ClusterIP|||10.152.183.5|8080:0,",
		"AIDT_END",
	}, "\n")
	got := parseAppServices(out, ds)
	s := findSvc(t, got, "Open WebUI", "ClusterIP")
	if s.URL != "" {
		t.Errorf("ClusterIP must not produce an external URL, got %q", s.URL)
	}
	// A dead link is worse than no link; the row should say how to reach it.
	if !strings.Contains(s.Detail, "port-forward") || !strings.Contains(s.Detail, "8080:8080") {
		t.Errorf("ClusterIP row lacks a usable port-forward hint: %q", s.Detail)
	}
}

func TestParseAppServicesPendingLoadBalancer(t *testing.T) {
	ds := svcDeploys()
	out := strings.Join([]string{
		"AIDT_BEGIN lab · ai/openwebui",
		"AIDT_SVC openwebui|LoadBalancer|||10.152.183.5|80:0,",
		"AIDT_END",
	}, "\n")
	got := parseAppServices(out, ds)
	s := findSvc(t, got, "Open WebUI", "LoadBalancer")
	if s.URL != "" {
		t.Errorf("pending LoadBalancer produced a URL: %q", s.URL)
	}
	if !strings.Contains(s.Detail, "pending") {
		t.Errorf("pending state not explained: %q", s.Detail)
	}
}

func TestParseAppServicesIngress(t *testing.T) {
	ds := svcDeploys()
	out := strings.Join([]string{
		"AIDT_BEGIN lab · ai/openwebui",
		"AIDT_ING openwebui|chat.example.com|/|openwebui-tls|10.42.156.80",
		"AIDT_END",
	}, "\n")
	got := parseAppServices(out, ds)
	s := findSvc(t, got, "Open WebUI", "Ingress")
	// TLS means https on 443, and a bare "/" path adds nothing to the link.
	if s.URL != "https://chat.example.com:443" {
		t.Errorf("ingress URL = %q", s.URL)
	}

	// Without TLS and with a real path.
	out2 := strings.Join([]string{
		"AIDT_BEGIN lab · ai/openwebui",
		"AIDT_ING openwebui|chat.example.com|/app||",
		"AIDT_END",
	}, "\n")
	s2 := findSvc(t, parseAppServices(out2, ds), "Open WebUI", "Ingress")
	if s2.URL != "http://chat.example.com:80/app" {
		t.Errorf("plain ingress URL = %q", s2.URL)
	}
}

func TestParseAppServicesIgnoresStrayLines(t *testing.T) {
	ds := svcDeploys()
	// A kubectl error for one cluster must not corrupt another's rows, and
	// records outside a BEGIN/END block must be ignored rather than attributed
	// to whatever was parsed last.
	out := strings.Join([]string{
		"AIDT_SVC orphan|LoadBalancer|1.2.3.4||10.0.0.1|80:0,",
		"error: You must be logged in to the server (Unauthorized)",
		"AIDT_BEGIN lab · ai/openwebui",
		"AIDT_SVC openwebui|LoadBalancer|10.42.156.80||10.152.183.5|80:0,",
		"AIDT_END",
		"AIDT_SVC another-orphan|LoadBalancer|9.9.9.9||10.0.0.2|80:0,",
	}, "\n")
	got := parseAppServices(out, ds)
	for _, s := range got {
		if strings.Contains(s.Name, "orphan") {
			t.Errorf("a record outside a BEGIN/END block was attributed to an app: %+v", s)
		}
	}
	if s := findSvc(t, got, "Open WebUI", "LoadBalancer"); s.URL != "http://10.42.156.80:80" {
		t.Errorf("valid row lost: %q", s.URL)
	}
}

func TestParseAppServicesReachableSortedFirst(t *testing.T) {
	ds := []appDeployment{{App: "A", Context: "lab", Namespace: "ai", Release: "a", Kind: appKindHelm}}
	out := strings.Join([]string{
		"AIDT_BEGIN lab · ai/a",
		"AIDT_SVC a-internal|ClusterIP|||10.0.0.1|8080:0,",
		"AIDT_SVC a-public|LoadBalancer|10.42.156.80||10.0.0.2|80:0,",
		"AIDT_END",
	}, "\n")
	got := parseAppServices(out, ds)
	if len(got) < 2 {
		t.Fatalf("got %d rows, want 2+", len(got))
	}
	if !got[0].reachable() {
		t.Errorf("an unreachable row sorted above a reachable one: %+v", got)
	}
}

func TestAppServicesScriptScopesByKind(t *testing.T) {
	got := appServicesScript(svcDeploys())
	// A helm release is scoped by its instance label when it has one...
	if !strings.Contains(got, "'app.kubernetes.io/instance=openwebui'") {
		t.Errorf("helm install not scoped by release label:\n%s", got)
	}
	// ...and a manifest install gets no selector, because it never carries one.
	if strings.Contains(got, "app.kubernetes.io/instance=thing") {
		t.Errorf("manifest install was scoped by a label it does not have:\n%s", got)
	}
	// The node address is needed once per cluster, not once per installation.
	if n := strings.Count(got, "AIDT_NODE"); n != 1 {
		t.Errorf("node address resolved %d times for one cluster, want 1", n)
	}
	// The selector must be a fallback, not a filter: a chart that omits the
	// instance label (podinfo does) would otherwise report nothing exposed.
	if !strings.Contains(got, `if [ -z "$out" ]; then`) {
		t.Errorf("no unlabelled fallback query:\n%s", got)
	}
}

func TestBuildURLAndSchemeEdgeCases(t *testing.T) {
	if got := buildURL("http", "", 80, ""); got != "" {
		t.Errorf("empty host produced %q", got)
	}
	if got := buildURL("http", "10.0.0.1", 0, ""); got != "" {
		t.Errorf("zero port produced %q", got)
	}
	if got := buildURL("http", "10.0.0.1", 99999, ""); got != "" {
		t.Errorf("out-of-range port produced %q", got)
	}
	// IPv6 must not end up with unbracketed host:port.
	if got := buildURL("http", "fd00::1", 80, ""); !strings.Contains(got, "[fd00::1]:80") {
		t.Errorf("IPv6 host not bracketed: %q", got)
	}
	if got := buildURL("http", "10.0.0.1", 80, "app"); got != "http://10.0.0.1:80/app" {
		t.Errorf("path not normalised: %q", got)
	}
	for _, p := range []int{443, 8443, 9443} {
		if schemeForPort(p) != "https" {
			t.Errorf("port %d should be https", p)
		}
	}
	if schemeForPort(8080) != "http" {
		t.Error("port 8080 should be http")
	}
}

func TestPortPairs(t *testing.T) {
	got := portPairs("80:31234,443:0,")
	if len(got) != 2 || got[0] != [2]int{80, 31234} || got[1] != [2]int{443, 0} {
		t.Errorf("portPairs = %v", got)
	}
	if len(portPairs("")) != 0 {
		t.Error("empty field produced pairs")
	}
	if len(portPairs("junk,")) != 0 {
		t.Error("unparseable field produced pairs")
	}
}
