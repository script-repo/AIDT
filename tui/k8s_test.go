package main

import (
	"strings"
	"testing"
)

const sampleKubeconfig = `apiVersion: v1
kind: Config
current-context: lab
clusters:
- name: lab-cluster
  cluster:
    server: https://10.0.0.5:16443
    certificate-authority-data: DATA+OMITTED
- name: prod-cluster
  cluster:
    server: https://10.0.0.9:6443
contexts:
- name: lab
  context:
    cluster: lab-cluster
    user: lab-admin
- name: prod
  context:
    cluster: prod-cluster
    user: prod-admin
    namespace: team
users:
- name: lab-admin
  user:
    client-certificate-data: REDACTED
- name: prod-admin
  user:
    token: REDACTED
`

func TestParseKubeContexts(t *testing.T) {
	got, err := parseKubeContexts(sampleKubeconfig)
	if err != nil {
		t.Fatalf("parseKubeContexts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parsed %d contexts, want 2", len(got))
	}
	// Sorted by name, so lab comes first.
	if got[0].Name != "lab" || got[1].Name != "prod" {
		t.Fatalf("contexts not sorted by name: %v", got)
	}
	if got[0].Server != "https://10.0.0.5:16443" {
		t.Errorf("lab server = %q", got[0].Server)
	}
	if !got[0].Current {
		t.Error("current-context was not flagged")
	}
	if got[1].Current {
		t.Error("a non-current context was flagged as current")
	}
	if got[1].Namespace != "team" {
		t.Errorf("prod namespace = %q, want team", got[1].Namespace)
	}
	if got[1].User != "prod-admin" {
		t.Errorf("prod user = %q", got[1].User)
	}
}

func TestParseKubeContextsEdgeCases(t *testing.T) {
	if _, err := parseKubeContexts("   "); err == nil {
		t.Error("empty kubeconfig was accepted")
	}
	if _, err := parseKubeContexts("\t- not: [valid"); err == nil {
		t.Error("malformed YAML was accepted")
	}
	// A context whose cluster is missing still has to list: it is visible to
	// kubectl, so hiding it here would misrepresent the gateway.
	orphan := `apiVersion: v1
kind: Config
contexts:
- name: orphan
  context:
    cluster: gone
    user: someone
`
	got, err := parseKubeContexts(orphan)
	if err != nil {
		t.Fatalf("parseKubeContexts: %v", err)
	}
	if len(got) != 1 || got[0].Name != "orphan" || got[0].Server != "" {
		t.Errorf("orphan context mishandled: %v", got)
	}
}

func TestDeleteContextScriptPrunesOnlyUnsharedEntries(t *testing.T) {
	all := []k8sContext{
		{Name: "a", Cluster: "shared-cluster", User: "shared-user"},
		{Name: "b", Cluster: "shared-cluster", User: "own-user"},
		{Name: "c", Cluster: "own-cluster", User: "shared-user"},
	}

	// Deleting "b": its cluster is shared with "a", its user is its own.
	got := deleteContextScript(all[1], all)
	if !strings.Contains(got, "delete-context 'b'") {
		t.Errorf("context not deleted:\n%s", got)
	}
	if strings.Contains(got, "delete-cluster 'shared-cluster'") {
		t.Errorf("a cluster still used by another context was deleted:\n%s", got)
	}
	if !strings.Contains(got, "delete-user 'own-user'") {
		t.Errorf("an unreferenced user was not pruned:\n%s", got)
	}

	// Deleting "c": its cluster is exclusive, its user is shared.
	got = deleteContextScript(all[2], all)
	if !strings.Contains(got, "delete-cluster 'own-cluster'") {
		t.Errorf("an unreferenced cluster was not pruned:\n%s", got)
	}
	if strings.Contains(got, "delete-user 'shared-user'") {
		t.Errorf("a user still used by another context was deleted:\n%s", got)
	}

	// The config is rewritten, so it must be backed up first.
	if !strings.Contains(got, "config.aidt-bak") {
		t.Errorf("kubeconfig was not backed up before mutation:\n%s", got)
	}
}

func TestDeleteContextScriptSoleContext(t *testing.T) {
	only := []k8sContext{{Name: "solo", Cluster: "solo-cluster", User: "solo-admin"}}
	got := deleteContextScript(only[0], only)
	for _, want := range []string{
		"delete-context 'solo'", "delete-cluster 'solo-cluster'", "delete-user 'solo-admin'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q\n%s", want, got)
		}
	}
	// The standalone copy the bastion flow leaves behind is a credential file
	// for a cluster we just stopped tracking.
	if !strings.Contains(got, "aidt-solo.conf") {
		t.Errorf("standalone kubeconfig not cleaned up:\n%s", got)
	}
}

func TestRepoSlugIsStableAndDistinct(t *testing.T) {
	a := repoSlug("https://charts.example.com")
	if a != repoSlug("http://charts.example.com") {
		t.Error("scheme changed the repo alias")
	}
	if a == repoSlug("https://other.example.com") {
		t.Error("two repositories share an alias")
	}
	if repoSlug("") == "" {
		t.Error("empty repo produced an empty alias")
	}
}

func TestRefreshK8sListCountsApps(t *testing.T) {
	m := testAppModel(t)
	m.k8sContexts = []k8sContext{{Name: "prod", Cluster: "c", Server: "https://s", Current: true}, {Name: "lab"}}
	m.appDeploys = []appDeployment{
		{App: "Web", Context: "prod", Namespace: "ai", Release: "web"},
		{App: "DB", Context: "prod", Namespace: "data", Release: "db"},
	}
	m.refreshK8sList()

	items := m.k8sList.Items()
	// The "+ add cluster" row plus the two contexts.
	if len(items) != 3 {
		t.Fatalf("rendered %d rows, want 3", len(items))
	}
	if add, ok := items[0].(k8sItem); !ok || !add.add {
		t.Fatal("first row is not the add action")
	}
	prod, ok := items[1].(k8sItem)
	if !ok {
		t.Fatal("row is not a k8sItem")
	}
	if prod.apps != 2 {
		t.Errorf("prod shows %d apps, want 2", prod.apps)
	}
	if !prod.current || prod.stateColor() != colGreen {
		t.Error("the current context is not highlighted")
	}
	lab, _ := items[2].(k8sItem)
	if lab.apps != 0 {
		t.Errorf("lab shows %d apps, want 0", lab.apps)
	}
}

func TestAppReconcileScriptCoversEveryInstall(t *testing.T) {
	ds := []appDeployment{
		{App: "Web", Context: "prod", Namespace: "ai", Release: "web", Kind: appKindHelm},
		{App: "Thing", Context: "lab", Namespace: "ops", Release: "thing", Kind: appKindManifest},
	}
	got := appReconcileScript(ds)
	if !strings.Contains(got, "helm status 'web'") {
		t.Errorf("helm install not probed with helm status:\n%s", got)
	}
	// A manifest install has no release to query, so it must not be probed as
	// one — that would report every manifest app as missing.
	if strings.Contains(got, "helm status 'thing'") {
		t.Errorf("manifest install probed as a helm release:\n%s", got)
	}
	for _, d := range ds {
		if !strings.Contains(got, d.label()) {
			t.Errorf("install %q not probed:\n%s", d.label(), got)
		}
	}
}

func TestImportKubeconfigScriptQuotesPath(t *testing.T) {
	got := importKubeconfigScript("/tmp/it's here.conf", "lab")
	if strings.Contains(got, "/tmp/it's here.conf'") && !strings.Contains(got, `it'\''s`) {
		t.Errorf("path with a quote was not escaped:\n%s", got)
	}
	if !strings.Contains(got, "'lab'") {
		t.Errorf("context name missing:\n%s", got)
	}
}
