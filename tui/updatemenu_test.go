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

func assertPowerShellSyntax(t *testing.T, script string) {
	t.Helper()
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		powershell, err = exec.LookPath("pwsh")
	}
	if err != nil {
		t.Skip("PowerShell is unavailable for parser validation")
	}
	cmd := exec.Command(powershell, "-NoProfile", "-Command", `$s=[Console]::In.ReadToEnd(); [void][scriptblock]::Create($s)`)
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PowerShell syntax validation failed: %v\n%s", err, out)
	}
}
