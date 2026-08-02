# AI Deployment Toolkit

AIDT is a terminal UI for deploying and operating an OpenAI-compatible LLM
pool on Nutanix AHV. It places an
[Olla](https://github.com/thushan/olla) gateway in front of one or more
[Ollama](https://ollama.com) worker VMs.

```text
OpenAI-compatible clients
        |
        v
Olla gateway VM (:40114)
        |
        +-- Ollama worker VM (:11434)
        +-- Ollama worker VM (:11434)
        +-- ...
```

The TUI can provision VMs through the Prism Central v4 API, install Olla and
Ollama as systemd services, manage endpoints and models, stream chat responses,
show load-balancing metrics, deploy supported terminal agents, and run common
maintenance workflows.

## Install

Prebuilt binaries for Windows, Linux, and macOS on amd64 and arm64 are
published on the [GitHub Releases](https://github.com/script-repo/AIDT/releases)
page. Release archives include the Python and shell helpers required by the
deployment workflows.

### Linux And macOS

```bash
curl -fsSL https://raw.githubusercontent.com/script-repo/AIDT/HEAD/scripts/install.sh | sh
```

With `wget`:

```bash
wget -qO- https://raw.githubusercontent.com/script-repo/AIDT/HEAD/scripts/install.sh | sh
```

### Windows

```powershell
irm https://raw.githubusercontent.com/script-repo/AIDT/HEAD/scripts/install.ps1 | iex
```

The installers:

- Detect the operating system and architecture.
- Install AIDT under `~/.ai-deployment-toolkit` on Unix or
  `%LOCALAPPDATA%\aidt` on Windows.
- Add the binary to the user's `PATH`.
- Best-effort install OpenSSH, Python, and a dedicated virtual environment with
  `requests` and `paramiko`.

Set `AIDT_SKIP_DEPS=1` to skip dependency setup. Set `AIDT_VERSION=vX.Y.Z` to
install a specific release. The core gateway, pool, model, chat, and load views
only require the static AIDT binary.

## Build From Source

Requirements:

- Go 1.25 or newer.
- Python 3 with the packages in `requirements.txt` for Nutanix mutations.
- OpenSSH for remote consoles, agent deployment, and maintenance actions.

```bash
cd tui
go test ./...
go build -o aidt .
./aidt
```

On Windows, build and run `aidt.exe` instead.

## First Launch

If no gateway is configured, AIDT first probes the local machine for Olla on
port `40114`. This lets an AIDT installation on a gateway VM connect without an
extra setup step. If no local gateway is found, the Connect form requests the
gateway URL and SSH credentials.

Values can be supplied in advance:

```bash
./aidt \
  --gateway http://gateway-host:40114 \
  --ssh-user rocky \
  --ssh-password 'your-ssh-password'
```

Equivalent environment variables are:

```text
OLLA_GATEWAY
OLLA_SSH_USER
OLLA_SSH_PASSWORD
```

Settings are persisted in `~/.ai-deployment-toolkit/tui.json`. Review
[`FINDINGS.md`](FINDINGS.md) before using the current implementation outside a
trusted lab environment; it records known credential, transport, and remote
execution risks.

## TUI Sections

AIDT uses a left sidebar and a focused content pane. Press `Enter` to focus a
section and `Esc` to return to the sidebar. Number keys `1` through `9` jump to
the first nine sections; `0` opens Update.

| Section | Purpose |
| --- | --- |
| Dashboard | Live request, latency, throughput, health, and uptime metrics. |
| Pool | View, add, and remove Ollama endpoints from the Olla configuration. |
| Models | List, pull, delete, warm, and select models across workers. |
| Chat | Stream OpenAI-compatible chat responses with bounded session history and optional URL fetch context. |
| Agents | Deploy and open Crush, OpenCode, Goose, Grok Build, Claude Code, Codex, and Hermes. |
| Load | Visualize per-worker request share and active connections. |
| Nutanix | Configure Prism placement, deploy or delete VMs, and run custom deployments. |
| Services | List gateway, worker, agent-server, and custom-service URLs with direct clickable links. `x` removes a custom-service listing without touching the workload. |
| Access | Show client endpoint values, model selection, and example requests. |
| Update | Update AIDT, guests, Olla, Ollama, agents, images, and Ollama cloud keys. |

Common keys:

| Key | Action |
| --- | --- |
| `Up` / `Down` or `k` / `j` | Move selection. |
| `Enter` | Open, focus, or confirm. |
| `Esc` | Return or cancel. |
| `/` | Filter the active list. |
| `c` | Open the Connect form from the sidebar. |
| `r` | Refresh. |
| `?` | Toggle expanded help. |
| `q` or `Ctrl+C` | Quit. |

The footer shows context-specific bindings for the active section.

## Deployment Workflows

The Go TUI performs inventory reads and delegates mutating Nutanix operations
to `scripts/nutanix_olla_vm.py`.

### Pattern A: Olla Gateway

Pattern A creates a VM, waits for its assigned address, installs Olla, and
installs AIDT on the gateway unless explicitly disabled. The installed gateway
can then manage itself and its worker pool.

```bash
python scripts/nutanix_olla_vm.py pattern-a \
  --vm-name aidt-gateway-01 \
  --image-name "$AIDT_IMAGE_NAME" \
  --cluster-name "$AIDT_CLUSTER_NAME" \
  --subnet-name "$AIDT_SUBNET_NAME"
```

### Pattern B: Ollama Worker

Pattern B creates a worker VM, installs Ollama, pulls the requested model, and
registers the worker with Olla.

```bash
python scripts/nutanix_olla_vm.py pattern-b \
  --vm-name aidt-worker-01 \
  --model nemotron-3-super:cloud \
  --olla-url http://gateway-host:40114
```

The TUI can deploy multiple workers concurrently. Each worker emits an
`AIDT_ENDPOINT` record and the TUI registers the completed batch with one
gateway configuration update.

### Custom Deployment

Custom deployment definitions run an operator-provided URL or shell command.
Open Nutanix, press `c`, and select a definition:

- Press `Enter` to provision a new VM and install the workload there.
- Press `w` to choose an existing registered Ollama worker and install the
  workload alongside Ollama using managed SSH access.

NP4M, NRCC, and MicroK8s are built in. NP4M and NRCC advertise their default
HTTPS service on port `8443`. When several custom services share a worker, AIDT assigns ports in
sequence (`8443`, `8444`, `8445`, and so on through `8543`). Redeploying the
same service reuses its registered port. NP4M and NRCC receive the assigned port
through their supported installer environment variables. Custom definitions are
intentionally arbitrary remote execution and should only use trusted, reviewed
sources; generic installers can read `PORT` or `AIDT_SERVICE_PORT`.

Before NP4M runs on Ubuntu or Debian, AIDT installs `python3-venv`, detects the
highest installed Python version, installs its matching `pythonX.Y-venv`
package, and verifies that the interpreter can create a virtual environment.

```bash
python scripts/nutanix_olla_vm.py pattern-custom \
  --script-url https://example.com/setup.sh \
  --name-prefix example-node- \
  --image-name "$AIDT_IMAGE_NAME" \
  --cluster-name "$AIDT_CLUSTER_NAME" \
  --subnet-name "$AIDT_SUBNET_NAME"
```

### MicroK8s

The built-in MicroK8s deployment targets **Ubuntu** guests (MicroK8s ships as a
snap) and installs MicroK8s, Helm, and MetalLB in one pass. Its installer is
`microk8s-install.sh` at the repository root, fetched by the guest the same way
the other built-ins fetch theirs.

MetalLB needs a block of addresses on the node's own L2 subnet that nothing else
answers for. The installer derives the subnet from the node's default route,
then prefers `x.x.x.81-.85`. If any of those five are taken it searches outward
from `.81` to the top of the subnet, and only wraps to lower addresses as a last
resort — staying near the requested range keeps the pool in whatever part of the
subnet is reserved for statics instead of landing on the DHCP scope.

Availability is tested by pinging each address to force an ARP resolution and
then checking the neighbour table, so hosts that answer ARP but drop ICMP are
still detected. Every candidate must stay silent across two passes before it is
used. This cannot see a powered-off host or a DHCP reservation, so **confirm the
reported range is excluded from DHCP** before relying on it.

Overrides, set on the deployment definition or in the guest environment:

| Variable | Effect |
| --- | --- |
| `AIDT_METALLB_RANGE` | Skip discovery, e.g. `10.0.0.90-10.0.0.94`. |
| `AIDT_METALLB_START` | Preferred first octet of the window (default `81`). |
| `AIDT_METALLB_FORCE=1` | Re-enable MetalLB even if it is already on. |
| `MICROK8S_CHANNEL` | Snap channel (default `stable`). |

The installer writes a kubeconfig to the deploy user's `~/.kube/config`, adds
that user to the `microk8s` group, installs a standalone `kubectl` alongside the
`microk8s kubectl` alias, and enables `dns` and `helm3`. It reports the cluster
into Services as `https://<node-ip>:16443` with the MetalLB pool shown alongside
it. No storage addon is enabled.

#### Bastion access

A cluster is only useful once something can reach it, so after a successful
MicroK8s deploy AIDT turns the **Olla gateway into a bastion**: it installs
`kubectl` there and merges the new cluster into the gateway user's
`~/.kube/config`. SSH to the gateway and the cluster is already selected:

```bash
kubectl get nodes
kubectl --context <vm-name> get nodes    # when several clusters are registered
```

`kubectl` is installed from the official release binary with its published
SHA-256 verified, so this works on the Rocky guests AIDT provisions as well as
on Ubuntu.

Each cluster is merged under names derived from its VM (`<vm-name>`,
`<vm-name>-cluster`, `<vm-name>-admin`). `microk8s config` always emits the same
three names — `microk8s`, `microk8s-cluster`, and `admin` — so without renaming,
a second cluster would silently overwrite the first. Existing contexts in the
gateway's kubeconfig are preserved by the merge.

The merge never destroys an existing kubeconfig. The previous file is saved as
`~/.kube/config.aidt-bak`, and a standalone copy of the new cluster is always
written to `~/.kube/aidt-<context>.conf`.

If the gateway's existing kubeconfig references a certificate file that no
longer exists, the preferred merge (which inlines certificates) cannot read it.
AIDT falls back to merging without inlining, so one stale entry cannot block a
new cluster. If even that fails, the existing config is left untouched and the
cluster is used through its standalone file:

```bash
export KUBECONFIG=~/.kube/aidt-<context>.conf
kubectl get nodes
```

Any `KUBECONFIG` exported in the environment is ignored: the merge always
targets `~/.kube/config`, so an inherited value cannot silently redirect the
`use-context` write to a file that was never backed up.

The kubeconfig contains admin credentials. It is read from the node and written
to the gateway over SSH stdin, never as a command argument and never into the
deploy log, and it lands in a mode-`0600` file. Bastion setup is best effort: if
it fails, the cluster is still deployed and registered, and the error is
reported in Output.

After a custom setup command completes successfully, its service URL is saved
in `~/.ai-deployment-toolkit/tui.json` and appears in Services. A setup script
can also print `AIDT_SERVICE_INFO {"url":"…","detail":"…"}` to publish an
endpoint AIDT could not know in advance, which is how MicroK8s reports its API
address and address pool. Failed installs
do not register a URL. Gateway and Ollama worker URLs are derived live rather
than persisted. When Prism inventory is available, the gateway service uses its
VM name (for example, `aidt-gateway-03`) instead of a generic label.

### Local Olla Installation

On Linux, the Nutanix section can install Olla on the machine running AIDT
instead of creating a gateway VM. This executes
`scripts/remote/install-olla.sh` as root or through non-interactive `sudo`.

## Prism Configuration

The TUI reads Prism configuration from its persisted settings and can fall back
to `~/.cursor/mcp.json`. The standalone Python helper accepts Prism credentials
through environment variables:

```bash
export PRISM_CENTRAL_URL="https://prism-central-host:9440"
export PRISM_USER="admin"
export PRISM_PASSWORD="********"
export AIDT_VM_PASSWORD="guest-password"
export AIDT_IMAGE_NAME="Rocky-9-GenericCloud-Base.latest.x86_64.qcow2"
export AIDT_CLUSTER_NAME="cluster-name"
export AIDT_SUBNET_NAME="subnet-name"
```

API-key authentication is also supported through `PRISM_API_KEY`. There are no
baked-in image, cluster, subnet, or guest-password defaults.

The helper supports the following commands:

```text
pattern-a
pattern-b
pattern-custom
seed-image
install-a
install-b
delete
show
next-name
register-endpoints
```

Run `python scripts/nutanix_olla_vm.py <command> --help` for command-specific
arguments.

## Models And Chat

Olla acts as the OpenAI-compatible gateway. Model inventory, pulls, deletes,
and warming are performed directly against the Ollama workers discovered from
the gateway configuration.

Chat requests stream through:

```text
/olla/openai/v1/chat/completions
```

The current session replays at most 24 recent turns and approximately 24,000
characters. Prompts can include up to three URLs; fetched text is supplied as
additional model context. URL fetch should be treated as an untrusted-input
feature and is covered by the security backlog in `FINDINGS.md`.

## Agents

The current catalog contains:

- **Crush**: configured to use the Olla OpenAI endpoint for the whole worker
  pool.
- **OpenCode**: uses an AIDT-owned provider config that selects Olla and the
  current default model. Deployments also run an OpenCode server on port `4096`
  and register its URL in Services. The server uses Basic Auth with username
  `opencode` and the API token active at deployment time as its password (`olla`
  when no token is configured).
- **Goose**: uses an Olla custom provider through its official OpenAI-compatible
  provider format.
- **Grok Build**: installed from xAI's official installer with an isolated
  `GROK_HOME` that selects Olla by default.
- **Claude Code**: launched through Olla's Anthropic Messages API translator
  using the current default model.
- **Codex**: installed from OpenAI's official installer and launched with an
  isolated `CODEX_HOME` configured for Olla's OpenAI-compatible endpoint.
- **Hermes**: configured to use Olla as a custom provider. Optional Telegram
  gateway settings are available.

Press `d` in the Agents section to deploy the selected agent, then choose the
gateway, one discovered worker, or all discovered workers. Press `Enter` or `o`
to choose and open a registered installation over SSH. Removal uninstalls the
selected agent from one or all registered hosts. Removing a gateway or worker
VM through AIDT also clears any agent registrations on that host. If a host was
already deleted externally, Remove still clears the stale registration and
reports remote cleanup as unconfirmed.

Before installing an agent, AIDT checks for Obsidian and installs the official
AppImage when it is absent. Every agent launches in the shared Obsidian vault
at `~/Obsidian/AIDT-Agent-Vault`, so generated notes and project context remain
available to Obsidian and to the other deployed agents. AIDT stores agent Olla
provider configuration in mode-`0600` files so credentials are not exposed in
long-lived SSH command arguments.

## The Shared Agent Vault

Deploying any agent scaffolds `~/Obsidian/AIDT-Agent-Vault` — a shared
workspace that gives every agent on the host one knowledge base, one skill
library, one roster, and one work queue.

```text
AIDT-Agent-Vault/
├── AGENTS.md          schema: what the vault is and how to maintain it
├── raw/               immutable sources — read only
├── wiki/              agent-maintained pages (index, log, concepts,
│                      entities, sources, comparisons)
├── skills/            REGISTRY.md + one directory per documented skill
├── agents/            REGISTRY.md + a capability card per agent
├── tasks/             QUEUE.md + open/ claimed/ done/ failed/
├── outputs/           reports and lint results
└── bin/               aidt-agent, aidt-skill, aidt-task
```

### Knowledge — the LLM-Wiki pattern

`raw/` and `wiki/` implement Karpathy's LLM Wiki: raw sources are immutable and
are compiled **once** into interlinked markdown pages, so agents query the wiki
instead of re-reading sources on every question. `AGENTS.md` is the schema
layer — page types, YAML frontmatter, `[[wikilink]]` conventions, and the
ingest / query / lint operations. Agents read it first.

### Skills — check before installing

`skills/REGISTRY.md` indexes every capability already available on the host.
Seven agents share one machine, so the rule is: look before you install.

```bash
aidt-skill list              # what already exists
aidt-skill show <name>       # read it before using it
aidt-skill new <name>        # scaffold from TEMPLATE.md (status: experimental)
aidt-skill register          # rebuild REGISTRY.md from every SKILL.md
```

A skill documents its prerequisites, exact invocation, and failure modes.
`vault-wiki`, `task-queue`, `agent-registry`, and `olla-pool` ship documented.

### Identity and work

Each deploy registers the agent's card in `agents/<id>.md` with its CLI,
endpoint, model, and capabilities; `agents/<id>.notes.md` belongs to the agent
and is never overwritten. Agents launch with `AIDT_AGENT_ID` set and `bin/` on
`PATH`.

```bash
aidt-agent whoami / list / show <id>
aidt-task list --me          # only tasks this agent is eligible for
aidt-task claim <id>         # atomic: exactly one agent can ever win
aidt-task done <id> "summary"
```

Tasks route on `for:` (`any`, an agent id, or `capability:<name>`) and on
`requires:`, which must name skills present and not `broken` in the registry.
Claiming is an atomic `mkdir`, so concurrent agents cannot both acquire a task.

Scaffolding is idempotent: documentation and queue state are written only when
absent, so agent work survives a redeploy, while `bin/` helpers are refreshed
every time. **The vault is per host** — a gateway vault and a worker vault are
not synchronized.

## Runtime State

AIDT stores local state under `~/.ai-deployment-toolkit`:

| Path | Purpose |
| --- | --- |
| `tui.json` | TUI connection, deployment, agent, model, and custom deployment settings. |
| `usage.json` | Per-model chat usage statistics. |
| `state.json` | Python helper gateway and endpoint state. |
| `keys/id_ed25519` | Managed SSH key used for deployed guests. |

New VMs receive the managed public key through cloud-init. Initial provisioning
also uses the configured guest password.

## Releases

GoReleaser builds static binaries for Linux, Windows, and macOS on amd64 and
arm64. Archives include the binary, install scripts, Python helper, remote
scripts, requirements, and this README.

The GitHub Actions release workflow currently creates the next patch tag for a
push to `main` and publishes the corresponding release. Known release-process
hardening work is tracked in `FINDINGS.md`.
