package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHermesDeployScriptHeadless(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.agentReg = map[string]string{}
	m.agentHosts = map[string][]string{}
	m.token = "tok"
	script := m.agentDeployScript(mustAgent(t, "Hermes"))
	for _, want := range []string{
		"ensuring curl, git, Node.js",
		"--skip-setup",
		"hermes-agent.nousresearch.com/install.sh",
		"pointing hermes at Olla",
		// Must not end by execing interactive hermes (exit 127 when missing).
	} {
		if !strings.Contains(script, want) {
			t.Errorf("hermes deploy missing %q", want)
		}
	}
	// Last meaningful action should not be a bare "hermes" line as the only launch.
	if strings.HasSuffix(strings.TrimSpace(script), "\nhermes") {
		t.Error("hermes deploy should not exec interactive hermes at the end")
	}
}

func TestCrushDeployScriptInstallsDependenciesAndStaysHeadless(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	script := m.agentDeployScript(mustAgent(t, "Crush"))
	for _, want := range []string{
		"apt-get install -y ca-certificates curl git gnupg",
		"repo.charm.sh/apt/gpg.key",
		"dnf",
		"yum",
		"repo.charm.sh/yum/",
		"Crush binary not found after package installation",
		"Open Crush from Agents",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("crush deploy missing %q", want)
		}
	}
	if strings.HasSuffix(strings.TrimSpace(script), "\ncrush") {
		t.Error("crush deploy should not launch interactive Crush at the end")
	}
}

func TestCrushDeployScriptShellSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable for deployment script syntax validation")
	}
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	script := m.agentDeployScript(mustAgent(t, "Crush"))
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crush deploy script has invalid shell syntax: %v\n%s", err, out)
	}
}

func TestCrushOpensInDedicatedWorkspace(t *testing.T) {
	for _, want := range []string{
		`mkdir -p "$HOME/.ai-deployment-toolkit/crush-workspace"`,
		`cd "$HOME/.ai-deployment-toolkit/crush-workspace"`,
		"exec crush",
	} {
		if !strings.Contains(crushOpenCommand, want) {
			t.Errorf("Crush launch command missing %q", want)
		}
	}
}

func TestAgentRemovalForgetsUnreachableHost(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.agentReg["Hermes"] = "10.0.0.2"
	m.agentHosts["Hermes"] = []string{"10.0.0.2"}

	m = drive(m, agentRemovedMsg{
		agent:          "Hermes",
		attemptedHosts: []string{"10.0.0.2"},
		errs:           []string{"10.0.0.2: connection refused"},
	})

	if _, ok := m.agentReg["Hermes"]; ok {
		t.Fatal("unreachable host remained the active Hermes registration")
	}
	if _, ok := m.agentHosts["Hermes"]; ok {
		t.Fatal("unreachable host remained in the Hermes host list")
	}
	if !strings.Contains(m.notice, "deregistered from 1 host(s)") || !strings.Contains(m.notice, "cleanup unconfirmed") {
		t.Fatalf("removal notice does not distinguish deregistration from remote cleanup: %q", m.notice)
	}
}

func TestDeletingHostForgetsAllAgentRegistrations(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.agentReg["OpenCode"] = "10.0.0.1"
	m.agentHosts["OpenCode"] = []string{"10.0.0.1"}
	m.agentReg["Hermes"] = "10.0.0.1"
	m.agentHosts["Hermes"] = []string{"10.0.0.1", "10.0.0.2"}
	m.agentReg["Claude Code"] = "gateway.example.com"
	m.agentHosts["Claude Code"] = []string{"gateway.example.com"}

	forgotten := m.forgetAgentHost("10.0.0.1", "gateway.example.com")
	if strings.Join(forgotten, ",") != "Claude Code,Hermes,OpenCode" {
		t.Fatalf("unexpected forgotten agents: %v", forgotten)
	}
	if _, ok := m.agentReg["OpenCode"]; ok {
		t.Fatal("OpenCode registration remained on deleted host")
	}
	if _, ok := m.agentReg["Claude Code"]; ok {
		t.Fatal("Claude Code hostname registration remained on deleted host")
	}
	if got := m.agentReg["Hermes"]; got != "10.0.0.2" {
		t.Fatalf("Hermes did not fall back to remaining host: %q", got)
	}
}

func TestSuccessfulVMDeletionForgetsAgentsButFailureKeepsThem(t *testing.T) {
	makeModel := func() model {
		m := newModel("http://gateway.example.com:40114", "rocky", "pw")
		m.tokFile = filepath.Join(t.TempDir(), "tui.json")
		m.agentReg["OpenCode"] = "gateway.example.com"
		m.agentHosts["OpenCode"] = []string{"gateway.example.com"}
		m.pendingDeleteHosts = []string{"10.0.0.1", "gateway.example.com"}
		return m
	}

	success, _ := makeModel().handleProc(ProcEvent{Done: true, Code: 0})
	succeeded := success.(model)
	if _, ok := succeeded.agentReg["OpenCode"]; ok {
		t.Fatal("successful VM deletion left the agent registered")
	}

	failure, _ := makeModel().handleProc(ProcEvent{Done: true, Code: 1})
	failed := failure.(model)
	if failed.agentReg["OpenCode"] != "gateway.example.com" {
		t.Fatal("failed VM deletion removed the agent registration")
	}
}

func TestUpdatePlanIncludesRegisteredNewAgents(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.agentReg = map[string]string{}
	m.agentHosts = map[string][]string{}
	for _, name := range []string{"OpenCode", "Goose", "Grok Build", "Claude Code"} {
		m.agentReg[name] = "10.0.0.1"
		m.agentHosts[name] = []string{"10.0.0.1"}
	}
	steps := m.updateAgentsSteps()
	if len(steps) != 4 {
		t.Fatalf("update plan has %d steps, want only 4 registered agents", len(steps))
	}
	for _, name := range []string{"OpenCode", "Goose", "Grok Build", "Claude Code"} {
		found := false
		for _, step := range steps {
			if strings.Contains(step.title, name) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("update plan omitted %s", name)
		}
	}
}

func TestRegisteredCrushAndHermesUseUpgradeScripts(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.agentReg = map[string]string{"Crush": "10.0.0.1", "Hermes": "10.0.0.2"}
	m.agentHosts = map[string][]string{"Crush": {"10.0.0.1"}, "Hermes": {"10.0.0.2"}}

	steps := m.updateAgentsSteps()
	if len(steps) != 2 {
		t.Fatalf("update plan has %d steps, want 2", len(steps))
	}
	for _, step := range steps {
		switch {
		case strings.Contains(step.title, "Crush"):
			if !strings.Contains(step.script, "--only-upgrade crush") || !strings.Contains(step.script, "dnf upgrade -y crush") {
				t.Error("Crush update step does not upgrade the installed package")
			}
		case strings.Contains(step.title, "Hermes"):
			if !strings.Contains(step.script, "installing latest Hermes") || !strings.Contains(step.script, "--skip-setup") {
				t.Error("Hermes update step does not rerun the official installer")
			}
		}
	}
}

func TestAgentScriptsAvoidArgumentsAndPrivateStaging(t *testing.T) {
	secretScript := "export TOKEN=super-secret\necho ok\n"
	commands := map[string]*exec.Cmd{
		"ssh": sshStepCmd("rocky", "10.0.0.1", "", secretScript, false),
	}
	if runtime.GOOS != "windows" {
		commands["local"] = localStepCmd(secretScript, false)
	}
	for name, cmd := range commands {
		if strings.Contains(strings.Join(cmd.Args, " "), "super-secret") {
			t.Errorf("%s update command exposes script contents in argv", name)
		}
		got, err := io.ReadAll(cmd.Stdin)
		if err != nil || string(got) != secretScript {
			t.Errorf("%s update command did not stream the script over stdin", name)
		}
	}

	path := filepath.Join(t.TempDir(), "staged deploy.sh")
	if _, err := localWriteScript(path, secretScript); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("staged script mode = %o, want 700", info.Mode().Perm())
	}
	command := stagedScriptCommand(path)
	if !strings.Contains(command, "rm -f") || !strings.Contains(command, "'"+path+"'") {
		t.Fatalf("staged script command does not safely clean up %q: %s", path, command)
	}
}

func TestNewAgentDeployScriptsUseOfficialInstallers(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.token = "tok"
	wants := map[string][]string{
		"OpenCode":    {"https://opencode.ai/install", "AIDT OpenCode configuration"},
		"Goose":       {"github.com/aaif-goose/goose/releases/download/stable/download_cli.sh", "writing Olla provider for Goose"},
		"Grok Build":  {"https://x.ai/cli/install.sh", "AIDT Grok Build configuration"},
		"Claude Code": {"https://claude.ai/install.sh", "Claude Code installed"},
	}
	for name, expected := range wants {
		script := m.agentDeployScript(mustAgent(t, name))
		for _, want := range expected {
			if !strings.Contains(script, want) {
				t.Errorf("%s deploy missing %q", name, want)
			}
		}
		if !strings.Contains(script, `INSTALLER="$(mktemp)"`) || !strings.Contains(script, `-o "$INSTALLER"`) {
			t.Errorf("%s deploy does not fail closed when its installer download fails", name)
		}
		if strings.Contains(script, "grok.com/install.sh") {
			t.Errorf("%s deploy uses obsolete Grok installer", name)
		}
	}
}

func TestNewAgentOllaConfiguration(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.token = ""
	m.defModel = "qwen3:8b"
	for _, a := range []agentDef{mustAgent(t, "OpenCode"), mustAgent(t, "Goose"), mustAgent(t, "Grok Build"), mustAgent(t, "Claude Code")} {
		if !strings.Contains(m.agentConfigScript(a), "umask 077") {
			t.Errorf("%s config does not set a private umask before writing credentials", a.name)
		}
	}

	var openCode map[string]any
	if err := json.Unmarshal(decodeScriptPayload(t, m.openCodeConfigScript(m.effDefaultModel())), &openCode); err != nil {
		t.Fatal(err)
	}
	if openCode["model"] != "olla/qwen3:8b" {
		t.Fatalf("OpenCode default model = %v", openCode["model"])
	}
	openProviders := openCode["provider"].(map[string]any)
	olla := openProviders["olla"].(map[string]any)
	options := olla["options"].(map[string]any)
	if options["baseURL"] != "http://10.0.0.1:40114/olla/openai/v1" {
		t.Fatalf("OpenCode Olla base URL = %v", options["baseURL"])
	}
	if options["apiKey"] != "olla" {
		t.Fatalf("OpenCode Olla API key = %v", options["apiKey"])
	}

	var goose map[string]any
	if err := json.Unmarshal(decodeScriptPayload(t, m.gooseConfigScript(m.effDefaultModel())), &goose); err != nil {
		t.Fatal(err)
	}
	if goose["name"] != "olla" || goose["base_url"] != "http://10.0.0.1:40114/olla/openai/v1/chat/completions" {
		t.Fatalf("Goose provider is not Olla: %#v", goose)
	}

	grok := string(decodeScriptPayload(t, m.grokConfigScript(m.effDefaultModel())))
	for _, want := range []string{`model = "qwen3:8b"`, `base_url = "http://10.0.0.1:40114/olla/openai/v1"`, `api_key = "olla"`, `default = "olla"`} {
		if !strings.Contains(grok, want) {
			t.Errorf("Grok config missing %q", want)
		}
	}

	claudeEnv := string(decodeScriptPayload(t, m.claudeCodeConfigScript(m.effDefaultModel())))
	for _, want := range []string{"ANTHROPIC_BASE_URL", "/olla/anthropic", "ANTHROPIC_DEFAULT_SONNET_MODEL", "qwen3:8b"} {
		if !strings.Contains(claudeEnv, want) {
			t.Errorf("Claude Code environment missing %q", want)
		}
	}
}

func TestAgentCatalogDeploysToWorkers(t *testing.T) {
	for _, a := range agentCatalog {
		if a.target != "worker" {
			t.Errorf("%s target = %q, want worker", a.name, a.target)
		}
	}
}

func TestAgentWorkerHostsDeduplicatesEndpoints(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.endpoints = []endpointEntry{
		{Name: "worker-1", URL: "http://10.0.0.2:11434", Type: "ollama"},
		{Name: "worker-1-alias", URL: "http://10.0.0.2:11434", Type: "ollama"},
		{Name: "worker-2", URL: "http://10.0.0.3:11434", Type: "ollama"},
		{Name: "vllm", URL: "http://10.0.0.4:8000", Type: "vllm"},
	}
	if got := strings.Join(m.agentWorkerHosts(), ","); got != "10.0.0.2,10.0.0.3" {
		t.Fatalf("agent worker hosts = %q", got)
	}
}

func TestAllWorkerSelectionStartsBatchDeployment(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.endpoints = []endpointEntry{
		{Name: "worker-1", URL: "http://10.0.0.2:11434", Type: "ollama"},
		{Name: "worker-2", URL: "http://10.0.0.3:11434", Type: "ollama"},
	}
	m.modal = modalAgentHost
	m.pendingAgent = "OpenCode"
	m.pendingAct = "deploy"
	m.fAgentHost = "all"
	m.pendingAgentHosts = []string{"10.0.0.2", "10.0.0.3"}
	// A background refresh after the picker opened must not change the confirmed set.
	m.endpoints = append(m.endpoints, endpointEntry{Name: "worker-3", URL: "http://10.0.0.4:11434", Type: "ollama"})

	cmd := m.onFormComplete()
	if cmd == nil {
		t.Fatal("all-worker selection did not start a deployment")
	}
	if !strings.Contains(m.notice, "all 2 workers") {
		t.Fatalf("unexpected deployment notice: %q", m.notice)
	}
}

func TestAgentBatchDeploymentRegistersSuccessfulWorkers(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.agentReg = map[string]string{}
	m.agentHosts = map[string][]string{}
	m.agentBusy = true

	m = drive(m, agentBatchDeployedMsg{
		agent:   "OpenCode",
		okHosts: []string{"10.0.0.2", "10.0.0.3"},
		errs:    []string{"10.0.0.4: connection refused"},
	})

	if got := strings.Join(m.agentHosts["OpenCode"], ","); got != "10.0.0.2,10.0.0.3" {
		t.Fatalf("registered batch hosts = %q", got)
	}
	if m.agentReg["OpenCode"] != "10.0.0.2" {
		t.Fatalf("primary batch registration = %q", m.agentReg["OpenCode"])
	}
	if !strings.Contains(m.notice, "deployed on 2 worker(s)") || !strings.Contains(m.notice, "connection refused") {
		t.Fatalf("batch notice did not report partial success: %q", m.notice)
	}
	if m.agentBusy {
		t.Fatal("batch completion did not clear the deployment busy flag")
	}

	item := agentItem{name: "OpenCode", desc: "coding agent", endpoint: "Olla", registered: true, regHost: "10.0.0.2", regCount: 2}
	if !strings.Contains(item.Description(), "deployed on 2 workers") {
		t.Fatalf("multi-worker agent description = %q", item.Description())
	}
}

func TestAgentDeployManyExecutesLocalBatchPath(t *testing.T) {
	a := agentDef{name: "Test Agent", cli: "true", deployable: true, target: "worker"}
	msg := agentDeployManyCmd(
		a,
		[]string{"localhost"},
		"rocky",
		"",
		agentBatchDeployOptions{scripts: map[string]string{"localhost": "set -e\nprintf 'batch-ok\\n'\n"}},
	)().(agentBatchDeployedMsg)
	if strings.Join(msg.okHosts, ",") != "localhost" || len(msg.errs) != 0 {
		t.Fatalf("local batch deployment result: ok=%v errs=%v", msg.okHosts, msg.errs)
	}
}

func TestHermesGatewayFallsBackToSuccessfulWorker(t *testing.T) {
	a := agentDef{name: "Hermes", cli: "hermes", deployable: true, target: "worker"}
	msg := agentDeployManyCmd(
		a,
		[]string{"localhost", "127.0.0.1"},
		"rocky",
		"",
		agentBatchDeployOptions{
			scripts: map[string]string{
				"localhost": "exit 1\n",
				"127.0.0.1": "exit 0\n",
			},
			gatewayHost:     "localhost",
			gatewayFallback: "exit 0\n",
		},
	)().(agentBatchDeployedMsg)
	if strings.Join(msg.okHosts, ",") != "127.0.0.1" {
		t.Fatalf("Hermes fallback successes = %v", msg.okHosts)
	}
	if msg.gatewayHost != "127.0.0.1" {
		t.Fatalf("Hermes gateway fallback host = %q", msg.gatewayHost)
	}
}

func TestAgentBusyBlocksMaintenance(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.agentBusy = true
	if cmd := m.startUpdatePlan([]updateStep{{title: "test", local: true, script: "true"}}, "test"); cmd != nil {
		t.Fatal("maintenance started during all-worker agent deployment")
	}
	if !strings.Contains(m.notice, "active agent deployment or removal") {
		t.Fatalf("unexpected busy notice: %q", m.notice)
	}
}

func TestMaintenanceAndRemovalBlockAgentDeployment(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.endpoints = []endpointEntry{{Name: "worker-1", URL: "http://10.0.0.2:11434", Type: "ollama"}}
	m.refreshAgents()
	m.procBusy = true
	if cmd := m.deploySelectedAgent(); cmd != nil {
		t.Fatal("agent deployment started during maintenance")
	}
	if !strings.Contains(m.notice, "active deployment, removal, or update") {
		t.Fatalf("unexpected maintenance notice: %q", m.notice)
	}

	m.procBusy = false
	m.agentReg = map[string]string{"Hermes": "10.0.0.2"}
	m.agentHosts = map[string][]string{"Hermes": {"10.0.0.2"}}
	m.modal = modalAgentRemove
	m.pendingAgent = "Hermes"
	m.fRemoveTarget = "10.0.0.2"
	if cmd := m.onFormComplete(); cmd == nil {
		t.Fatal("agent removal did not start")
	}
	if !m.agentBusy {
		t.Fatal("agent removal did not set the busy state")
	}
}

func TestOpenMultiHostAgentWithoutEndpointInventory(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.agentReg = map[string]string{"OpenCode": "10.0.0.2"}
	m.agentHosts = map[string][]string{"OpenCode": {"10.0.0.2", "10.0.0.3"}}
	m.endpoints = nil

	cmd := m.openAgentHostPick("OpenCode", "open")
	if cmd == nil || m.form == nil {
		t.Fatal("registered multi-host agent could not open its picker without live endpoints")
	}
	if m.fAgentHost != "10.0.0.2" {
		t.Fatalf("offline host picker selected %q", m.fAgentHost)
	}
}

func TestHermesAllWorkersStartsOneTelegramGateway(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.token = "tok"
	m.hermesCfg = hermesSettings{GatewayEnabled: true, TelegramBotToken: "bot-token"}
	a := mustAgent(t, "Hermes")
	scripts := m.agentDeployScripts(a, []string{"10.0.0.2", "10.0.0.3"})
	if !strings.Contains(scripts["10.0.0.2"], "gateway install") {
		t.Fatal("primary Hermes worker did not receive gateway setup")
	}
	if strings.Contains(scripts["10.0.0.3"], "gateway install") {
		t.Fatal("secondary Hermes worker received duplicate gateway setup")
	}
}

func TestCrushConfigAndUninstallProtectSupportedPlatforms(t *testing.T) {
	if !strings.Contains(crushMergePy, "json.load(sys.stdin)") || !strings.Contains(crushMergePy, "os.chmod(base, 0o600)") {
		t.Fatal("remote Crush config merge does not privately consume and protect credentials")
	}
	uninstall := agentUninstallScript(mustAgent(t, "Crush"))
	for _, want := range []string{"apt-get remove -y crush", "dnf remove -y crush", "yum remove -y crush"} {
		if !strings.Contains(uninstall, want) {
			t.Errorf("Crush uninstall missing %q", want)
		}
	}
}

func TestNewAgentOpenCommandsSelectOlla(t *testing.T) {
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.token = "to'k"
	m.defModel = "qwen3:8b"
	wants := map[string][]string{
		"OpenCode":    {"OPENCODE_CONFIG", "exec opencode"},
		"Goose":       {"goose.env", "GOOSE_PROVIDER=olla", "GOOSE_MODEL", "exec goose session"},
		"Grok Build":  {"GROK_HOME", "exec grok"},
		"Claude Code": {"claude-code.env", "exec claude"},
	}
	for name, expected := range wants {
		cmd := m.agentOpenCmd(mustAgent(t, name))
		for _, want := range expected {
			if !strings.Contains(cmd, want) {
				t.Errorf("%s open command missing %q", name, want)
			}
		}
		if strings.Contains(cmd, m.token) {
			t.Errorf("%s open command exposes the Olla token in argv", name)
		}
	}
}

func TestAllAgentScriptsHaveValidShellSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable for script syntax validation")
	}
	m := newModel("http://10.0.0.1:40114", "rocky", "pw")
	m.tokFile = filepath.Join(t.TempDir(), "tui.json")
	m.token = "to'k"
	for _, a := range agentCatalog {
		for label, script := range map[string]string{
			"deploy":    m.agentDeployScript(a),
			"uninstall": agentUninstallScript(a),
			"open":      m.agentOpenCmd(a),
		} {
			cmd := exec.Command(bash, "-n")
			cmd.Stdin = strings.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s %s script has invalid shell syntax: %v\n%s", a.name, label, err, out)
			}
		}
	}
}

func decodeScriptPayload(t *testing.T, script string) []byte {
	t.Helper()
	start := strings.Index(script, "\necho ")
	if start < 0 {
		t.Fatalf("script has no encoded payload: %q", script)
	}
	start += len("\necho ")
	end := strings.Index(script[start:], " | base64 -d")
	if end < 0 {
		t.Fatalf("script payload has no base64 decoder: %q", script)
	}
	b, err := base64.StdEncoding.DecodeString(script[start : start+end])
	if err != nil {
		t.Fatalf("decode script payload: %v", err)
	}
	return b
}

func mustAgent(t *testing.T, name string) agentDef {
	t.Helper()
	a, ok := agentByName(name)
	if !ok {
		t.Fatalf("agent %q missing from catalog", name)
	}
	return a
}
