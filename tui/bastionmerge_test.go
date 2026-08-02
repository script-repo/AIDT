package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These exercise the real merge script against a real kubectl, because the bug
// it fixes (a stale certificate-authority path in the operator's existing
// kubeconfig aborting the whole merge) is invisible to string assertions.

const incomingCluster = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTiBDRVJU
    server: https://10.42.156.21:16443
  name: aidt-microk8s-01-cluster
contexts:
- context:
    cluster: aidt-microk8s-01-cluster
    user: aidt-microk8s-01-admin
  name: aidt-microk8s-01
current-context: aidt-microk8s-01
kind: Config
users:
- name: aidt-microk8s-01-admin
  user:
    token: NEWTOKEN
`

// brokenExistingConfig points at a certificate file that does not exist, which
// is what the gateway actually had.
const brokenExistingConfig = `apiVersion: v1
clusters:
- cluster:
    certificate-authority: /nonexistent/certs/stale-ca.crt
    server: https://10.38.48.141:6443
  name: nke-cluster
contexts:
- context:
    cluster: nke-cluster
    user: nke-admin
  name: nke
current-context: nke
kind: Config
users:
- name: nke-admin
  user:
    token: OLDTOKEN
`

const healthyExistingConfig = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTiBPVEhFUg==
    server: https://10.38.48.141:6443
  name: other-cluster
contexts:
- context:
    cluster: other-cluster
    user: other-admin
  name: other
current-context: other
kind: Config
users:
- name: other-admin
  user:
    token: OLDTOKEN
`

// runMergeScript runs the bastion merge script in an isolated HOME.
func runMergeScript(t *testing.T, existing string) (home, out string, err error) {
	t.Helper()
	bash, lookErr := exec.LookPath("bash")
	if lookErr != nil {
		t.Skip("bash unavailable")
	}
	if _, lookErr := exec.LookPath("kubectl"); lookErr != nil {
		t.Skip("kubectl unavailable; the merge cannot be exercised for real")
	}

	home = t.TempDir()
	if existing != "" {
		if err := os.MkdirAll(filepath.Join(home, ".kube"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".kube", "config"), []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(t.TempDir(), "merge.sh")
	if err := os.WriteFile(script, []byte(bastionMergeScript), 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bash, script, "aidt-microk8s-01")
	cmd.Stdin = strings.NewReader(incomingCluster)
	// A hostile KUBECONFIG on purpose: operators commonly have one exported, and
	// the script must still target ~/.kube/config rather than following it and
	// writing to a file nobody backed up.
	decoy := filepath.Join(t.TempDir(), "decoy.conf")
	if err := os.WriteFile(decoy, []byte(healthyExistingConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(os.Environ(), "HOME="+home, "KUBECONFIG="+decoy)
	b, err := cmd.CombinedOutput()

	// The decoy must be untouched: following it would have rewritten it.
	after, readErr := os.ReadFile(decoy)
	if readErr != nil {
		t.Fatalf("reading decoy kubeconfig: %v", readErr)
	}
	if string(after) != healthyExistingConfig {
		t.Errorf("an inherited KUBECONFIG was modified by the merge")
	}
	return home, string(b), err
}

func contextsIn(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("kubectl", "--kubeconfig="+path, "config", "get-contexts", "-o", "name").CombinedOutput()
	if err != nil {
		t.Fatalf("reading contexts from %s: %v\n%s", path, err, out)
	}
	return string(out)
}

// The reported failure: a stale cert path in the existing kubeconfig made
// --flatten abort, so the new cluster was never added at all.
func TestMergeSurvivesExistingConfigWithMissingCertFile(t *testing.T) {
	home, out, err := runMergeScript(t, brokenExistingConfig)
	if err != nil {
		t.Fatalf("merge failed on a repairable config: %v\n%s", err, out)
	}
	if !strings.Contains(out, "AIDT_BASTION_MERGED") {
		t.Errorf("expected a merge, got:\n%s", out)
	}
	if !strings.Contains(out, "references a missing file") {
		t.Errorf("the fallback should say why it could not inline:\n%s", out)
	}

	cfg := filepath.Join(home, ".kube", "config")
	got := contextsIn(t, cfg)
	for _, want := range []string{"aidt-microk8s-01", "nke"} {
		if !strings.Contains(got, want) {
			t.Errorf("context %q missing after merge; have: %s", want, got)
		}
	}
	// Credentials on both sides must survive; a plain "config view" would have
	// redacted them into an unusable file.
	body, _ := os.ReadFile(cfg)
	for _, want := range []string{"NEWTOKEN", "OLDTOKEN"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("%s was lost in the merge", want)
		}
	}
	if strings.Contains(string(body), "DATA+OMITTED") || strings.Contains(string(body), "REDACTED") {
		t.Error("merged kubeconfig contains redacted placeholders")
	}
	// The operator's original file must be recoverable.
	if _, statErr := os.Stat(filepath.Join(home, ".kube", "config.aidt-bak")); statErr != nil {
		t.Error("no backup of the previous kubeconfig was taken")
	}
}

func TestMergeIntoHealthyConfigKeepsBothClusters(t *testing.T) {
	home, out, err := runMergeScript(t, healthyExistingConfig)
	if err != nil {
		t.Fatalf("merge failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "AIDT_BASTION_MERGED") {
		t.Errorf("expected a merge, got:\n%s", out)
	}
	got := contextsIn(t, filepath.Join(home, ".kube", "config"))
	for _, want := range []string{"aidt-microk8s-01", "other"} {
		if !strings.Contains(got, want) {
			t.Errorf("context %q missing; have: %s", want, got)
		}
	}
}

func TestMergeWithNoExistingConfigInstallsCluster(t *testing.T) {
	home, out, err := runMergeScript(t, "")
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "AIDT_BASTION_MERGED") {
		t.Errorf("expected a merge marker, got:\n%s", out)
	}
	if got := contextsIn(t, filepath.Join(home, ".kube", "config")); !strings.Contains(got, "aidt-microk8s-01") {
		t.Errorf("cluster not installed; contexts: %s", got)
	}
}

// However the merge goes, a standalone copy is always left behind so the
// cluster is reachable even if the default kubeconfig is unusable.
func TestStandaloneKubeconfigIsAlwaysWritten(t *testing.T) {
	home, out, err := runMergeScript(t, brokenExistingConfig)
	if err != nil {
		t.Fatalf("merge failed: %v\n%s", err, out)
	}
	standalone := filepath.Join(home, ".kube", "aidt-aidt-microk8s-01.conf")
	info, statErr := os.Stat(standalone)
	if statErr != nil {
		t.Fatalf("no standalone kubeconfig: %v", statErr)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("standalone kubeconfig mode = %o, want 600", mode)
	}
	if got := contextsIn(t, standalone); !strings.Contains(got, "aidt-microk8s-01") {
		t.Errorf("standalone kubeconfig is unusable; contexts: %s", got)
	}
}

func TestParseBastionOutcome(t *testing.T) {
	if got := parseBastionOutcome("[bastion] ok\nAIDT_BASTION_MERGED aidt-microk8s-01\n"); got != "" {
		t.Errorf("a merged run should report no standalone path, got %q", got)
	}
	got := parseBastionOutcome("noise\nAIDT_BASTION_STANDALONE /home/rocky/.kube/aidt-k.conf\nmore\n")
	if got != "/home/rocky/.kube/aidt-k.conf" {
		t.Errorf("standalone path = %q", got)
	}
	if got := parseBastionOutcome("nothing to see"); got != "" {
		t.Errorf("unexpected path %q", got)
	}
}
