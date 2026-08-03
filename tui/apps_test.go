package main

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// testAppModel builds a fully initialised model whose persistence is redirected
// into a temp dir, so a test can exercise the save paths without writing to the
// operator's real ~/.ai-deployment-toolkit/tui.json.
func testAppModel(t *testing.T) *model {
	t.Helper()
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	return &m
}

func TestAppKindAndSourceKey(t *testing.T) {
	cases := []struct {
		name string
		app  k8sApp
		kind string
		oci  bool
	}{
		{"repo chart", k8sApp{Repo: "https://charts.example.com", Chart: "web"}, appKindHelm, false},
		{"oci chart", k8sApp{Chart: "oci://ghcr.io/org/chart"}, appKindHelm, true},
		{"manifest", k8sApp{ManifestURL: "https://example.com/app.yaml"}, appKindManifest, false},
		{"empty", k8sApp{}, "", false},
		// A definition carrying both is a Helm chart: the chart is the richer
		// install, and a stale manifest URL must not silently win.
		{"both", k8sApp{Chart: "web", Repo: "https://r", ManifestURL: "https://m.yaml"}, appKindHelm, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.app.kind(); got != c.kind {
				t.Errorf("kind() = %q, want %q", got, c.kind)
			}
			if got := c.app.isOCI(); got != c.oci {
				t.Errorf("isOCI() = %v, want %v", got, c.oci)
			}
		})
	}

	// The source key must distinguish two charts of the same name from
	// different repositories, or the seed ledger would conflate them.
	a := k8sApp{Repo: "https://a.example.com", Chart: "web"}
	b := k8sApp{Repo: "https://b.example.com", Chart: "web"}
	if a.sourceKey() == b.sourceKey() {
		t.Errorf("same-name charts from different repos share a source key: %q", a.sourceKey())
	}
	// A trailing slash on the repo is not a different source.
	if (k8sApp{Repo: "https://a.example.com/", Chart: "web"}).sourceKey() != a.sourceKey() {
		t.Error("trailing slash changed the source key")
	}
}

func TestSeedBuiltinApps(t *testing.T) {
	// Fresh install: everything is offered and recorded.
	apps, ledger, changed := seedBuiltinApps(nil, nil, false)
	if !changed {
		t.Fatal("fresh install reported no change")
	}
	if len(apps) != len(builtinApps()) {
		t.Fatalf("seeded %d apps, want %d", len(apps), len(builtinApps()))
	}
	if len(ledger) != len(builtinApps()) {
		t.Fatalf("ledger has %d entries, want %d", len(ledger), len(builtinApps()))
	}

	// Re-running is a no-op.
	apps2, _, changed2 := seedBuiltinApps(apps, ledger, false)
	if changed2 || len(apps2) != len(apps) {
		t.Error("re-seeding an already-seeded catalog changed it")
	}

	// A delete sticks: the ledger remembers the app was offered, so it is not
	// helpfully re-added on the next launch.
	pruned := apps[1:]
	after, _, changedAfter := seedBuiltinApps(pruned, ledger, false)
	if changedAfter || len(after) != len(pruned) {
		t.Errorf("deleted built-in came back: %d apps, want %d", len(after), len(pruned))
	}

	// A built-in added by a later release reaches an existing config once: it is
	// absent from both the catalog and the ledger, which is what distinguishes
	// it from the deleted-app case above.
	shortLedger := ledger[1:]
	topped, newLedger, toppedChanged := seedBuiltinApps(pruned, shortLedger, false)
	if !toppedChanged {
		t.Fatal("a built-in missing from both the catalog and the ledger was not added")
	}
	if len(topped) != len(pruned)+1 {
		t.Errorf("catalog has %d apps, want %d", len(topped), len(pruned)+1)
	}
	if len(newLedger) != len(ledger) {
		t.Errorf("ledger grew to %d, want %d", len(newLedger), len(ledger))
	}
	// …and only once: re-running with the updated ledger is now a no-op.
	if _, _, again := seedBuiltinApps(topped, newLedger, false); again {
		t.Error("the same built-in was offered twice")
	}
}

func TestSeedBuiltinAppsLegacyConfig(t *testing.T) {
	// A config written before the ledger existed has already been offered the
	// catalog. Treating it as fresh would resurrect every app the operator had
	// deleted, which is the bug the ledger exists to prevent.
	existing := []k8sApp{{Name: "Mine", Repo: "https://x", Chart: "mine"}}
	out, _, changed := seedBuiltinApps(existing, nil, true)
	if changed {
		t.Error("legacy config was re-seeded")
	}
	if len(out) != 1 {
		t.Errorf("legacy config gained %d apps", len(out)-1)
	}
}

func TestAppDeploymentRegistry(t *testing.T) {
	m := testAppModel(t)
	m.apps = []k8sApp{{Name: "Web", Repo: "https://r", Chart: "web"}}
	m.refreshAppsList()

	// The same app on two clusters is two installations, which is the core
	// multi-context requirement.
	d1 := appDeployment{App: "Web", Context: "prod", Namespace: "ai", Release: "web", Kind: appKindHelm}
	d2 := appDeployment{App: "Web", Context: "lab", Namespace: "ai", Release: "web", Kind: appKindHelm}
	m.recordAppDeployment(d1)
	m.recordAppDeployment(d2)
	if got := len(m.appDeploymentsFor("Web")); got != 2 {
		t.Fatalf("recorded %d installations, want 2", got)
	}
	if got := m.appContextsFor("Web"); len(got) != 2 {
		t.Fatalf("app spans %d contexts, want 2", len(got))
	}

	// Re-recording the same tuple updates rather than duplicates.
	m.recordAppDeployment(d1)
	if got := len(m.appDeploymentsFor("Web")); got != 2 {
		t.Fatalf("re-deploy duplicated the entry: %d installations", got)
	}

	// Two releases in one namespace are distinct installations.
	m.recordAppDeployment(appDeployment{App: "Web", Context: "prod", Namespace: "ai", Release: "web-2", Kind: appKindHelm})
	if got := len(m.appDeploymentsFor("Web")); got != 3 {
		t.Fatalf("second release not recorded: %d installations", got)
	}

	m.forgetAppDeployment(d2)
	if got := m.appContextsFor("Web"); len(got) != 1 || got[0] != "prod" {
		t.Fatalf("after forget, contexts = %v, want [prod]", got)
	}

	// Removing a cluster must not leave apps looking deployed there.
	m.recordAppDeployment(d2)
	dropped := m.forgetAppsForContext("lab")
	if len(dropped) != 1 {
		t.Fatalf("dropped %d installs for the removed context, want 1", len(dropped))
	}
	for _, d := range m.appDeploys {
		if d.Context == "lab" {
			t.Error("an install survived removal of its context")
		}
	}
}

func TestDeleteAppKeepsDeployments(t *testing.T) {
	// Dropping a definition must not strand running workloads: the registry is
	// the only record that can drive a later uninstall.
	m := testAppModel(t)
	m.apps = []k8sApp{{Name: "Web", Chart: "web", Repo: "https://r"}}
	m.appDeploys = []appDeployment{{App: "Web", Context: "prod", Namespace: "ai", Release: "web", Kind: appKindHelm}}
	m.refreshAppsList()

	m.deleteApp("Web")
	if len(m.apps) != 0 {
		t.Fatal("definition was not removed")
	}
	if len(m.appDeploymentsFor("Web")) != 1 {
		t.Error("deleting the definition also dropped the deployment record")
	}
}

func TestAppDeployScriptHelm(t *testing.T) {
	a := k8sApp{Name: "Web", Repo: "https://charts.example.com", Chart: "web", Version: "1.2.3", Values: "a=1, b=2"}
	d := appDeployment{App: "Web", Context: "prod", Namespace: "ai", Release: "web", Kind: appKindHelm}
	got, err := appDeployScript(a, d, nil)
	if err != nil {
		t.Fatalf("appDeployScript: %v", err)
	}
	for _, want := range []string{
		"helm repo add", "https://charts.example.com",
		"helm upgrade --install", "--kube-context 'prod'", "--namespace 'ai'",
		"--create-namespace", "--version '1.2.3'",
		"--set 'a=1'", "--set 'b=2'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q\n%s", want, got)
		}
	}
	// The chart must be referenced through the repo alias that was just added,
	// not by bare name, or helm resolves it from whatever else is configured.
	if !strings.Contains(got, "/web'") && !strings.Contains(got, "/web ") {
		t.Errorf("chart not qualified by its repo alias:\n%s", got)
	}
}

func TestAppDeployScriptPublishesPrimaryService(t *testing.T) {
	a := k8sApp{Name: "Web", Repo: "https://r", Chart: "web"}
	d := appDeployment{App: "Web", Context: "prod", Namespace: "ai", Release: "web", Kind: appKindHelm}
	got, err := appDeployScript(a, d, nil)
	if err != nil {
		t.Fatalf("appDeployScript: %v", err)
	}
	for _, want := range []string{"aidt_expose", `"spec":{"type":"NodePort"}`, "AIDT_EXPOSED"} {
		if !strings.Contains(got, want) {
			t.Errorf("deploy script missing %q\n%s", want, got)
		}
	}
	// The publish step must come after the install, or there is no Service to
	// patch yet.
	if strings.Index(got, "aidt_expose\n") < strings.Index(got, "helm upgrade --install") {
		t.Error("expose step runs before the install")
	}
	// Publishing must not be done by passing values: chart value paths are not
	// standardised and a --set against a disagreeing schema fails the deploy.
	if strings.Contains(got, "--set service.type") {
		t.Errorf("publish was implemented as a --set:\n%s", got)
	}

	// A manifest install has no values at all and must still be published.
	mo := k8sApp{Name: "Thing", ManifestURL: "https://example.com/app.yaml"}
	md := appDeployment{App: "Thing", Context: "prod", Namespace: "ops", Release: "thing", Kind: appKindManifest}
	mGot, err := appDeployScript(mo, md, nil)
	if err != nil {
		t.Fatalf("appDeployScript(manifest): %v", err)
	}
	if !strings.Contains(mGot, "aidt_expose") {
		t.Errorf("manifest install is not published:\n%s", mGot)
	}
}

func TestAppDeployScriptSurvivesTheExposePatchOnUpgrade(t *testing.T) {
	// Publishing a Service with kubectl patch makes kubectl the field manager
	// for .spec.type. Helm 4's server-side apply then refuses the next upgrade
	// with "conflict with kubectl-patch using v1: .spec.type", so every
	// redeploy of a published app fails until helm is told to take ownership.
	a := k8sApp{Name: "Web", Repo: "https://r", Chart: "web"}
	d := appDeployment{App: "Web", Context: "prod", Namespace: "ai", Release: "web", Kind: appKindHelm}
	got, err := appDeployScript(a, d, nil)
	if err != nil {
		t.Fatalf("appDeployScript: %v", err)
	}
	if !strings.Contains(got, "$AIDT_HELM_FORCE") {
		t.Errorf("upgrade cannot reclaim ownership of a patched field:\n%s", got)
	}
	// Helm 3 has no such flag, and passing it unconditionally would break every
	// deploy on a gateway that already had helm 3 installed.
	if !strings.Contains(got, "grep -q -- '--force-conflicts'") {
		t.Errorf("the flag is not guarded by a support check:\n%s", got)
	}
	if strings.Index(got, `AIDT_HELM_FORCE=""`) > strings.Index(got, "helm upgrade --install") {
		t.Error("the guard runs after the upgrade that needs it")
	}
}

func TestAppDeployScriptKeepsTheNodePortStable(t *testing.T) {
	// helm reclaims .spec.type on upgrade, dropping the node port allocation.
	// Re-publishing without asking for the old port hands out a fresh random
	// one, so the app's URL changes on every redeploy — and an app configured
	// with that URL (Paperclip's publicURL) then rejects its own traffic.
	a := k8sApp{Name: "Web", Repo: "https://r", Chart: "web"}
	d := appDeployment{App: "Web", Context: "prod", Namespace: "ai", Release: "web", Kind: appKindHelm}
	got, err := appDeployScript(a, d, nil)
	if err != nil {
		t.Fatalf("appDeployScript: %v", err)
	}
	if !strings.Contains(got, "AIDT_X_KEEP=") {
		t.Errorf("existing node ports are never recorded:\n%s", got)
	}
	// The capture has to happen before helm resets the service.
	if strings.Index(got, "AIDT_X_KEEP=") > strings.Index(got, "helm upgrade --install") {
		t.Error("node ports are captured after helm has already reset them")
	}
	if !strings.Contains(got, `\"nodePort\":`) {
		t.Errorf("the patch never requests a specific node port:\n%s", got)
	}
	// A remembered port can be taken by something else by the time we ask for
	// it; publishing on a fresh port beats not publishing at all.
	if !strings.Contains(got, "(new port)") {
		t.Errorf("no fallback when the remembered port is unavailable:\n%s", got)
	}
	// A never-before-deployed app has nothing to remember and must still work.
	if !strings.Contains(got, `patch='{"spec":{"type":"NodePort"}}'`) {
		t.Errorf("first install has no unqualified patch path:\n%s", got)
	}
}

func TestAppDeployScriptRespectsExposeNone(t *testing.T) {
	a := k8sApp{Name: "Op", Repo: "https://r", Chart: "op", Expose: exposeNone}
	d := appDeployment{App: "Op", Context: "prod", Namespace: "data", Release: "op", Kind: appKindHelm}
	got, err := appDeployScript(a, d, nil)
	if err != nil {
		t.Fatalf("appDeployScript: %v", err)
	}
	if strings.Contains(got, "aidt_expose") {
		t.Errorf("an app opted out of publishing was still exposed:\n%s", got)
	}
}

func TestExposeMode(t *testing.T) {
	// Publishing is the default, because a stock ClusterIP leaves a healthy
	// install with no address to open.
	if (k8sApp{}).exposeMode() != exposeNodePort {
		t.Error("default is not to publish")
	}
	for _, v := range []string{"none", "None", " NONE "} {
		if (k8sApp{Expose: v}).exposeMode() != exposeNone {
			t.Errorf("Expose=%q did not opt out", v)
		}
	}
}

func TestOperatorBuiltinsAreNotPublished(t *testing.T) {
	// Operators expose webhook and metrics endpoints, not something to browse
	// to, and their managed databases must not land on every node address.
	want := map[string]bool{"CloudNativePG": true, "Redis": true}
	for _, a := range builtinApps() {
		if want[a.Name] && a.exposeMode() != exposeNone {
			t.Errorf("%s should not be published by default", a.Name)
		}
		if !want[a.Name] && a.exposeMode() != exposeNodePort {
			t.Errorf("%s should be published by default", a.Name)
		}
	}
}

func TestExposeFragmentGuardsUnsafeServices(t *testing.T) {
	got := exposeFragment("prod", "ai", "web")
	// A headless service has no cluster IP to publish and patching it breaks
	// the DNS contract its clients rely on.
	if !strings.Contains(got, `"None"`) || !strings.Contains(got, "headless") {
		t.Errorf("headless services are not guarded:\n%s", got)
	}
	// An app already on LoadBalancer/NodePort must be left as the operator set it.
	if !strings.Contains(got, `"$type" != "ClusterIP"`) {
		t.Errorf("an already-published service would be re-patched:\n%s", got)
	}
	// Exposing every service in a release would publish its dependencies.
	if !strings.Contains(got, "$1==rel") {
		t.Errorf("primary service is not selected by release name:\n%s", got)
	}
	// A failure to publish must not fail the deploy that already succeeded.
	if !strings.Contains(got, "return 0") {
		t.Errorf("expose step can fail the deploy:\n%s", got)
	}
}

func TestValuesYAMLNestsDottedPaths(t *testing.T) {
	got, err := valuesYAML(map[string]string{
		"postgresql.auth.password": "pw",
		"auth.betterAuthSecret":    "sec",
	})
	if err != nil {
		t.Fatalf("valuesYAML: %v", err)
	}
	var round map[string]any
	if err := yaml.Unmarshal([]byte(got), &round); err != nil {
		t.Fatalf("rendered values are not valid YAML: %v\n%s", err, got)
	}
	pg, _ := round["postgresql"].(map[string]any)
	auth, _ := pg["auth"].(map[string]any)
	if auth["password"] != "pw" {
		t.Errorf("postgresql.auth.password did not nest: %s", got)
	}
	top, _ := round["auth"].(map[string]any)
	if top["betterAuthSecret"] != "sec" {
		t.Errorf("auth.betterAuthSecret did not nest: %s", got)
	}

	if s, err := valuesYAML(nil); err != nil || s != "" {
		t.Errorf("empty input produced %q, %v", s, err)
	}
	// A path that is both a leaf and a parent cannot be rendered, and silently
	// dropping one of them would deploy with a value the operator never sees.
	if _, err := valuesYAML(map[string]string{"a": "1", "a.b": "2"}); err == nil {
		t.Error("conflicting value paths were accepted")
	}
}

func TestGenerateSecretIsRandomAndSized(t *testing.T) {
	a, err := generateSecret(24)
	if err != nil {
		t.Fatalf("generateSecret: %v", err)
	}
	b, _ := generateSecret(24)
	if a == b {
		t.Error("two generated secrets are identical")
	}
	if len(a) != 48 { // hex doubles the byte count
		t.Errorf("len = %d, want 48", len(a))
	}
	// BetterAuth needs at least 32 characters.
	if s, _ := generateSecret(32); len(s) < 32 {
		t.Errorf("32-byte secret rendered %d chars", len(s))
	}
	if s, _ := generateSecret(0); len(s) != defaultSecretBytes*2 {
		t.Errorf("default length = %d", len(s))
	}
}

func TestEnsureAppSecretsAreStableAcrossRedeploys(t *testing.T) {
	m := testAppModel(t)
	a := k8sApp{Name: "Paperclip", Repo: "https://r", Chart: "paperclip", Secrets: []appSecret{
		{Path: "postgresql.auth.password", Bytes: 24},
		{Path: "auth.betterAuthSecret", Bytes: 32},
	}}
	d := appDeployment{App: "Paperclip", Context: "prod", Namespace: "ai", Release: "paperclip", Kind: appKindHelm}

	first, err := m.ensureAppSecrets(a, d)
	if err != nil {
		t.Fatalf("ensureAppSecrets: %v", err)
	}
	if len(first) != 2 || first["postgresql.auth.password"] == "" {
		t.Fatalf("secrets not generated: %v", first)
	}

	// Redeploying must reuse them. Handing an already-initialised PostgreSQL a
	// fresh password locks the app out of its own database.
	second, err := m.ensureAppSecrets(a, d)
	if err != nil {
		t.Fatalf("ensureAppSecrets: %v", err)
	}
	for k, v := range first {
		if second[k] != v {
			t.Errorf("secret %q was rotated on redeploy", k)
		}
	}

	// A different installation of the same app gets its own secrets.
	other := d
	other.Context = "lab"
	third, _ := m.ensureAppSecrets(a, other)
	if third["postgresql.auth.password"] == first["postgresql.auth.password"] {
		t.Error("two installations share a generated password")
	}

	// They must survive a restart, which is the whole point of persisting them.
	reloaded := loadSettings(m.tokFile)
	if got := reloaded.AppSecrets[d.secretKey()]["postgresql.auth.password"]; got != first["postgresql.auth.password"] {
		t.Errorf("secret did not persist: %q", got)
	}

	// The returned map is a copy; mutating it must not corrupt the store.
	second["postgresql.auth.password"] = "tampered"
	if m.appSecrets[d.secretKey()]["postgresql.auth.password"] == "tampered" {
		t.Error("caller mutated the persisted secret store")
	}

	// Removing the installation clears its credentials.
	m.forgetAppSecrets(d)
	if _, ok := m.appSecrets[d.secretKey()]; ok {
		t.Error("secrets outlived the installation")
	}
	if _, ok := m.appSecrets[other.secretKey()]; !ok {
		t.Error("forgetting one installation dropped another's secrets")
	}
}

func TestAppDeployScriptKeepsSecretsOutOfArgv(t *testing.T) {
	a := k8sApp{Name: "Paperclip", Repo: "https://r", Chart: "paperclip", Secrets: []appSecret{
		{Path: "postgresql.auth.password"},
	}}
	d := appDeployment{App: "Paperclip", Context: "prod", Namespace: "ai", Release: "paperclip", Kind: appKindHelm}
	secrets := map[string]string{"postgresql.auth.password": "s3cr3tvalue"}

	got, err := appDeployScript(a, d, secrets)
	if err != nil {
		t.Fatalf("appDeployScript: %v", err)
	}
	// The secret must never reach helm's argv, where any other user on the host
	// can read it out of ps — the rule bastion.go already follows.
	if strings.Contains(got, "--set postgresql.auth.password") ||
		strings.Contains(got, "--set 'postgresql.auth.password=s3cr3tvalue'") {
		t.Errorf("secret passed as a command argument:\n%s", got)
	}
	for _, want := range []string{"umask 077", "mktemp", "-f \"$AIDT_VALS\"", "s3cr3tvalue", "AIDT_VALUES_EOF"} {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q\n%s", want, got)
		}
	}
	// The temp file holds credentials and must not survive the run.
	if !strings.Contains(got, `trap 'rm -f "$AIDT_VALS"' EXIT`) {
		t.Errorf("values file is not cleaned up:\n%s", got)
	}

	// A manifest install takes no values, so asking for generated secrets is a
	// configuration error rather than something to silently ignore.
	mo := k8sApp{Name: "M", ManifestURL: "https://e/x.yaml", Secrets: []appSecret{{Path: "a.b"}}}
	md := appDeployment{App: "M", Context: "c", Namespace: "n", Release: "r", Kind: appKindManifest}
	if _, err := appDeployScript(mo, md, map[string]string{"a.b": "v"}); err == nil {
		t.Error("secrets on a manifest install were accepted")
	}
}

func TestPaperclipBuiltinDeclaresItsSecrets(t *testing.T) {
	var pc k8sApp
	for _, a := range builtinApps() {
		if a.Name == "Paperclip" {
			pc = a
		}
	}
	if pc.Name == "" {
		t.Fatal("Paperclip is not in the catalog")
	}
	// Without both of these the chart deploys and then fails: PostgreSQL will
	// not initialise with an empty password, and BetterAuth needs a 32-char key.
	want := map[string]bool{"postgresql.auth.password": false, "auth.betterAuthSecret": false}
	for _, s := range pc.Secrets {
		if _, ok := want[s.Path]; ok {
			want[s.Path] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Errorf("Paperclip does not declare %s", path)
		}
	}
}

func TestAppDeployScriptOCISkipsRepoAdd(t *testing.T) {
	a := k8sApp{Name: "n8n", Chart: "oci://reg.example.com/library/n8n"}
	d := appDeployment{App: "n8n", Context: "prod", Namespace: "ops", Release: "n8n", Kind: appKindHelm}
	got, err := appDeployScript(a, d, nil)
	if err != nil {
		t.Fatalf("appDeployScript: %v", err)
	}
	if strings.Contains(got, "helm repo add") {
		t.Errorf("OCI chart should not add a repo:\n%s", got)
	}
	if !strings.Contains(got, "oci://reg.example.com/library/n8n") {
		t.Errorf("OCI reference missing:\n%s", got)
	}
}

func TestAppDeployScriptManifest(t *testing.T) {
	a := k8sApp{Name: "Thing", ManifestURL: "https://example.com/app.yaml"}
	d := appDeployment{App: "Thing", Context: "prod", Namespace: "ops", Release: "thing", Kind: appKindManifest}
	got, err := appDeployScript(a, d, nil)
	if err != nil {
		t.Fatalf("appDeployScript: %v", err)
	}
	// Check for an actual invocation rather than the word: the publish step's
	// comments legitimately mention helm.
	for _, line := range strings.Split(got, "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") {
			continue
		}
		if strings.HasPrefix(code, "helm ") || strings.Contains(code, "; helm ") {
			t.Errorf("manifest deploy invokes helm: %q", code)
		}
	}
	for _, want := range []string{"create namespace 'ops'", "apply -f 'https://example.com/app.yaml'"} {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q\n%s", want, got)
		}
	}
}

func TestAppDeployScriptRejectsIncomplete(t *testing.T) {
	d := appDeployment{App: "X", Context: "prod", Namespace: "ai", Release: "x"}
	if _, err := appDeployScript(k8sApp{Name: "X"}, d, nil); err == nil {
		t.Error("an app with no chart and no manifest was accepted")
	}
	if _, err := appDeployScript(k8sApp{Name: "X", Chart: "c"}, appDeployment{Context: "prod"}, nil); err == nil {
		t.Error("a deployment with no namespace was accepted")
	}
	// A non-OCI chart with no repository cannot be resolved.
	if _, err := appDeployScript(k8sApp{Name: "X", Chart: "c"}, d, nil); err == nil {
		t.Error("a bare chart name with no repo was accepted")
	}
}

func TestAppRemoveScriptUsesRecordedKind(t *testing.T) {
	// The definition has since been edited into a Helm chart, but the install
	// was a manifest. The uninstall has to undo what was actually done.
	a := k8sApp{Name: "Thing", Repo: "https://r", Chart: "thing", ManifestURL: "https://example.com/app.yaml"}
	d := appDeployment{App: "Thing", Context: "prod", Namespace: "ops", Release: "thing", Kind: appKindManifest}
	got, err := appRemoveScript(a, d)
	if err != nil {
		t.Fatalf("appRemoveScript: %v", err)
	}
	if strings.Contains(got, "helm uninstall") {
		t.Errorf("manifest install was removed with helm:\n%s", got)
	}
	if !strings.Contains(got, "delete -f 'https://example.com/app.yaml'") {
		t.Errorf("manifest delete missing:\n%s", got)
	}

	helmD := appDeployment{App: "Thing", Context: "prod", Namespace: "ops", Release: "thing", Kind: appKindHelm}
	got, err = appRemoveScript(a, helmD)
	if err != nil {
		t.Fatalf("appRemoveScript(helm): %v", err)
	}
	if !strings.Contains(got, "helm uninstall 'thing'") {
		t.Errorf("helm uninstall missing:\n%s", got)
	}

	// A manifest install whose URL is gone cannot be deleted automatically, and
	// saying so beats emitting a delete that silently does nothing.
	if _, err := appRemoveScript(k8sApp{Name: "Thing"}, d); err == nil {
		t.Error("manifest removal with no URL was accepted")
	}
}

func TestParseAppReconcile(t *testing.T) {
	out := strings.Join([]string{
		"noise",
		"AIDT_APP_OK prod · ai/web",
		"AIDT_APP_GONE lab · ai/web",
		"more noise",
	}, "\n")
	got := parseAppReconcile(out)
	if v, ok := got["prod · ai/web"]; !ok || v {
		t.Errorf("present install marked missing: %v", got)
	}
	if v, ok := got["lab · ai/web"]; !ok || !v {
		t.Errorf("absent install not marked missing: %v", got)
	}
}

func TestApplyAppReconcileMarksMissing(t *testing.T) {
	m := testAppModel(t)
	m.apps = []k8sApp{{Name: "Web", Repo: "https://r", Chart: "web"}}
	m.appDeploys = []appDeployment{
		{App: "Web", Context: "prod", Namespace: "ai", Release: "web", Kind: appKindHelm},
		{App: "Web", Context: "lab", Namespace: "ai", Release: "web", Kind: appKindHelm},
	}
	gone := m.applyAppReconcile(map[string]bool{
		"prod · ai/web": false,
		"lab · ai/web":  true,
	})
	if gone != 1 {
		t.Fatalf("reported %d missing, want 1", gone)
	}

	// The row must show the warning state, not the deployed state.
	var found bool
	for _, it := range m.appsList.Items() {
		ai, ok := it.(appItem)
		if !ok || ai.add {
			continue
		}
		found = true
		if ai.missing != 1 {
			t.Errorf("appItem.missing = %d, want 1", ai.missing)
		}
		if ai.stateColor() != colYellow {
			t.Error("an app with a missing install is not coloured as a warning")
		}
	}
	if !found {
		t.Fatal("no app row rendered")
	}
}

func TestAppItemStateColors(t *testing.T) {
	if (appItem{count: 0}).stateColor() != colMuted {
		t.Error("an undeployed app should be muted")
	}
	if (appItem{count: 2}).stateColor() != colGreen {
		t.Error("a deployed app should be green")
	}
	// Deployed and undeployed must not render the same, which is the explicit
	// requirement for this section.
	if (appItem{count: 0}).stateColor() == (appItem{count: 1}).stateColor() {
		t.Error("deployed and undeployed apps share a colour")
	}
}
