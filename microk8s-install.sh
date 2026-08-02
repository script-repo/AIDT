#!/usr/bin/env bash
# install-microk8s.sh — MicroK8s + Helm + MetalLB on Ubuntu.
#
# MetalLB needs a block of addresses on the node's own L2 subnet that nothing
# else answers for. This script prefers x.x.x.81-.85 and falls back to the first
# free five-address window it can find.
#
# Overrides:
#   AIDT_METALLB_RANGE   skip discovery entirely, e.g. 10.0.0.90-10.0.0.94
#   AIDT_METALLB_START   preferred last octet for the window (default 81)
#   AIDT_METALLB_FORCE=1 re-enable MetalLB even if it is already on
#   MICROK8S_CHANNEL     snap channel (default: stable)
set -euo pipefail

POOL_SIZE=5
PREFERRED_START="${AIDT_METALLB_START:-81}"
MICROK8S_CHANNEL="${MICROK8S_CHANNEL:-stable}"

log() { echo "[microk8s] $*"; }
die() {
	echo "[microk8s] ERROR: $*" >&2
	exit 1
}

# --- platform -----------------------------------------------------------------
[ -r /etc/os-release ] || die "cannot read /etc/os-release; unsupported system"
. /etc/os-release
[ "${ID:-}" = "ubuntu" ] || die "this deployment supports Ubuntu only (found '${ID:-unknown}'). MicroK8s is delivered as a snap; use a different image."

if [ "$(id -u)" -eq 0 ]; then
	SUDO=""
	TARGET_USER="${SUDO_USER:-root}"
elif command -v sudo >/dev/null 2>&1; then
	SUDO="sudo -n"
	TARGET_USER="$(id -un)"
else
	die "root or passwordless sudo is required"
fi
TARGET_HOME="$(getent passwd "$TARGET_USER" | cut -d: -f6)"
[ -n "$TARGET_HOME" ] || TARGET_HOME="/root"

log "installing on $(hostname) as ${TARGET_USER}"

# --- dependencies -------------------------------------------------------------
log "installing dependencies…"
$SUDO apt-get update -y >/dev/null
$SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y \
	snapd iproute2 iputils-ping python3 ca-certificates curl >/dev/null
command -v snap >/dev/null 2>&1 || die "snapd is not available after installation"

# --- network discovery --------------------------------------------------------
DEF_ROUTE="$(ip -4 route show default | head -1 || true)"
[ -n "$DEF_ROUTE" ] || die "no IPv4 default route; cannot determine the node subnet"
IFACE="$(awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}' <<<"$DEF_ROUTE")"
GATEWAY="$(awk '{for(i=1;i<=NF;i++) if($i=="via"){print $(i+1); exit}}' <<<"$DEF_ROUTE")"
[ -n "$IFACE" ] || die "could not determine the primary interface"

CIDR="$(ip -4 -o addr show dev "$IFACE" scope global | awk '{print $4; exit}')"
[ -n "$CIDR" ] || die "interface $IFACE has no global IPv4 address"
NODE_IP="${CIDR%/*}"

log "node ${NODE_IP} on ${IFACE} (${CIDR}), gateway ${GATEWAY:-none}"

# --- MetalLB address pool -----------------------------------------------------
# Probe: ping the address to force the kernel to ARP for it, then look for a
# resolved lladdr in the neighbour table. This catches hosts that answer ARP but
# drop ICMP, which a plain ping sweep would miss entirely.
find_pool() {
	python3 - "$CIDR" "$NODE_IP" "${GATEWAY:-}" "$PREFERRED_START" "$POOL_SIZE" <<'PYEOF'
import ipaddress, subprocess, sys
from concurrent.futures import ThreadPoolExecutor

cidr, node_ip, gateway, preferred, size = sys.argv[1:6]
preferred, size = int(preferred), int(size)
net = ipaddress.ip_network(cidr, strict=False)

reserved = {ipaddress.ip_address(node_ip)}
if gateway:
    try:
        reserved.add(ipaddress.ip_address(gateway))
    except ValueError:
        pass

hosts = list(net.hosts())
if len(hosts) < size:
    print("ERROR subnet %s is too small for a %d-address pool" % (net, size), file=sys.stderr)
    sys.exit(2)

def in_use(ip):
    """True if anything answers for this address."""
    s = str(ip)
    subprocess.run(["ping", "-c", "1", "-W", "1", "-n", s],
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        out = subprocess.run(["ip", "neigh", "show", s],
                             capture_output=True, text=True, timeout=5).stdout
    except subprocess.SubprocessError:
        return True          # fail closed: never hand out an address we could not test
    return "lladdr" in out and "FAILED" not in out

def probe(candidates):
    """Return the subset that looks free, confirmed by a second pass."""
    with ThreadPoolExecutor(max_workers=32) as pool:
        first = [ip for ip, used in zip(candidates, pool.map(in_use, candidates)) if not used]
    if not first:
        return set()
    # A single miss can be a dropped ARP reply, so only trust an address that
    # stays quiet across two independent passes.
    with ThreadPoolExecutor(max_workers=32) as pool:
        return {ip for ip, used in zip(first, pool.map(in_use, first)) if not used}

def window(start_ip):
    """The `size` consecutive addresses from start_ip, or None if out of range."""
    try:
        ips = [start_ip + i for i in range(size)]
    except ipaddress.AddressValueError:
        return None
    if any(ip not in net or ip == net.broadcast_address or ip == net.network_address for ip in ips):
        return None
    if any(ip in reserved for ip in ips):
        return None
    return ips

# 1. The preferred window, if it exists in this subnet.
base = net.network_address
pref = window(base + preferred) if (base + preferred) in net else None
if pref and probe(pref) == set(pref):
    print("%s %s preferred" % (pref[0], pref[-1]))
    sys.exit(0)

# 2. Otherwise sweep the subnet once, then take the first free window at or
#    above the preferred address, wrapping to the bottom only if nothing higher
#    is free. Searching outward from the requested range keeps the pool in
#    whatever part of the subnet the operator set aside for static addresses;
#    scanning from the bottom would hand back .2-.6, which is normally where
#    the gateway and the DHCP scope live.
free = probe([h for h in hosts if h not in reserved])
pref_addr = base + preferred
ordered = [h for h in hosts if h >= pref_addr] + [h for h in hosts if h < pref_addr]
for start in ordered:
    w = window(start)
    if w and all(ip in free for ip in w):
        print("%s %s fallback" % (w[0], w[-1]))
        sys.exit(0)

print("ERROR no %d consecutive free addresses in %s" % (size, net), file=sys.stderr)
sys.exit(3)
PYEOF
}

if [ -n "${AIDT_METALLB_RANGE:-}" ]; then
	POOL="$AIDT_METALLB_RANGE"
	log "using operator-supplied pool: $POOL"
else
	log "scanning ${CIDR} for ${POOL_SIZE} consecutive free addresses (preferring .${PREFERRED_START})…"
	RESULT="$(find_pool)" || die "could not find a free ${POOL_SIZE}-address range in ${CIDR}. Set AIDT_METALLB_RANGE=<start>-<end> to choose one manually."
	POOL_START="$(awk '{print $1}' <<<"$RESULT")"
	POOL_END="$(awk '{print $2}' <<<"$RESULT")"
	HOW="$(awk '{print $3}' <<<"$RESULT")"
	POOL="${POOL_START}-${POOL_END}"
	if [ "$HOW" = "preferred" ]; then
		log "pool: ${POOL} (preferred range was free)"
	else
		log "pool: ${POOL} (preferred .${PREFERRED_START} range was in use; using first free window)"
	fi
fi

# --- MicroK8s -----------------------------------------------------------------
if snap list microk8s >/dev/null 2>&1; then
	log "MicroK8s already installed"
else
	log "installing MicroK8s (channel ${MICROK8S_CHANNEL})…"
	$SUDO snap install microk8s --classic --channel="$MICROK8S_CHANNEL"
fi

log "waiting for MicroK8s to become ready…"
$SUDO microk8s status --wait-ready --timeout 300 >/dev/null ||
	die "MicroK8s did not become ready within 300s (check: sudo microk8s inspect)"

if [ "$TARGET_USER" != "root" ]; then
	$SUDO usermod -a -G microk8s "$TARGET_USER"
	$SUDO mkdir -p "$TARGET_HOME/.kube"
	$SUDO chown -R "$TARGET_USER:$TARGET_USER" "$TARGET_HOME/.kube"
fi

log "enabling dns…"
$SUDO microk8s enable dns >/dev/null

# --- MetalLB ------------------------------------------------------------------
if $SUDO microk8s status --format short 2>/dev/null | grep -q 'metallb: enabled' && [ "${AIDT_METALLB_FORCE:-0}" != "1" ]; then
	log "MetalLB already enabled; leaving its existing pool untouched (AIDT_METALLB_FORCE=1 to override)"
else
	log "enabling MetalLB with pool ${POOL}…"
	$SUDO microk8s enable "metallb:${POOL}"
fi

# --- Helm ---------------------------------------------------------------------
if command -v helm >/dev/null 2>&1; then
	log "helm already installed: $(command -v helm)"
else
	log "installing Helm…"
	$SUDO snap install helm --classic
fi
$SUDO microk8s enable helm3 >/dev/null 2>&1 || true

# Point the standalone helm/kubectl at the MicroK8s cluster.
KUBECONFIG_PATH="$TARGET_HOME/.kube/config"
log "writing kubeconfig to ${KUBECONFIG_PATH}…"
$SUDO mkdir -p "$TARGET_HOME/.kube"
$SUDO microk8s config | $SUDO tee "$KUBECONFIG_PATH" >/dev/null
$SUDO chmod 0600 "$KUBECONFIG_PATH"
[ "$TARGET_USER" != "root" ] && $SUDO chown "$TARGET_USER:$TARGET_USER" "$KUBECONFIG_PATH"

# --- report -------------------------------------------------------------------
log "verifying…"
$SUDO microk8s kubectl get nodes || true
echo

# Machine-readable record. AIDT parses this line to register the cluster in its
# Services menu with the API endpoint and the MetalLB pool it actually got.
printf 'AIDT_SERVICE_INFO {"url":"https://%s:16443","detail":"cluster %s · MetalLB pool %s"}\n' \
	"$NODE_IP" "$NODE_IP" "$POOL"

log "=========================================="
log " MicroK8s ready on $(hostname) (${NODE_IP})"
log " MetalLB pool : ${POOL}"
log " Helm         : $(helm version --short 2>/dev/null || echo 'installed (re-login for group access)')"
log " kubeconfig   : ${KUBECONFIG_PATH}"
log "=========================================="
log "The pool was chosen by probing for addresses nothing answers for. A"
log "powered-off host or a DHCP reservation can still look free — confirm"
log "${POOL} is excluded from DHCP before relying on it."
if [ "$TARGET_USER" != "root" ]; then
	log "Log out and back in (or run 'newgrp microk8s') to use microk8s without sudo."
fi
