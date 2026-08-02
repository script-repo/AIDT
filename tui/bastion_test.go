package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// microk8sConfigSample mirrors what `microk8s config` emits: the cluster, user,
// and context names are fixed strings, which is exactly why they must be
// rewritten before merging.
const microk8sConfigSample = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTiBDRVJU
    server: https://127.0.0.1:16443
  name: microk8s-cluster
contexts:
- context:
    cluster: microk8s-cluster
    user: admin
  name: microk8s
current-context: microk8s
kind: Config
preferences: {}
users:
- name: admin
  user:
    token: SUPERSECRETTOKEN
`

func parseKubeconfig(t *testing.T, raw string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("result is not valid YAML: %v", err)
	}
	return doc
}

func TestRetargetKubeconfigRenamesAndRepointsServer(t *testing.T) {
	out, err := retargetKubeconfig(microk8sConfigSample, "microk8s-01", "https://10.0.0.5:16443")
	if err != nil {
		t.Fatalf("retarget: %v", err)
	}
	doc := parseKubeconfig(t, out)

	if doc["current-context"] != "microk8s-01" {
		t.Errorf("current-context = %v", doc["current-context"])
	}
	cluster := doc["clusters"].([]any)[0].(map[string]any)
	if cluster["name"] != "microk8s-01-cluster" {
		t.Errorf("cluster name = %v", cluster["name"])
	}
	// microk8s can emit a loopback server, which is useless from the bastion.
	if got := cluster["cluster"].(map[string]any)["server"]; got != "https://10.0.0.5:16443" {
		t.Errorf("server = %v, want the node's routable address", got)
	}
	user := doc["users"].([]any)[0].(map[string]any)
	if user["name"] != "microk8s-01-admin" {
		t.Errorf("user name = %v", user["name"])
	}
	ctx := doc["contexts"].([]any)[0].(map[string]any)
	if ctx["name"] != "microk8s-01" {
		t.Errorf("context name = %v", ctx["name"])
	}
	inner := ctx["context"].(map[string]any)
	if inner["cluster"] != "microk8s-01-cluster" || inner["user"] != "microk8s-01-admin" {
		t.Errorf("context does not reference the renamed cluster/user: %+v", inner)
	}
	// Credentials must survive the round-trip intact.
	if !strings.Contains(out, "SUPERSECRETTOKEN") || !strings.Contains(out, "LS0tLS1CRUdJTiBDRVJU") {
		t.Error("credential material was lost in the rewrite")
	}
}

// Two clusters must not collide. Without renaming, both arrive as
// microk8s-cluster/admin/microk8s and the second silently replaces the first.
func TestRetargetKubeconfigKeepsTwoClustersDistinct(t *testing.T) {
	a, err := retargetKubeconfig(microk8sConfigSample, "k8s-a", "https://10.0.0.5:16443")
	if err != nil {
		t.Fatal(err)
	}
	b, err := retargetKubeconfig(microk8sConfigSample, "k8s-b", "https://10.0.0.6:16443")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"k8s-a", "k8s-a-cluster", "k8s-a-admin"} {
		if strings.Contains(b, name) {
			t.Errorf("second cluster reuses %q from the first", name)
		}
	}
	if strings.Contains(a, "10.0.0.6") {
		t.Error("clusters share a server address")
	}
	// The stock names must be gone entirely, since those are what collide.
	for _, stale := range []string{"name: microk8s-cluster", "name: admin", "current-context: microk8s\n"} {
		if strings.Contains(a, stale) {
			t.Errorf("stock name %q survived the rewrite", stale)
		}
	}
}

func TestRetargetKubeconfigRejectsGarbage(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":       "",
		"not yaml":    "\t\x00 not: [valid",
		"no clusters": "apiVersion: v1\nkind: Config\ncontexts: []\nusers: []\n",
		"no users":    "apiVersion: v1\nkind: Config\nclusters:\n- name: c\n",
	} {
		if _, err := retargetKubeconfig(raw, "x", "https://h:16443"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// The kubeconfig holds an admin token. It must reach the bastion over stdin,
// never as a command argument (visible in ps) and never in the TUI log.
func TestBastionNeverPutsCredentialsInArgumentsOrLog(t *testing.T) {
	if strings.Contains(bastionMergeScript, "$2") {
		t.Error("merge script takes more than the context name as an argument")
	}
	if !strings.Contains(bastionMergeScript, "cat > \"$INCOMING\"") {
		t.Error("merge script does not read the kubeconfig from stdin")
	}
	if !strings.Contains(bastionMergeScript, "umask 077") || !strings.Contains(bastionMergeScript, "chmod 600") {
		t.Error("merge script does not restrict kubeconfig permissions")
	}
	// bastionReadyMsg is what reaches the TUI; it must carry no kubeconfig.
	msg := bastionReadyMsg{host: "10.0.0.1", context: "k", log: "[bastion] kubectl ready"}
	if strings.Contains(msg.log, "token") || strings.Contains(msg.log, "certificate-authority-data") {
		t.Error("bastion log carries credential material")
	}
}

func TestBastionScriptsAreValidShell(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	for name, script := range map[string]string{
		"merge":           bastionMergeScript,
		"kubectl-install": "set -euo pipefail\n" + kubectlInstallFragment,
	} {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s script has invalid shell syntax: %v\n%s", name, err, out)
		}
	}
}

func TestKubectlInstallVerifiesChecksum(t *testing.T) {
	// SEC-04 in FINDINGS.md is about installing unverified remote binaries.
	// kubectl publishes a checksum, so there is no excuse for skipping it.
	if !strings.Contains(kubectlInstallFragment, "sha256sum -c -") {
		t.Error("kubectl install does not verify the published checksum")
	}
	if !strings.Contains(kubectlInstallFragment, "kubectl checksum mismatch") {
		t.Error("kubectl install does not fail closed on a checksum mismatch")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if !strings.Contains(kubectlInstallFragment, arch) {
			t.Errorf("kubectl install does not handle %s", arch)
		}
	}
}

func TestMicroK8sInstallerAddsStandaloneKubectl(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "microk8s-install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "snap install kubectl --classic") {
		t.Error("node installer does not add a standalone kubectl")
	}
}

// The bastion step must be skipped, not attempted, when there is nothing to
// wire it into — a failed lookup here would otherwise dial an empty host.
func TestBastionSkippedWithoutGatewayOrClusterAddress(t *testing.T) {
	m := newModel("", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	// newModel falls back to persisted settings, which on a developer machine
	// can supply a real gateway, so state the precondition explicitly rather
	// than assuming the constructor left it empty.
	m.gateway = ""
	run := customRun{cfg: customDeploy{Name: "MicroK8s", ScriptURL: microk8sInstall},
		target: "microk8s-01", url: "https://10.0.0.5:16443"}
	if cmd := m.microk8sBastionCmd(run); cmd != nil {
		t.Error("bastion attempted with no gateway configured")
	}

	m2 := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m2.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m2.gateway = "http://10.0.0.1:40114"
	if cmd := m2.microk8sBastionCmd(customRun{target: "x"}); cmd != nil {
		t.Error("bastion attempted with no reported cluster address")
	}
	// With both known, it should proceed.
	if cmd := m2.microk8sBastionCmd(run); cmd == nil {
		t.Error("bastion not attempted despite a gateway and a cluster address")
	}
}
