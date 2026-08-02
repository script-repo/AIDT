package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func atlasInstaller(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "command-atlas-install.sh"))
	if err != nil {
		t.Fatalf("installer missing at repo root (the built-in URL serves this file): %v", err)
	}
	return string(b)
}

func TestCommandAtlasIsABuiltin(t *testing.T) {
	var found *customDeploy
	all := builtinCustomDeploys()
	for i := range all {
		if all[i].Name == "Command Atlas" {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatal("Command Atlas is not a built-in deployment")
	}
	if !isBareHTTPURL(found.ScriptURL) {
		t.Errorf("ScriptURL must be a bare URL, got %q", found.ScriptURL)
	}
	if !strings.HasSuffix(found.ScriptURL, "/command-atlas-install.sh") {
		t.Errorf("built-in URL %q does not name command-atlas-install.sh", found.ScriptURL)
	}
	// It fronts a TLS proxy, so it advertises an https service like NP4M/NRCC.
	if found.Scheme != "https" || found.Port != "8443" {
		t.Errorf("scheme/port = %q/%q, want https/8443", found.Scheme, found.Port)
	}
}

// The app hands out real PTY shells and binds to loopback deliberately. The
// installer must not undo that, and must not publish it without authentication.
func TestCommandAtlasKeepsAppOnLoopbackAndAuthenticates(t *testing.T) {
	s := atlasInstaller(t)

	// server.js must not be rewritten to listen on the network.
	for _, forbidden := range []string{"0.0.0.0", "HOST = '0", `sed -i.*HOST`} {
		if strings.Contains(s, forbidden) {
			t.Errorf("installer appears to expose the app directly (%q)", forbidden)
		}
	}
	if !strings.Contains(s, "proxy_pass http://127.0.0.1:${APP_PORT}") {
		t.Error("nginx does not proxy to the app on loopback")
	}
	// PAM auth against local accounts is the control that makes exposure safe.
	for _, want := range []string{
		"auth_pam ",
		`auth_pam_service_name "command-atlas"`,
		"/etc/pam.d/command-atlas",
		"pam_unix.so",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("installer missing PAM auth element %q", want)
		}
	}
	// A missing PAM module must abort, never degrade to no auth.
	if !strings.Contains(s, "nginx rejected the generated configuration") {
		t.Error("installer does not fail closed when nginx rejects the config")
	}
	if !strings.Contains(s, "could not install nginx with the PAM auth module") {
		t.Error("installer does not fail closed when the PAM module is unavailable")
	}
}

// Basic auth sends a real system password on every request, so the front end
// must be TLS.
func TestCommandAtlasServesTLS(t *testing.T) {
	s := atlasInstaller(t)
	for _, want := range []string{"listen ${FRONT_PORT} ssl", "openssl req -x509", "ssl_protocols TLSv1.2 TLSv1.3"} {
		if !strings.Contains(s, want) {
			t.Errorf("installer missing TLS element %q", want)
		}
	}
	if strings.Contains(s, "listen [::]:${FRONT_PORT}") {
		t.Error("an IPv6 listen aborts nginx on hosts with IPv6 disabled")
	}
}

// The app token is a credential. It is injected by the proxy, so it must not
// travel in the published URL or sit in a world-readable file.
func TestCommandAtlasKeepsTokenOutOfPublishedURL(t *testing.T) {
	s := atlasInstaller(t)
	if !strings.Contains(s, `chmod 600 "$ENV_FILE"`) {
		t.Error("token env file is not mode 600")
	}
	if !strings.Contains(s, `chmod 600 "$SITE"`) {
		t.Error("nginx site embeds the token but is not mode 600")
	}
	// The reported URL must carry no token query string.
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "AIDT_SERVICE_INFO") && strings.Contains(line, "token=") {
			t.Errorf("published service URL leaks the token: %s", line)
		}
	}
	// The page reads its token from its own URL and reuses it for the WebSocket,
	// so the operator must land on a tokened URL — but only after authenticating,
	// which is what keeps the token out of the published link.
	if !strings.Contains(s, `return 302 /?token=${ATLAS_TOKEN}`) {
		t.Error("nginx does not hand the token over after authentication")
	}
	if !strings.Contains(s, "absolute_redirect off") {
		t.Error("without absolute_redirect off the 302 loses the port")
	}
}

// The app rejects a WebSocket upgrade whose Origin is not its own loopback
// address. Behind the proxy the browser sends the external origin, so without a
// rewrite the terminal never connects.
func TestCommandAtlasRewritesOriginForWebSocket(t *testing.T) {
	s := atlasInstaller(t)
	if !strings.Contains(s, "proxy_set_header Origin http://127.0.0.1:${APP_PORT}") {
		t.Error("Origin is not rewritten, so the WebSocket upgrade will be refused")
	}
	if !strings.Contains(s, `proxy_set_header Upgrade \$http_upgrade`) {
		t.Error("the Upgrade header is not forwarded")
	}
}

func TestCommandAtlasInstallerIsValidShell(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(atlasInstaller(t))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("installer has invalid shell syntax: %v\n%s", err, out)
	}
}

// The heredoc must expand shell values while leaving nginx's own variables
// alone; getting that backwards produces a config nginx cannot parse.
func TestCommandAtlasNginxTemplateEscaping(t *testing.T) {
	s := atlasInstaller(t)
	start := strings.Index(s, `tee "$SITE" >/dev/null <<NGINXEOF`)
	end := strings.Index(s, "\nNGINXEOF\n")
	if start < 0 || end < start {
		t.Fatal("could not locate the nginx heredoc")
	}
	tmpl := s[start:end]
	// nginx runtime variables must be escaped so the shell leaves them alone.
	for _, v := range []string{`\$http_upgrade`, `\$arg_token`, `\$uri`, `\$host`, `\$remote_addr`, `\$atlas_connection`, `\$atlas_redirect`} {
		if !strings.Contains(tmpl, v) {
			t.Errorf("nginx variable %s is not escaped and would be eaten by the shell", v)
		}
	}
	// Shell values must NOT be escaped, or the config gets literal $NAME.
	for _, v := range []string{"${FRONT_PORT}", "${APP_PORT}", "${ATLAS_TOKEN}", "${CERT_DIR}"} {
		if !strings.Contains(tmpl, v) {
			t.Errorf("shell value %s is missing from the template", v)
		}
		if strings.Contains(tmpl, `\`+v) {
			t.Errorf("shell value %s is escaped and would not be substituted", v)
		}
	}
}

// AIDT runs custom installers as root, so a "SUDO" variable that is empty in
// that case silently drops its own flags: `$SUDO -E bash -` becomes `-E bash -`
// and dies with exit 127. Any installer combining the variable with a
// sudo-specific flag has this bug, so none may.
func TestNoInstallerCombinesSudoVariableWithFlags(t *testing.T) {
	pattern := regexp.MustCompile(`\$\{?SUDO\}? +-[a-zA-Z]`)
	roots := []string{"..", filepath.Join("..", "scripts"), filepath.Join("..", "scripts", "remote")}
	checked := 0
	for _, dir := range roots {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			checked++
			for i, line := range strings.Split(string(body), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "#") {
					continue // prose about the bug is fine
				}
				if pattern.MatchString(line) {
					t.Errorf("%s:%d combines $SUDO with a flag, which breaks when already root:\n  %s",
						e.Name(), i+1, trimmed)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no installer scripts were checked")
	}
}

func TestCommandAtlasShipsInReleaseArchive(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "command-atlas-install.sh") {
		t.Error("installer is not included in the release archives")
	}
}
