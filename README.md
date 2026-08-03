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
the first nine sections; `0` opens Update. App Deploy and K8S are reached from
the sidebar — there are only ten number keys, and the sections that predate them
keep the shortcuts they have always had.

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
| App Deploy | Install Helm charts and manifests onto the clusters listed in K8S. Deployed apps are coloured differently from undeployed ones. |
| App Services | Where deployed apps are reachable: LoadBalancer, NodePort, and Ingress addresses, with a clickable URL. |
| K8S | List, add, and remove the Kubernetes clusters in the gateway's kubeconfig. |

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

NP4M, NRCC, MicroK8s, and Command Atlas are built in. NP4M, NRCC, and Command
Atlas advertise their default HTTPS service on port `8443`. When several custom services share a worker, AIDT assigns ports in
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
| `AIDT_MICROK8S_STORAGE` | `none` skips the storage addon (default: `hostpath`). |
| `MICROK8S_CHANNEL` | Snap channel (default `stable`). |

#### Storage

`hostpath-storage` is enabled so PersistentVolumeClaims work out of the box,
backed by the node's own disk. Without a default StorageClass every PVC stays
`Pending` and Helm charts that request persistence hang with nothing pointing at
the cause.

The class is `microk8s-hostpath`, marked default, with
`volumeBindingMode: WaitForFirstConsumer` — so a claim on its own stays `Pending`
until a pod mounts it. That is expected, not a fault. The installer proves the
whole path by binding a real claim through a throwaway pod (using an image
already on the node, so a registry problem cannot fail an otherwise healthy
deploy) and deletes it again.

Volumes live under `/var/snap/microk8s/common/default-storage`, and the
installer reports the free space there. Being node-local, the data does not
survive the node and does not follow a workload to a second node — fine for a
lab cluster, not for production. Set `AIDT_MICROK8S_STORAGE=none` to skip the
addon if you intend to attach your own CSI driver instead.

The installer writes a kubeconfig to the deploy user's `~/.kube/config`, adds
that user to the `microk8s` group, installs a standalone `kubectl` alongside the
`microk8s kubectl` alias, and enables `dns` and `helm3`. It reports the cluster
into Services as `https://<node-ip>:16443` with the MetalLB pool shown alongside
it.

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

### Command Atlas

[Command Atlas](https://github.com/script-repo/showcase/tree/main/005) is an
interactive CLI mind-map with a live terminal deck. Installer:
`command-atlas-install.sh` at the repository root.

**It brokers real PTY shells to a browser page**, which is why the app binds to
`127.0.0.1` and documents itself as never reachable from the network. That
protection is kept: the deployment leaves the app on loopback and puts an
authenticating nginx in front of it.

| Layer | Where |
| --- | --- |
| Command Atlas (`systemd: command-atlas`) | `127.0.0.1:7420`, shells run as the deploy user |
| nginx reverse proxy | `0.0.0.0:<service port>`, TLS + PAM basic auth |

Signing in requires a **real local account on that host**, checked through PAM
(`/etc/pam.d/command-atlas`). TLS is mandatory rather than cosmetic: basic auth
sends a system password on every request, and a self-signed certificate is
generated at install time — browsers warn once. The app's own token is held in a
mode-`0600` env file and injected by nginx after authentication, so it never
appears in a published link, a log line, or AIDT's saved settings.

Overrides:

| Variable | Effect |
| --- | --- |
| `AIDT_SERVICE_PORT` / `PORT` | Front-end HTTPS port (default `8443`). |
| `AIDT_ATLAS_APP_PORT` | Loopback port for the app (default `7420`). |
| `AIDT_ATLAS_USER` | Account the shells run as (default: the deploy user). |
| `AIDT_ATLAS_REF` | Git ref of `script-repo/showcase` (default `main`). |

If the nginx PAM module cannot be installed, or nginx rejects the generated
configuration, the install **fails** rather than publishing an unauthenticated
shell. Anyone with an account on that host can open a shell through this page,
so remove the deployment when you are finished with it.

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

## App Deploy And K8S

These two sections install workloads onto Kubernetes clusters. They are the
cluster-native counterpart to the custom deployments above: those provision a VM
and run a setup script, these install a chart or manifest into a cluster that
already exists.

Everything runs **on the Olla gateway**, not on the machine running AIDT. The
gateway is already the operator's bastion (see [MicroK8s](#microk8s)): `kubectl`
lives there and every cluster AIDT knows about is merged into its kubeconfig.
`helm` is installed there on first use, from the official release with its
published checksum verified. AIDT needs no local `kubectl` or `helm`.

### K8S

Lists the contexts in the gateway's `~/.kube/config`. The list is read live on
every refresh rather than cached, because it decides where workloads land and a
stale entry would mean deploying into the wrong cluster. Credentials never reach
the TUI: the contexts are read through `kubectl config view`, which redacts
certificates and tokens but keeps the names and server URLs shown here.

| Key | Action |
| --- | --- |
| `a` | Add a cluster: fetch from a MicroK8s node over SSH, or import a kubeconfig already on the gateway. |
| `x` | Remove the entry from the gateway's kubeconfig. |
| `Enter` | Make the selected cluster the gateway's current context. |
| `r` | Re-read the kubeconfig and reconcile recorded app installs. |

Removing an entry only stops AIDT reaching that cluster. It does not delete the
cluster or anything running on it. The kubeconfig is backed up to
`config.aidt-bak` before any change, and a cluster or user still referenced by
another context is left in place.

### App Deploy

Each application is either a Helm chart (repository plus chart, or an `oci://`
reference) or a manifest URL applied with `kubectl apply -f`. The distinction is
recorded on every installation, because it decides how the workload is removed
again — a definition edited after it was installed is still uninstalled the way
it was created.

| Key | Action |
| --- | --- |
| `Tab` / `Shift+Tab` | Move through the catalog (`Up`/`Down` and `k`/`j` also work). |
| `d` | Deploy: pick a cluster context, namespace, and deployment name. |
| `x` | Remove an installation from its cluster. |
| `a` / `e` | Add or edit an application definition. |
| `X` | Drop the definition from the catalog, leaving deployed workloads running. |

After a successful deploy, the application's primary Service is published on a
NodePort so it is reachable from outside the cluster and appears with a URL in
[App Services](#app-services). Charts overwhelmingly default to `ClusterIP`,
which otherwise leaves a healthy install with nowhere to browse to.

Only the *primary* Service is published — the one named after the release, or
the sole Service when there is only one. Publishing everything a release creates
would expose its dependencies too: an Open WebUI install alone would put Redis
and an Ollama API on every node address with no authentication. A headless
Service is never touched, and one already on `LoadBalancer` or `NodePort` is
left as it was.

Set **Publish after deploy** to "Do not publish" on applications with no
user-facing endpoint. The two built-in operators (CloudNativePG, Redis) ship
that way already, since their Services are webhook and metrics endpoints rather
than anything to open.

This is done by patching the Service after the install rather than by passing
`--set service.type=NodePort`. Chart value paths are not standardised — a chart
may use `service.type`, `server.service.type`, or no such key at all — and a
`--set` against a chart whose schema disagrees fails the whole deploy. Patching
also works for manifest installs, which take no values. It is re-applied on
every deploy, so a `helm upgrade` that resets the type is corrected immediately.

Patching makes kubectl the field manager for `.spec.type`, which Helm 4's
server-side apply would otherwise refuse on the next upgrade
(`conflict with "kubectl-patch" using v1: .spec.type`), so the upgrade passes
`--force-conflicts` where the installed Helm supports it. Helm reclaiming the
field drops the node port allocation, so the ports assigned by the previous
deploy are recorded beforehand and requested again — **the URL is stable across
redeploys**, which matters for any app configured with its own address. If a
remembered port has been taken in the meantime, the app is published on a fresh
one rather than left unreachable.

Rows are coloured by state: grey when the app is deployed nowhere, green once it
is installed, and amber when a recorded installation was not found in its
cluster at the last refresh. Deploying the same app to another context — or to
the same context under a different deployment name — creates a second
installation, so one application can run on many clusters at once. Helm installs
run as `upgrade --install`, so pressing `d` again converges rather than failing.

The registry of what is installed where lives in
`~/.ai-deployment-toolkit/tui.json` and is written only after a deploy or remove
exits successfully, so a failed deploy never leaves an app showing as running.

### Generated secrets

Some charts require a secret that has no sensible default. An application can
declare which values AIDT must fill with generated randomness, and one is
generated per installation on first deploy.

They are generated rather than shipped because a value hardcoded in the catalog
would be identical on every AIDT install, and they are kept rather than rotated
because handing an already-initialised database a fresh password locks the app
out of its own data. The values live in `~/.ai-deployment-toolkit/tui.json`
(mode 0600, alongside the SSH and Prism credentials) and are dropped when the
installation is removed.

They reach Helm through a `umask 077` values file rather than `--set`: the
deploy script itself travels over stdin, but a `--set` would put the secret into
helm's own argv, where any other user on the gateway could read it out of `ps`.

None of the built-ins currently need this — they all install and run on their
own — but a chart that demands a password with no usable default can declare
one rather than being deployed and then repaired by hand.

### Pointing an app at the Olla pool

An application can declare that it wants the gateway's Olla endpoints, and they
are injected into its workload after every deploy:

| Mode | Injected |
| --- | --- |
| `openai` | `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `OPENAI_MODEL` |
| `anthropic` | `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL` |
| `both` | all of the above |

Off unless an app asks for it: `OPENAI_BASE_URL` means nothing to Grafana or a
database operator. Open WebUI, LiteLLM, AnythingLLM and Langfuse ship on
`openai`; LangGraph on `both`.

This is done by setting environment on the workload after install, because
charts routinely provide no way to set arbitrary environment and the address of
an LLM gateway cannot be expressed any other way. Like the publish step it
re-applies on every deploy, so a `helm upgrade` that drops it self-corrects.

An app can also declare `SelfURLValue`, a chart value that should receive its
own external URL. Because the node port is chosen *before* helm runs, that URL
is known at install time — an app that has to be told its own address is
configured correctly on the first deploy rather than needing to be deployed,
inspected and deployed again.

Built-in applications are seeded on first launch and topped up when a later AIDT
release adds one, using the same ledger as the custom deployments — so deleting
one sticks. Bitnami charts are deliberately absent: the public Bitnami image
catalog was withdrawn in 2025 and those charts install but cannot pull their
images, so PostgreSQL is served by CloudNativePG and Redis by the
ot-container-kit operator.

### App Services

App Deploy says an application is installed; App Services says where it can be
reached. It is the Kubernetes counterpart to [Services](#tui-sections), which
covers the gateway, the Ollama workers, and VM-based custom deployments.

For every recorded installation, AIDT asks the cluster what it exposes and turns
that into an address:

| Service type | Result |
| --- | --- |
| `LoadBalancer` | `http(s)://<external-ip>:<port>` — one row per published port. A LoadBalancer with no address yet is reported as pending rather than shown blank. |
| `NodePort` | `http(s)://<node-ip>:<nodePort>`, using the cluster's own node address. |
| `Ingress` | `http(s)://<host><path>`, https when the rule carries TLS. |
| `ClusterIP` | No URL — the row gives the `kubectl port-forward` command that does reach it. |

Ports 443, 8443, and 9443 are rendered as `https`; everything else as `http`.
`Enter` or `b` opens the highlighted URL, `r` refreshes, `/` filters. The list
also refreshes when you open the section and immediately after a successful
deploy, so a new app's address is there without hunting through the deploy log.

Nothing here is persisted. A LoadBalancer IP can change and an Ingress host can
be re-pointed, so a remembered URL would eventually send you somewhere wrong —
the addresses are read from the cluster every time.

An installation that exposes nothing still appears, marked as such. That is a
real answer: a workload with only a `ClusterIP` service is running but
unreachable from outside the cluster, and saying so is more useful than omitting
the row, which would read as "not deployed". This is now the exception rather
than the rule — App Deploy publishes the primary Service on a NodePort after
every deploy — but it still happens for applications set to "Do not publish",
for anything deployed before that behaviour existed, and for services beyond the
primary one. Redeploy with `d` to publish an app installed earlier.

Scoping is best-effort. A Helm release usually stamps
`app.kubernetes.io/instance` on its objects, but that is a convention rather
than a guarantee, so when the label matches nothing AIDT falls back to listing
the namespace. In a namespace shared by two applications this can attribute a
Service to both; the row names the object and namespace so it stays possible to
tell which is which.

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

Each deploy also writes `~/.config/aidt/agent-env.sh` and sources it from
`~/.bashrc`, so an agent points at Olla in **every** shell on the host rather
than only in the one AIDT launches. Without it, the same binary started from a
plain SSH login or a brokered terminal falls back to its own default
configuration and talks to its vendor's endpoint instead. The file is mode
`0600` because it sources the per-agent credential files, and it is regenerated
on every deploy.

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
