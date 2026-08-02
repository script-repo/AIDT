package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMicroK8sIsABuiltinBareURL(t *testing.T) {
	var found *customDeploy
	for i, c := range builtinCustomDeploys() {
		if c.Name == "MicroK8s" {
			found = &builtinCustomDeploys()[i]
		}
	}
	if found == nil {
		t.Fatal("MicroK8s is not in the built-in deployment list")
	}
	// A bare URL matters: both deploy paths download a bare URL to a file and
	// run the file, which keeps stdin free for the installer's python heredoc.
	// A `curl … | bash` pipeline would hand the script's own text to python.
	if !isBareHTTPURL(found.ScriptURL) {
		t.Errorf("MicroK8s ScriptURL must be a bare URL, got %q", found.ScriptURL)
	}
	// MicroK8s exposes a Kubernetes API, not a web UI. A Scheme/Port here would
	// both advertise a link that opens nothing and push the value through the
	// 8443-8543 worker port allocator, which rejects 16443.
	if found.Scheme != "" || found.Port != "" {
		t.Errorf("MicroK8s should not declare a scheme/port, got %q/%q", found.Scheme, found.Port)
	}
	if _, err := m8sPort(found.Port); err != nil {
		t.Errorf("empty port should be accepted: %v", err)
	}
}

// m8sPort mirrors allocateServicePort's contract for an empty preference.
func m8sPort(preferred string) (string, error) {
	m := &model{}
	return m.allocateServicePort("MicroK8s", "10.0.0.5", preferred)
}

func TestMicroK8sInstallerShipsInRepo(t *testing.T) {
	path := filepath.Join("..", "microk8s-install.sh")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("installer missing at repo root (the built-in URL serves this file): %v", err)
	}
	script := string(b)
	for _, want := range []string{
		"AIDT_SERVICE_INFO",   // publishes itself into Services
		"AIDT_METALLB_RANGE",  // operator override
		"microk8s enable dns", // a cluster without dns is not usable
		"metallb:",
		"snap install helm --classic",
		"microk8s config",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("microk8s-install.sh missing %q", want)
		}
	}
	// MicroK8s is a snap; refusing non-Ubuntu early beats failing halfway.
	if !strings.Contains(script, `[ "${ID:-}" = "ubuntu" ]`) {
		t.Error("installer does not guard against non-Ubuntu guests")
	}
	// The URL in the catalog must actually point at this file.
	if !strings.HasSuffix(microk8sInstall, "/microk8s-install.sh") {
		t.Errorf("built-in URL %q does not name microk8s-install.sh", microk8sInstall)
	}
}

func TestParseServiceInfo(t *testing.T) {
	line := `AIDT_SERVICE_INFO {"url":"https://10.0.0.5:16443","detail":"cluster 10.0.0.5 · MetalLB pool 10.0.0.81-10.0.0.85"}`
	got, ok := parseServiceInfo(line)
	if !ok {
		t.Fatal("valid service info was not parsed")
	}
	if got.URL != "https://10.0.0.5:16443" {
		t.Errorf("url = %q", got.URL)
	}
	if !strings.Contains(got.Detail, "MetalLB pool 10.0.0.81-10.0.0.85") {
		t.Errorf("detail = %q", got.Detail)
	}
	if !validServiceURL(got.URL) {
		t.Error("reported URL must be renderable as a service link")
	}

	// Junk must not fail a deployment that otherwise succeeded.
	for _, bad := range []string{
		"just a log line",
		"AIDT_SERVICE_INFO not json",
		`AIDT_SERVICE_INFO {"url":"","detail":""}`,
	} {
		if _, ok := parseServiceInfo(bad); ok {
			t.Errorf("expected %q to be ignored", bad)
		}
	}
}

func TestServiceDetailReachesTheServicesMenu(t *testing.T) {
	m := newModel("", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.recordCustomService(customRun{
		cfg:    customDeploy{Name: "MicroK8s"},
		target: "microk8s-01",
		url:    "https://10.0.0.5:16443",
		detail: "cluster 10.0.0.5 · MetalLB pool 10.0.0.81-10.0.0.85",
	})

	var row serviceItem
	for _, it := range m.servicesList.Items() {
		if si, ok := it.(serviceItem); ok && si.name == "MicroK8s" {
			row = si
		}
	}
	if row.name == "" {
		t.Fatal("MicroK8s did not appear in Services")
	}
	desc := row.Description()
	for _, want := range []string{"10.0.0.5:16443", "MetalLB pool 10.0.0.81-10.0.0.85", "microk8s-01"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Services row %q missing %q", desc, want)
		}
	}
}

func TestSeedBuiltinsReachExistingInstallsButDeletesStick(t *testing.T) {
	// An install from before MicroKs existed: seeded, two built-ins, no ledger.
	existing := []customDeploy{
		{Name: "NP4M", ScriptURL: np4mInstall, Scheme: "https", Port: "8443"},
		{Name: "NRCC", ScriptURL: nrccInstall, Scheme: "https", Port: "8443"},
	}
	// Everything newer than NP4M/NRCC should arrive, whatever that set is now.
	var wantNew []string
	for _, b := range builtinCustomDeploys() {
		if b.ScriptURL != np4mInstall && b.ScriptURL != nrccInstall {
			wantNew = append(wantNew, b.Name)
		}
	}
	if len(wantNew) == 0 {
		t.Skip("no built-ins beyond the original two")
	}

	out, ledger, changed := seedBuiltinCustomDeploys(existing, nil, true)
	if !changed {
		t.Fatal("newer built-ins should be added to an existing install")
	}
	if len(out) != len(existing)+len(wantNew) {
		t.Fatalf("expected %d entries, got %+v", len(existing)+len(wantNew), out)
	}
	for _, name := range wantNew {
		var seen bool
		for _, c := range out {
			if c.Name == name {
				seen = true
			}
		}
		if !seen {
			t.Errorf("built-in %q was not added to an existing install", name)
		}
	}

	// Deleting them must stick: the ledger remembers they were already offered.
	afterDelete := out[:len(existing)]
	out2, _, changed2 := seedBuiltinCustomDeploys(afterDelete, ledger, true)
	if changed2 {
		t.Error("a deleted built-in was re-added on the next launch")
	}
	if len(out2) != len(existing) {
		t.Errorf("expected the delete to persist, got %+v", out2)
	}

	// A user who deleted the older built-ins before the ledger existed must not
	// have those resurrected — only the genuinely new ones arrive.
	out3, _, _ := seedBuiltinCustomDeploys(nil, nil, true)
	if len(out3) != len(wantNew) {
		t.Errorf("legacy install should only gain the newer built-ins, got %+v", out3)
	}

	// A brand-new install gets everything exactly once.
	fresh, ledger4, _ := seedBuiltinCustomDeploys(nil, nil, false)
	if len(fresh) != len(builtinCustomDeploys()) {
		t.Errorf("fresh install should seed every built-in, got %+v", fresh)
	}
	if _, _, again := seedBuiltinCustomDeploys(fresh, ledger4, true); again {
		t.Error("seeding is not idempotent")
	}
}
