#!/usr/bin/env bash
# command-atlas-install.sh — Command Atlas (script-repo/showcase/005) behind an
# authenticating TLS reverse proxy.
#
# Command Atlas brokers real PTY shells to a browser page, which is why it binds
# to 127.0.0.1 and refuses to be reachable from the network. That protection is
# left in place: the app keeps listening on loopback, and nginx terminates TLS on
# the network interface, authenticates against local Linux accounts through PAM,
# and proxies through. Reaching a shell therefore needs a real OS account on this
# host, not just the URL.
#
# Overrides:
#   AIDT_SERVICE_PORT / PORT   front-end HTTPS port (default 8443)
#   AIDT_ATLAS_APP_PORT        loopback port for the app itself (default 7420)
#   AIDT_ATLAS_USER            account the shells run as (default: deploy user)
#   AIDT_ATLAS_REF             git ref of script-repo/showcase (default main)
set -euo pipefail

FRONT_PORT="${AIDT_SERVICE_PORT:-${PORT:-8443}}"
APP_PORT="${AIDT_ATLAS_APP_PORT:-7420}"
ATLAS_REF="${AIDT_ATLAS_REF:-main}"
REPO_URL="https://github.com/script-repo/showcase.git"
INSTALL_DIR="/opt/command-atlas"
APP_DIR="$INSTALL_DIR/005"
ENV_FILE="/etc/command-atlas/atlas.env"

log() { echo "[atlas] $*"; }
die() {
	echo "[atlas] ERROR: $*" >&2
	exit 1
}

# --- platform -----------------------------------------------------------------
[ -r /etc/os-release ] || die "cannot read /etc/os-release"
. /etc/os-release

if [ "$(id -u)" -eq 0 ]; then
	SUDO=""
	RUN_AS="${SUDO_USER:-root}"
elif command -v sudo >/dev/null 2>&1; then
	SUDO="sudo -n"
	RUN_AS="$(id -un)"
else
	die "root or passwordless sudo is required"
fi
ATLAS_USER="${AIDT_ATLAS_USER:-$RUN_AS}"
id "$ATLAS_USER" >/dev/null 2>&1 || die "no such user: $ATLAS_USER"

case "${ID:-} ${ID_LIKE:-}" in
*ubuntu* | *debian*) FAMILY=debian ;;
*rhel* | *fedora* | *centos* | *rocky* | *almalinux*) FAMILY=rhel ;;
*) die "unsupported distribution '${ID:-unknown}' (need Ubuntu/Debian or Rocky/RHEL)" ;;
esac

log "installing on $(hostname) — shells will run as '$ATLAS_USER'"
if [ "$ATLAS_USER" = "root" ]; then
	log "WARNING: shells will run as root. Set AIDT_ATLAS_USER to a normal account to limit them."
fi

# --- dependencies -------------------------------------------------------------
# The PAM auth module is what makes this safe to expose. If it cannot be
# installed we stop, rather than quietly publishing an unauthenticated shell.
log "installing dependencies…"
if [ "$FAMILY" = debian ]; then
	$SUDO apt-get update -y >/dev/null
	$SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y \
		git curl ca-certificates build-essential python3 openssl \
		nginx libnginx-mod-http-auth-pam libpam-modules >/dev/null
	NGINX_USER=www-data
	SHADOW_GROUP=shadow
else
	$SUDO dnf install -y epel-release >/dev/null 2>&1 || true
	$SUDO dnf install -y \
		git curl ca-certificates gcc-c++ make python3 openssl \
		nginx nginx-mod-http-auth-pam pam >/dev/null ||
		die "could not install nginx with the PAM auth module (EPEL provides nginx-mod-http-auth-pam)"
	NGINX_USER=nginx
	SHADOW_GROUP=shadow
fi

# Node 18+ is required; distro packages are often older.
NODE_OK=0
if command -v node >/dev/null 2>&1; then
	NODE_MAJOR="$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null || echo 0)"
	[ "$NODE_MAJOR" -ge 18 ] 2>/dev/null && NODE_OK=1
fi
if [ "$NODE_OK" -ne 1 ]; then
	log "installing Node.js 22…"
	if [ "$FAMILY" = debian ]; then
		curl -fsSL https://deb.nodesource.com/setup_22.x | $SUDO -E bash - >/dev/null
		$SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y nodejs >/dev/null
	else
		curl -fsSL https://rpm.nodesource.com/setup_22.x | $SUDO -E bash - >/dev/null
		$SUDO dnf install -y nodejs >/dev/null
	fi
fi
command -v node >/dev/null 2>&1 || die "node is missing after installation"
log "node $(node --version)"

# --- application --------------------------------------------------------------
log "fetching Command Atlas from script-repo/showcase (${ATLAS_REF})…"
if [ -d "$INSTALL_DIR/.git" ]; then
	$SUDO git -C "$INSTALL_DIR" fetch --depth 1 origin "$ATLAS_REF" >/dev/null
	$SUDO git -C "$INSTALL_DIR" reset --hard "origin/$ATLAS_REF" >/dev/null
else
	$SUDO rm -rf "$INSTALL_DIR"
	$SUDO git clone --depth 1 --branch "$ATLAS_REF" "$REPO_URL" "$INSTALL_DIR" >/dev/null
fi
[ -f "$APP_DIR/server.js" ] || die "$APP_DIR/server.js not found in the repository"
$SUDO chown -R "$ATLAS_USER" "$INSTALL_DIR"

# node-pty ships a native addon; the committed node_modules was built elsewhere.
log "installing node dependencies (node-pty compiles a native addon)…"
$SUDO -u "$ATLAS_USER" env HOME="$(getent passwd "$ATLAS_USER" | cut -d: -f6)" \
	npm --prefix "$APP_DIR" install --no-audit --no-fund >/dev/null ||
	die "npm install failed (build tools or network?)"

# --- token --------------------------------------------------------------------
# Pinned so the published URL keeps working across restarts. nginx injects it,
# so it never appears in a link, a log line, or AIDT's saved settings.
$SUDO mkdir -p "$(dirname "$ENV_FILE")"
if $SUDO test -s "$ENV_FILE"; then
	log "reusing the existing token"
else
	log "generating an access token…"
	printf 'ATLAS_TOKEN=%s\nPORT=%s\n' "$(openssl rand -hex 24)" "$APP_PORT" |
		$SUDO tee "$ENV_FILE" >/dev/null
fi
$SUDO chmod 600 "$ENV_FILE"
$SUDO chown "$ATLAS_USER" "$ENV_FILE"
ATLAS_TOKEN="$($SUDO sed -n 's/^ATLAS_TOKEN=//p' "$ENV_FILE" | head -1)"
[ -n "$ATLAS_TOKEN" ] || die "could not read the access token"

# --- service ------------------------------------------------------------------
log "installing the command-atlas service…"
$SUDO tee /etc/systemd/system/command-atlas.service >/dev/null <<UNIT
[Unit]
Description=Command Atlas terminal (loopback only; reached through nginx)
After=network.target

[Service]
Type=simple
User=$ATLAS_USER
WorkingDirectory=$APP_DIR
EnvironmentFile=$ENV_FILE
ExecStart=$(command -v node) $APP_DIR/server.js
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
$SUDO systemctl daemon-reload
$SUDO systemctl enable --now command-atlas >/dev/null
sleep 2
$SUDO systemctl is-active --quiet command-atlas ||
	die "command-atlas failed to start (journalctl -u command-atlas)"

# --- PAM ----------------------------------------------------------------------
# nginx must be able to verify local passwords, which means reading the shadow
# database through PAM's helper.
log "configuring PAM authentication against local accounts…"
$SUDO tee /etc/pam.d/command-atlas >/dev/null <<'PAMEOF'
auth    required pam_unix.so
account required pam_unix.so
PAMEOF
$SUDO usermod -a -G "$SHADOW_GROUP" "$NGINX_USER" 2>/dev/null ||
	log "NOTE: could not add $NGINX_USER to $SHADOW_GROUP; PAM auth may fail"

# --- TLS ----------------------------------------------------------------------
# Self-signed, but the point is encryption: basic auth sends a real system
# password on every request, and plain HTTP would put it on the wire in base64.
CERT_DIR="/etc/command-atlas/tls"
$SUDO mkdir -p "$CERT_DIR"
if ! $SUDO test -s "$CERT_DIR/server.crt"; then
	log "generating a self-signed certificate…"
	NODE_IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')"
	[ -n "$NODE_IP" ] || NODE_IP="127.0.0.1"
	$SUDO openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
		-subj "/CN=$(hostname)" \
		-addext "subjectAltName=DNS:$(hostname),IP:$NODE_IP,IP:127.0.0.1" \
		-keyout "$CERT_DIR/server.key" -out "$CERT_DIR/server.crt" >/dev/null 2>&1 ||
		die "certificate generation failed"
fi
$SUDO chmod 600 "$CERT_DIR/server.key"
$SUDO chmod 644 "$CERT_DIR/server.crt"

# --- reverse proxy ------------------------------------------------------------
log "configuring nginx on :${FRONT_PORT} (TLS + PAM basic auth)…"
if [ "$FAMILY" = debian ]; then
	SITE=/etc/nginx/sites-available/command-atlas.conf
	$SUDO mkdir -p /etc/nginx/sites-enabled
else
	SITE=/etc/nginx/conf.d/command-atlas.conf
fi

$SUDO tee "$SITE" >/dev/null <<NGINXEOF
# Sending "Connection: upgrade" on ordinary requests breaks keepalive, so switch
# on whether the client actually asked to upgrade.
map \$http_upgrade \$atlas_connection {
    default upgrade;
    ''      close;
}

server {
    # IPv4 only: a listen on [::] aborts nginx entirely on a host with IPv6
    # disabled, which several minimal images are.
    listen ${FRONT_PORT} ssl;
    server_name _;

    ssl_certificate     ${CERT_DIR}/server.crt;
    ssl_certificate_key ${CERT_DIR}/server.key;
    ssl_protocols TLSv1.2 TLSv1.3;

    # Shells are long-lived; do not cut them off mid-session.
    proxy_read_timeout 86400s;
    proxy_send_timeout 86400s;

    location / {
        auth_pam "Command Atlas - use your account on this host";
        auth_pam_service_name "command-atlas";

        # The app's own token stays server-side: nginx appends it after the
        # operator has authenticated, so it never appears in a published link.
        set \$atlas_args "token=${ATLAS_TOKEN}";
        if (\$args != "") {
            set \$atlas_args "\$args&token=${ATLAS_TOKEN}";
        }

        proxy_pass http://127.0.0.1:${APP_PORT}\$uri?\$atlas_args;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \$atlas_connection;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
    }
}
NGINXEOF
# The file embeds the app token, so keep it off a world-readable path. Only the
# nginx master reads the config, and it does so as root.
$SUDO chmod 600 "$SITE"
if [ "$FAMILY" = debian ]; then
	$SUDO ln -sf "$SITE" /etc/nginx/sites-enabled/command-atlas.conf
	$SUDO rm -f /etc/nginx/sites-enabled/default
fi
# Both distributions' packages drop their own load_module snippet into a
# directory nginx.conf already includes, so nothing is wired up by hand here. If
# the module is missing, the auth_pam directive makes `nginx -t` fail below —
# which is the outcome we want, rather than publishing an unauthenticated shell.

# SELinux: nginx may not connect to a local socket or read shadow by default.
if command -v setsebool >/dev/null 2>&1; then
	$SUDO setsebool -P httpd_can_network_connect 1 2>/dev/null || true
	$SUDO setsebool -P nis_enabled 1 2>/dev/null || true
fi

$SUDO nginx -t >/dev/null 2>&1 || {
	$SUDO nginx -t || true
	die "nginx rejected the generated configuration"
}
$SUDO systemctl enable nginx >/dev/null 2>&1 || true
$SUDO systemctl restart nginx
$SUDO systemctl is-active --quiet nginx || die "nginx failed to start"

# --- firewall -----------------------------------------------------------------
if command -v firewall-cmd >/dev/null 2>&1 && $SUDO firewall-cmd --state >/dev/null 2>&1; then
	$SUDO firewall-cmd --permanent --add-port="${FRONT_PORT}/tcp" >/dev/null 2>&1 || true
	$SUDO firewall-cmd --reload >/dev/null 2>&1 || true
elif command -v ufw >/dev/null 2>&1 && $SUDO ufw status 2>/dev/null | grep -q '^Status: active'; then
	$SUDO ufw allow "${FRONT_PORT}/tcp" >/dev/null 2>&1 || true
fi

# --- report -------------------------------------------------------------------
NODE_IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')"
[ -n "$NODE_IP" ] || NODE_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"

printf 'AIDT_SERVICE_INFO {"url":"https://%s:%s/","detail":"sign in with a local account on %s · shells run as %s"}\n' \
	"$NODE_IP" "$FRONT_PORT" "$(hostname)" "$ATLAS_USER"

log "=========================================="
log " Command Atlas ready on $(hostname)"
log " URL       : https://${NODE_IP}:${FRONT_PORT}/"
log " Sign in   : any local account on this host (PAM)"
log " Shells run: as ${ATLAS_USER}"
log " App       : 127.0.0.1:${APP_PORT} (loopback only)"
log "=========================================="
log "The certificate is self-signed, so the browser will warn once."
log "Anyone with an account on this host can open a shell here. Remove the"
log "deployment, or stop 'command-atlas' and 'nginx', when you are done."
