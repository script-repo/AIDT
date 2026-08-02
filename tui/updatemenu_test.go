package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsLocalInstallerUsesSecureFallback(t *testing.T) {
	script := windowsLocalInstallerScript()
	for _, want := range []string{
		"SecurityProtocolType]::Tls12",
		"$ErrorActionPreference = 'Stop'",
		"Invoke-WebRequest -UseBasicParsing -ErrorAction Stop",
		"Get-Command curl.exe",
		"Test-Path -LiteralPath $p",
		"Length -eq 0",
		"AIDT update failed",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("Windows update bootstrap missing %q", want)
		}
	}
	if strings.Index(script, "Invoke-WebRequest") > strings.Index(script, "try {") && !strings.Contains(script, "catch {") {
		t.Fatal("Windows installer download is not protected by fallback error handling")
	}
	assertPowerShellSyntax(t, script)
}

func TestWindowsInstallerUsesTLSAndCurlFallback(t *testing.T) {
	path := filepath.Join("..", "scripts", "install.ps1")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, want := range []string{
		"SecurityProtocolType]::Tls12",
		"function Download-File",
		"Get-Command curl.exe",
		"Download-File $Url $zip",
		"ConvertFrom-Json",
		"Length -eq 0",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("install.ps1 missing %q", want)
		}
	}
	assertPowerShellSyntax(t, script)
}

// runnablePowerShell returns a PowerShell interpreter that can actually be
// executed here, or "" when there is none.
//
// LookPath alone is not enough: under WSL, PATH includes the Windows System32
// directory, so powershell.exe resolves and has the executable bit set, yet
// exec fails with "exec format error" whenever interop is unavailable. That
// made the parser check fail on every WSL developer machine instead of
// skipping. pwsh is tried first because it is the portable one.
func runnablePowerShell() string {
	for _, name := range []string{"pwsh", "powershell.exe"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if err := exec.Command(path, "-NoProfile", "-Command", "exit 0").Run(); err == nil {
			return path
		}
	}
	return ""
}

func assertPowerShellSyntax(t *testing.T, script string) {
	t.Helper()
	powershell := runnablePowerShell()
	if powershell == "" {
		t.Skip("no runnable PowerShell for parser validation")
	}
	cmd := exec.Command(powershell, "-NoProfile", "-Command", `$s=[Console]::In.ReadToEnd(); [void][scriptblock]::Create($s)`)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PowerShell syntax validation failed: %v\n%s", err, out)
	}
}
