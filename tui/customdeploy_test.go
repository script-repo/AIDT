package main

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCustomAccessURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  customDeploy
		host string
		want string
	}{
		{"https", customDeploy{Scheme: "https", Port: "8443"}, "10.0.0.8", "https://10.0.0.8:8443"},
		{"path", customDeploy{Port: "8080", Path: "dashboard"}, "worker.local", "http://worker.local:8080/dashboard"},
		{"ipv6", customDeploy{Scheme: "https", Port: "8443"}, "2001:db8::8", "https://[2001:db8::8]:8443"},
		{"bad port", customDeploy{Port: "70000"}, "10.0.0.8", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.accessURL(tt.host); got != tt.want {
				t.Fatalf("accessURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuiltinCustomMigration(t *testing.T) {
	in := []customDeploy{
		{Name: "NP4M", ScriptURL: np4mInstall},
		{Name: "NRCC", ScriptURL: nrccLegacy},
		{Name: "NRCC", ScriptURL: "custom command"},
	}
	got, changed := migrateBuiltinCustomDeploys(in)
	if !changed {
		t.Fatal("expected built-in migration")
	}
	if got[0].Scheme != "https" || got[0].Port != "8443" {
		t.Fatalf("NP4M metadata not migrated: %+v", got[0])
	}
	if got[1].ScriptURL != nrccInstall || got[1].Port != "8443" {
		t.Fatalf("NRCC not migrated: %+v", got[1])
	}
	if got[2] != in[2] {
		t.Fatalf("user definition was modified: got %+v want %+v", got[2], in[2])
	}
}

func TestCustomSetupScript(t *testing.T) {
	fromURL := customSetupScript(customDeploy{ScriptURL: "https://example.com/setup.sh?x=1", Port: "8443"})
	for _, want := range []string{"set -euo pipefail", "curl -fsSL", "wget -qO", "sudo -n env", "AIDT_SERVICE_PORT=8443"} {
		if !strings.Contains(fromURL, want) {
			t.Errorf("URL setup script missing %q", want)
		}
	}
	command := "curl -fsSL https://example.com/install.sh | sudo bash"
	full := customSetupScript(customDeploy{ScriptURL: command, Port: "8443"})
	if !strings.Contains(full, command) || !strings.Contains(full, "pipefail") {
		t.Fatalf("full setup command not preserved: %q", full)
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		for name, script := range map[string]string{"url": fromURL, "command": full} {
			cmd := exec.Command(bash, "-n")
			cmd.Stdin = strings.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s setup script has invalid shell syntax: %v\n%s", name, err, out)
			}
		}
	}
}

func TestBuiltinCustomCommandsReceiveAssignedPort(t *testing.T) {
	np4m := customCommand(customDeploy{Name: "NP4M", ScriptURL: np4mInstall, Port: "8444"})
	if !strings.Contains(np4m, "sudo env NP4M_PORT=8444 bash") {
		t.Fatalf("NP4M command does not receive assigned port: %s", np4m)
	}
	for _, want := range []string{"python3-venv", `python${NP4M_PY_VER}-venv`, `-m venv "$NP4M_VENV_PROBE"`} {
		if !strings.Contains(np4m, want) {
			t.Errorf("NP4M command missing venv prerequisite %q", want)
		}
	}
	nrcc := customCommand(customDeploy{Name: "NRCC", ScriptURL: nrccInstall, Port: "8445"})
	if !strings.Contains(nrcc, "NRCC_NO_OPEN=1 NRCC_PORT=8445 bash") {
		t.Fatalf("NRCC command does not receive assigned port: %s", nrcc)
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(customSetupScript(customDeploy{Name: "NP4M", ScriptURL: np4mInstall, Port: "8444"}))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("NP4M setup has invalid shell syntax: %v\n%s", err, out)
		}
	}
}

func TestSuccessfulCustomDeployPersistsService(t *testing.T) {
	isolateHome(t)
	m := newModel("", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.pcCfg = nil
	m.pendingCustom = &customRun{
		cfg:    customDeploy{Name: "NP4M", Scheme: "https", Port: "8443"},
		target: "aidt-worker-01",
		url:    "https://10.0.0.8:8443",
	}
	next, _ := m.handleProc(ProcEvent{Done: true, Code: 0})
	m = next.(model)
	if len(m.services) != 1 || m.services[0].Name != "NP4M" {
		t.Fatalf("successful deploy did not record service: %+v", m.services)
	}
	if got := loadSettings(m.tokFile).Services; len(got) != 1 || got[0].URL != "https://10.0.0.8:8443" {
		t.Fatalf("service was not persisted: %+v", got)
	}
}

func TestFailedCustomDeployDoesNotPersistService(t *testing.T) {
	isolateHome(t)
	m := newModel("", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.pcCfg = nil
	m.pendingCustom = &customRun{
		cfg:    customDeploy{Name: "NP4M", Scheme: "https", Port: "8443"},
		target: "aidt-worker-01",
		url:    "https://10.0.0.8:8443",
	}
	next, _ := m.handleProc(ProcEvent{Done: true, Code: 1})
	m = next.(model)
	if len(m.services) != 0 || len(loadSettings(m.tokFile).Services) != 0 {
		t.Fatalf("failed deploy recorded a service: %+v", m.services)
	}
}

func TestServicesSectionCombinesLiveAndPersistedURLs(t *testing.T) {
	isolateHome(t)
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.endpoints = []endpointEntry{{Name: "aidt-worker-01", URL: "http://10.0.0.8:11434", Type: "ollama"}}
	m.services = []serviceLink{{Name: "NP4M", Target: "aidt-worker-01", URL: "https://10.0.0.8:8443"}}
	m.refreshServices()
	if got := len(m.servicesList.Items()); got != 3 {
		t.Fatalf("services list has %d items, want gateway + worker + custom", got)
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'8'}})
	if m.section != secServices || m.zone != zoneContent {
		t.Fatalf("8 did not open Services: section=%v zone=%v", m.section, m.zone)
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	if m.section != secUpdate {
		t.Fatalf("0 did not open Update: section=%v", m.section)
	}
}

func TestCustomWorkerPickerUsesRegisteredWorkers(t *testing.T) {
	isolateHome(t)
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.endpoints = []endpointEntry{
		{Name: "aidt-worker-01", URL: "http://10.0.0.8:11434", Type: "ollama"},
		{Name: "other-api", URL: "http://10.0.0.9:9000", Type: "openai"},
	}
	cmd := m.openCustomWorkerPick(customDeploy{Name: "NP4M", ScriptURL: np4mInstall, Scheme: "https", Port: "8443"})
	if cmd == nil || m.modal != modalCustomWorker || m.form == nil {
		t.Fatal("worker picker did not open")
	}
	if m.pendingCustom == nil || m.fCustHost != "10.0.0.8" {
		t.Fatalf("worker picker selected wrong target: pending=%+v host=%q", m.pendingCustom, m.fCustHost)
	}
}

func TestCustomServicePortsIncrementOnSharedWorker(t *testing.T) {
	m := model{services: []serviceLink{{
		Name: "NP4M", Target: "aidt-worker-01", URL: "https://10.0.0.8:8443",
	}}}
	port, err := m.allocateServicePort("NRCC", "10.0.0.8", "8443")
	if err != nil || port != "8444" {
		t.Fatalf("second service port = %q, %v; want 8444", port, err)
	}
	m.services = append(m.services, serviceLink{Name: "NRCC", Target: "aidt-worker-01", URL: "https://10.0.0.8:8444"})
	port, err = m.allocateServicePort("Other", "10.0.0.8", "8443")
	if err != nil || port != "8445" {
		t.Fatalf("third service port = %q, %v; want 8445", port, err)
	}
	port, err = m.allocateServicePort("NP4M", "10.0.0.8", "8443")
	if err != nil || port != "8443" {
		t.Fatalf("NP4M redeploy port = %q, %v; want 8443", port, err)
	}
}

func TestCustomServicePortRangeEndsAt8543(t *testing.T) {
	m := model{}
	for port := 8443; port <= 8543; port++ {
		m.services = append(m.services, serviceLink{
			Name: "service-" + strconv.Itoa(port), Target: "aidt-worker-01",
			URL: "https://10.0.0.8:" + strconv.Itoa(port),
		})
	}
	if _, err := m.allocateServicePort("overflow", "10.0.0.8", "8443"); err == nil {
		t.Fatal("expected allocation to fail after port 8543")
	}
}
