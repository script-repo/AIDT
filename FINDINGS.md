# Repository Review Findings

Review date: 2026-07-31

This document records issues identified during a repository architecture and
operational review. It is a remediation backlog, not a statement that every
item must be addressed before using AIDT in a trusted lab environment.

## Current Validation Baseline

- `go test ./...` passes.
- `go vet ./...` passes.
- `python scripts/nutanix_olla_vm.py --help` loads successfully.
- The active UI contains nine sections and supports Crush, OpenCode, Goose,
  Grok Build, Claude Code, and Hermes.
- The Go application is the operator-facing orchestrator; Python performs
  Nutanix mutations; shell scripts configure Linux guests.

## Critical And High-Priority Findings

| ID | Status | Finding | Relevant code | Suggested remediation |
| --- | --- | --- | --- | --- |
| SEC-01 | Open | Prism TLS verification and SSH host-key verification are disabled in several paths. This permits machine-in-the-middle attacks against infrastructure credentials and managed hosts. | `tui/pc.go`, `tui/ssh.go`, `tui/console.go`, `scripts/nutanix_olla_vm.py` | Support a trusted CA bundle and managed `known_hosts`; require explicit opt-in for insecure lab mode. |
| SEC-02 | Open | VM passwords are passed as command-line arguments and included in the TUI subprocess log. They can be exposed through the UI and local process inspection. | `tui/forms.go`, `tui/update.go` | Pass secrets through protected environment variables or stdin and redact all command logging. |
| SEC-03 | Open | Operational secrets are stored as plaintext in `~/.ai-deployment-toolkit/tui.json`. Unix mode `0600` is requested, but equivalent restrictive Windows ACL handling is not applied. | `tui/commands.go`, `tui/sshkeys.go` | Use the OS credential store where possible; at minimum apply explicit Windows ACLs and repair Unix modes on existing files. |
| SEC-04 | Open | Install and update paths execute mutable remote scripts, frequently as root, without checksum or signature verification. Published release checksums are not consumed by the installers. | `scripts/install.sh`, `scripts/install.ps1`, `scripts/remote/install-olla.sh`, `scripts/remote/install-ollama.sh`, `tui/agents.go` | Pin versions or commits, verify release checksums/signatures, and avoid `curl | bash` execution. |
| SEC-05 | Open | Chat web fetch accepts arbitrary URLs, follows redirects, and promotes fetched content into a system message. This creates SSRF and prompt-injection exposure. | `tui/webfetch.go`, `tui/update.go` | Block private/link-local destinations, validate every redirect, cap content more strictly, and treat fetched data as untrusted user content. |
| SEC-06 | Open | A free-form model name is interpolated into a remote shell command used to install Ollama models. Shell metacharacters can produce command injection. | `scripts/nutanix_olla_vm.py`, `tui/forms.go` | Validate model names and pass remote arguments through a safely quoted argument boundary. |
| SEC-07 | Open | Olla and Ollama bind to all interfaces and firewall rules expose their ports. Token enforcement is not configured or demonstrated by the installer. | `scripts/remote/install-olla.sh`, `scripts/remote/install-ollama.sh`, `tui/olla.go` | Restrict network ranges and require authenticated gateway access before deployment outside an isolated lab. |
| SEC-08 | Open | Custom deployments intentionally execute arbitrary URLs or shell commands with passwordless-sudo access. Compromise of persisted settings becomes remote root execution. | `tui/customdeploy.go`, `scripts/nutanix_olla_vm.py` | Add an explicit trust warning, optional allowlist/signature policy, and confirmation that shows the exact command and source revision. |

## Reliability And Correctness Findings

| ID | Status | Finding | Relevant code | Suggested remediation |
| --- | --- | --- | --- | --- |
| REL-01 | Open | Endpoint removal and VM deletion are launched concurrently even though endpoint removal should happen first. A deleted worker can remain registered in Olla. | `tui/update.go` | Sequence deregistration and deletion, and stop when deregistration fails unless the operator forces deletion. |
| REL-02 | Open | Provisioning has no rollback. A failure after VM creation leaves a partially configured VM and possibly stale local state. | `scripts/nutanix_olla_vm.py` | Track completed phases and offer automatic cleanup or a clearly reported resumable state. |
| REL-03 | Open | Python endpoint registration builds gateway configuration from local `state.json` instead of first reading live gateway state. Manual or externally added endpoints can be overwritten. | `scripts/nutanix_olla_vm.py` | Read, merge, validate, back up, and atomically replace the live gateway configuration. |
| REL-04 | Open | `ApplyEndpointChange` can continue after a failed gateway configuration read and then write a replacement derived from empty input. | `tui/ssh.go` | Fail closed on read or parse errors; validate and back up YAML before replacement. |
| REL-05 | Open | Status polling can overlap because the poll interval is shorter than HTTP timeouts, and responses have no generation identifier. Stale requests can update current connection state. | `tui/app.go`, `tui/update.go`, `tui/olla.go` | Permit one active poll or attach a connection generation to every request and response. |
| REL-06 | Open | Chat submission does not prevent a second request while the first stream is active. Stream channels and usage state can be replaced and responses mixed. | `tui/update.go` | Disable submission during streaming or assign independent request IDs and cancellation controls. |
| REL-07 | Open | Missing VM addresses are represented as `-` in inventory but some console and update paths only reject an empty address. | `tui/pc.go`, `tui/update.go`, `tui/updatemenu.go` | Normalize unknown addresses to empty values and validate hosts at every action boundary. |
| REL-08 | Open | Prism inventory calls are capped without pagination, so larger environments can silently omit VMs, images, clusters, or subnets. | `tui/pc.go` | Implement pagination until all pages are consumed or a user-visible limit is reached. |
| REL-09 | Open | New Olla defaults are not consistently applied during gateway updates because an existing `olla.yaml` is preserved, while other registration paths replace it wholesale. | `scripts/remote/install-olla.sh`, `scripts/nutanix_olla_vm.py` | Define one versioned configuration migration and merge strategy. |

## Release And Supply-Chain Findings

| ID | Status | Finding | Relevant code | Suggested remediation |
| --- | --- | --- | --- | --- |
| RELS-01 | Addressed 2026-07-31 | GitHub used `master` as the default branch while the release workflow listened only to `main`, preventing the workflow from running. | `.github/workflows/release.yml` | Repository migrated to `main`; retain a single canonical branch name. |
| RELS-02 | Open | Every push to `main` automatically creates and publishes a patch release, including documentation-only changes, without approval. | `.github/workflows/release.yml` | Separate CI from release, then release from explicit tags or a manual workflow. |
| RELS-03 | Open | The release workflow has no test, vet, script-validation, or packaging-validation gate before publishing. | `.github/workflows/release.yml` | Add required validation jobs and make GoReleaser depend on them. |
| RELS-04 | Open | Concurrent pushes can calculate and attempt to create the same next patch tag. | `.github/workflows/release.yml` | Add workflow concurrency and determine the release version in one serialized job. |
| RELS-05 | Open | Actions, Go, and GoReleaser use mutable major or moving versions; releases have no signature, SBOM, or provenance. | `.github/workflows/release.yml`, `.goreleaser.yaml` | Pin actions by digest, pin tool versions, and publish provenance, signatures, and an SBOM. |

## Maintainability And Documentation Findings

| ID | Status | Finding | Relevant code | Suggested remediation |
| --- | --- | --- | --- | --- |
| DOC-01 | Addressed 2026-07-31 | The README documented removed Buzz, Nanoclaw, OpenClaw, and OmniRoute UI features while the active catalog only offered Crush and Hermes. | `README.md`, `tui/agents.go` | README was rewritten against the current implementation. Keep feature documentation tied to active catalog and section definitions. |
| DOC-02 | Open | Source still contains unused state, comments, persistence fields, and install fragments for removed agents and Buzz. | `tui/agents.go`, `tui/app.go`, `tui/commands.go`, `tui/updatemenu.go` | Remove dead architecture after confirming no persisted-data migration is required. |
| DOC-03 | Open | The standalone NAI Scenario D scripts are packaged but not exposed by the TUI or documented, and they reference a missing companion document. | `scripts/remote/deploy-nai-scenario-d*.sh`, `.goreleaser.yaml` | Decide whether NAI is supported; document and test it, or remove it from release archives. |
| DOC-04 | Open | The README previously implied an API token protected Olla, but current HTTP calls and install configuration do not demonstrate enforcement. | `README.md`, `tui/olla.go`, `scripts/remote/install-olla.sh` | Document the actual trust model and implement authentication before presenting tokens as an access control. |

## Testing Gaps

- Add HTTP and streaming tests for Olla status, model inventory, and chat.
- Add SSH and YAML mutation tests, including failed reads and atomic rollback.
- Add Prism API negotiation, pagination, task timeout, and response parsing tests.
- Add persistence and platform-specific secret-permission tests.
- Add subprocess redaction and multi-worker deployment phase tests.
- Add update-plan, custom-deployment, agent removal, and registration tests.
- Isolate every test from the developer's real home directory and persisted settings.
- Add release archive smoke tests for all supported operating-system layouts.

## Deferred Design Decisions

- Decide whether AIDT is explicitly a trusted-lab tool or should meet production
  infrastructure security expectations. Reflect that decision in defaults and warnings.
- Decide whether Python remains the mutation layer or Prism operations should be
  consolidated into Go to reduce duplicate clients and configuration behavior.
- Decide whether standalone NAI deployment is part of this product's supported scope.
- Define compatibility and migration rules for persisted settings before removing
  dormant Buzz and agent fields.
