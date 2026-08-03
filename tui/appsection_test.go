package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestNumberShortcutsKeepHistoricalSections guards the quick-jump contract.
//
// Adding a section used to renumber every section after it, silently moving
// shortcuts operators have in their fingers. The ten numbered slots are now
// pinned to the ten sections that predate App Deploy, so a new section must be
// appended rather than inserted.
func TestNumberShortcutsKeepHistoricalSections(t *testing.T) {
	want := []struct {
		key  string
		name string
	}{
		{"1", "Dashboard"}, {"2", "Pool"}, {"3", "Models"}, {"4", "Chat"},
		{"5", "Agents"}, {"6", "Load"}, {"7", "Nutanix"}, {"8", "Services"},
		{"9", "Access"}, {"0", "Update"},
	}
	if len(sections) < numberedSections {
		t.Fatalf("only %d sections, want at least %d", len(sections), numberedSections)
	}
	for _, c := range want {
		m := newModel("http://10.0.0.1:40114", "rocky", "pw")
		got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(c.key)})
		gm, ok := got.(model)
		if !ok {
			t.Fatalf("handleKey returned %T", got)
		}
		if int(gm.section) >= len(sections) {
			t.Fatalf("key %q selected section %d, out of range", c.key, gm.section)
		}
		if name := sections[gm.section].name; name != c.name {
			t.Errorf("key %q opened %q, want %q", c.key, name, c.name)
		}
	}
}

// TestNewSectionsAreReachableAndWired checks the two new sections have a list,
// a view, and key handling — the three things a section needs to work.
func TestNewSectionsAreReachableAndWired(t *testing.T) {
	for _, sec := range []section{secApps, secK8s} {
		m := newModel("http://10.0.0.1:40114", "rocky", "pw")
		m.applyLayout(120, 40)
		m.section = sec
		m.zone = zoneContent

		if m.activeList() == nil {
			t.Errorf("section %q has no active list", sections[sec].name)
		}
		if got := strings.TrimSpace(m.viewContent()); got == "" {
			t.Errorf("section %q rendered nothing", sections[sec].name)
		}
		// esc must return to the sidebar rather than falling through to the
		// default branch and doing nothing.
		got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
		if gm, ok := got.(model); !ok || gm.zone != zoneSidebar {
			t.Errorf("esc did not leave content in section %q", sections[sec].name)
		}
	}
}

// TestAppsSectionRefusesToDeployWithoutPrerequisites checks the guard rails: a
// deploy needs a gateway to run on and a cluster to target, and saying which is
// missing beats opening a form that cannot produce a working command.
func TestAppsSectionRefusesToDeployWithoutPrerequisites(t *testing.T) {
	// No gateway at all.
	m := newModel("", "rocky", "")
	m.section = secApps
	m.appsList.Select(1) // skip the "+ add application" row
	if cmd := m.deploySelectedApp(); cmd != nil {
		t.Error("deploy proceeded with no gateway configured")
	}
	if m.notice == "" {
		t.Error("no explanation given for the refused deploy")
	}

	// Gateway present, but no clusters known yet.
	m2 := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m2.section = secApps
	m2.k8sContexts = nil
	m2.appsList.Select(1)
	if cmd := m2.deploySelectedApp(); cmd != nil {
		t.Error("deploy proceeded with no clusters known")
	}
	if !strings.Contains(m2.notice, "K8S") {
		t.Errorf("notice does not point at the K8S section: %q", m2.notice)
	}
}

// TestRemoveAppRequiresAnInstallation makes sure x on an undeployed app is a
// no-op with an explanation rather than an empty picker.
func TestRemoveAppRequiresAnInstallation(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.section = secApps
	m.k8sContexts = []k8sContext{{Name: "prod"}}
	m.appsList.Select(1)
	if cmd := m.removeSelectedApp(); cmd != nil {
		t.Error("remove opened a picker for an app that is not deployed")
	}
	if !strings.Contains(m.notice, "not deployed") {
		t.Errorf("unhelpful notice: %q", m.notice)
	}
}

// TestBuiltinCatalogIsWellFormed catches a typo in the seeded catalog before it
// reaches an operator as a deploy that cannot resolve its chart.
func TestBuiltinCatalogIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range builtinApps() {
		if a.Name == "" {
			t.Error("a built-in app has no name")
		}
		if a.kind() == "" {
			t.Errorf("%s has neither a chart nor a manifest URL", a.Name)
		}
		if a.kind() == appKindHelm && !a.isOCI() && a.Repo == "" {
			t.Errorf("%s is a non-OCI chart with no repository", a.Name)
		}
		// Bitnami's public catalog was deleted in 2025; those charts install and
		// then fail to pull their images.
		if strings.Contains(a.Repo, "charts.bitnami.com") {
			t.Errorf("%s uses the retired Bitnami chart repository", a.Name)
		}
		if seen[a.sourceKey()] {
			t.Errorf("%s duplicates an existing source key %q", a.Name, a.sourceKey())
		}
		seen[a.sourceKey()] = true

		// Every built-in must produce a runnable script.
		d := appDeployment{App: a.Name, Context: "c", Namespace: a.defaultNamespace(), Release: "r", Kind: a.kind()}
		if _, err := appDeployScript(a, d, nil, ollaTarget{}); err != nil {
			t.Errorf("%s does not produce a deploy script: %v", a.Name, err)
		}
		if _, err := appRemoveScript(a, d); err != nil {
			t.Errorf("%s does not produce a remove script: %v", a.Name, err)
		}
	}
}
