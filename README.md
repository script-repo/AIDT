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
| Agents | Deploy and open the supported Crush and Hermes terminal agents. |
| Load | Visualize per-worker request share and active connections. |
| Nutanix | Configure Prism placement, deploy or delete VMs, and run custom deployments. |
| Services | List gateway, worker, and custom-service URLs with direct clickable links. |
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

NP4M and NRCC are built in and advertise their default HTTPS service on port
`8443`. When several custom services share a worker, AIDT assigns ports in
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

After a custom setup command completes successfully, its service URL is saved
in `~/.ai-deployment-toolkit/tui.json` and appears in Services. Failed installs
do not register a URL. Gateway and Ollama worker URLs are derived live rather
than persisted.

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

- **Crush**: installed on the gateway and configured to use the Olla OpenAI
  endpoint for the whole worker pool.
- **Hermes**: installed on a selected worker and configured to use Olla as a
  custom provider. Optional Telegram gateway settings are available.

Press `d` in the Agents section to deploy the selected agent. Press `Enter` or
`o` to open it over SSH. Removal uninstalls the selected agent from its
registered hosts.

Crush normally inventories its current directory at startup to build project
context. AIDT launches it in the dedicated
`~/.ai-deployment-toolkit/crush-workspace` directory so it does not scan the
directory from which AIDT was started.

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
