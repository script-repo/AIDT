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

# Privilege helpers rather than a "$SUDO" string. AIDT runs custom installers as
# root, which made an empty $SUDO turn "$SUDO -E bash" into "-E bash" and fail
# with exit 127; a function cannot lose its flags that way.
if [ "$(id -u)" -eq 0 ]; then
	RUN_AS="${SUDO_USER:-root}"
	run_root() { "$@"; }
	run_root_env() { "$@"; } # already root: the environment is ours
	run_as_user() {
		_u="$1"
		shift
		if [ "$_u" = "root" ]; then
			"$@"
		elif command -v runuser >/dev/null 2>&1; then
			runuser -u "$_u" -- "$@"
		elif command -v sudo >/dev/null 2>&1; then
			sudo -n -u "$_u" -- "$@"
		else
			die "cannot drop privileges to $_u (need runuser or sudo)"
		fi
	}
elif command -v sudo >/dev/null 2>&1; then
	sudo -n true 2>/dev/null || die "passwordless sudo is required"
	RUN_AS="$(id -un)"
	run_root() { sudo -n "$@"; }
	run_root_env() { sudo -n -E "$@"; }
	run_as_user() {
		_u="$1"
		shift
		if [ "$_u" = "$(id -un)" ]; then "$@"; else sudo -n -u "$_u" -- "$@"; fi
	}
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
	run_root apt-get update -y >/dev/null
	run_root env DEBIAN_FRONTEND=noninteractive apt-get install -y \
		git curl ca-certificates build-essential python3 openssl \
		nginx libnginx-mod-http-auth-pam libpam-modules >/dev/null
	NGINX_USER=www-data
	SHADOW_GROUP=shadow
else
	run_root dnf install -y epel-release >/dev/null 2>&1 || true
	run_root dnf install -y \
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
		curl -fsSL https://deb.nodesource.com/setup_22.x | run_root_env bash - >/dev/null
		run_root env DEBIAN_FRONTEND=noninteractive apt-get install -y nodejs >/dev/null
	else
		curl -fsSL https://rpm.nodesource.com/setup_22.x | run_root_env bash - >/dev/null
		run_root dnf install -y nodejs >/dev/null
	fi
fi
command -v node >/dev/null 2>&1 || die "node is missing after installation"
log "node $(node --version)"

# --- application --------------------------------------------------------------
log "fetching Command Atlas from script-repo/showcase (${ATLAS_REF})…"
if [ -d "$INSTALL_DIR/.git" ]; then
	run_root git -C "$INSTALL_DIR" fetch --depth 1 origin "$ATLAS_REF" >/dev/null
	run_root git -C "$INSTALL_DIR" reset --hard "origin/$ATLAS_REF" >/dev/null
else
	run_root rm -rf "$INSTALL_DIR"
	run_root git clone --depth 1 --branch "$ATLAS_REF" "$REPO_URL" "$INSTALL_DIR" >/dev/null
fi
[ -f "$APP_DIR/server.js" ] || die "$APP_DIR/server.js not found in the repository"
run_root chown -R "$ATLAS_USER" "$INSTALL_DIR"

# The repository commits node_modules, and node-pty's prebuilds there cover only
# darwin-arm64/win32. With that tree in place npm considers the install complete
# and never compiles the addon for this platform, so the app starts and
# immediately exits with "Missing dependencies". Remove it and install clean.
log "installing node dependencies (node-pty compiles a native addon)…"
ATLAS_HOME="$(getent passwd "$ATLAS_USER" | cut -d: -f6)"
run_root rm -rf "$APP_DIR/node_modules"
run_as_user "$ATLAS_USER" env HOME="$ATLAS_HOME" \
	npm --prefix "$APP_DIR" install --no-audit --no-fund >/dev/null ||
	die "npm install failed (build tools or network?)"

# A successful npm exit is not proof the native addon loads: verify before
# handing the service to systemd, so a build problem is reported here instead of
# as a crash loop the operator has to read the journal to understand.
# Absolute paths on purpose: `node -e` resolves bare module names from the
# current directory, which is not the app directory when this runs.
run_as_user "$ATLAS_USER" env HOME="$ATLAS_HOME" \
	node -e "require('$APP_DIR/node_modules/node-pty'); require('$APP_DIR/node_modules/ws');" >/dev/null 2>&1 ||
	die "node-pty did not load after installation — check the build toolchain (gcc/make/python3)"
log "node-pty built for $(uname -m) and loads correctly"

# --- token --------------------------------------------------------------------
# Pinned so the published URL keeps working across restarts. nginx injects it,
# so it never appears in a link, a log line, or AIDT's saved settings.
run_root mkdir -p "$(dirname "$ENV_FILE")"
if run_root test -s "$ENV_FILE"; then
	log "reusing the existing token"
else
	log "generating an access token…"
	printf 'ATLAS_TOKEN=%s\nPORT=%s\n' "$(openssl rand -hex 24)" "$APP_PORT" |
		run_root tee "$ENV_FILE" >/dev/null
fi
run_root chmod 600 "$ENV_FILE"
run_root chown "$ATLAS_USER" "$ENV_FILE"
ATLAS_TOKEN="$(run_root sed -n 's/^ATLAS_TOKEN=//p' "$ENV_FILE" | head -1)"
[ -n "$ATLAS_TOKEN" ] || die "could not read the access token"

# --- service ------------------------------------------------------------------
log "installing the command-atlas service…"
run_root tee /etc/systemd/system/command-atlas.service >/dev/null <<UNIT
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
run_root systemctl daemon-reload
run_root systemctl enable --now command-atlas >/dev/null
sleep 2
if ! run_root systemctl is-active --quiet command-atlas; then
	# Surface the reason here rather than making the operator go and find it.
	echo "[atlas] --- last journal entries ---" >&2
	run_root journalctl -u command-atlas --no-pager -n 15 2>&1 | sed 's/^/[atlas]   /' >&2 || true
	die "command-atlas failed to start"
fi

# --- PAM ----------------------------------------------------------------------
# nginx must be able to verify local passwords, which means reading the shadow
# database through PAM's helper.
log "configuring PAM authentication against local accounts…"
run_root tee /etc/pam.d/command-atlas >/dev/null <<'PAMEOF'
auth    required pam_unix.so
account required pam_unix.so
PAMEOF
run_root usermod -a -G "$SHADOW_GROUP" "$NGINX_USER" 2>/dev/null ||
	log "NOTE: could not add $NGINX_USER to $SHADOW_GROUP; PAM auth may fail"

# --- TLS ----------------------------------------------------------------------
# Self-signed, but the point is encryption: basic auth sends a real system
# password on every request, and plain HTTP would put it on the wire in base64.
CERT_DIR="/etc/command-atlas/tls"
run_root mkdir -p "$CERT_DIR"
if ! run_root test -s "$CERT_DIR/server.crt"; then
	log "generating a self-signed certificate…"
	NODE_IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')"
	[ -n "$NODE_IP" ] || NODE_IP="127.0.0.1"
	run_root openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
		-subj "/CN=$(hostname)" \
		-addext "subjectAltName=DNS:$(hostname),IP:$NODE_IP,IP:127.0.0.1" \
		-keyout "$CERT_DIR/server.key" -out "$CERT_DIR/server.crt" >/dev/null 2>&1 ||
		die "certificate generation failed"
fi
run_root chmod 600 "$CERT_DIR/server.key"
run_root chmod 644 "$CERT_DIR/server.crt"

# --- reverse proxy ------------------------------------------------------------
log "configuring nginx on :${FRONT_PORT} (TLS + PAM basic auth)…"
if [ "$FAMILY" = debian ]; then
	SITE=/etc/nginx/sites-available/command-atlas.conf
	run_root mkdir -p /etc/nginx/sites-enabled
else
	SITE=/etc/nginx/conf.d/command-atlas.conf
fi

run_root tee "$SITE" >/dev/null <<NGINXEOF
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
run_root chmod 600 "$SITE"
if [ "$FAMILY" = debian ]; then
	run_root ln -sf "$SITE" /etc/nginx/sites-enabled/command-atlas.conf
	run_root rm -f /etc/nginx/sites-enabled/default
fi
# Both distributions' packages drop their own load_module snippet into a
# directory nginx.conf already includes, so nothing is wired up by hand here. If
# the module is missing, the auth_pam directive makes `nginx -t` fail below —
# which is the outcome we want, rather than publishing an unauthenticated shell.

# SELinux: nginx may not connect to a local socket or read shadow by default.
if command -v setsebool >/dev/null 2>&1; then
	run_root setsebool -P httpd_can_network_connect 1 2>/dev/null || true
	run_root setsebool -P nis_enabled 1 2>/dev/null || true
fi

run_root nginx -t >/dev/null 2>&1 || {
	run_root nginx -t || true
	die "nginx rejected the generated configuration"
}
run_root systemctl enable nginx >/dev/null 2>&1 || true
run_root systemctl restart nginx
run_root systemctl is-active --quiet nginx || die "nginx failed to start"

# --- firewall -----------------------------------------------------------------
if command -v firewall-cmd >/dev/null 2>&1 && run_root firewall-cmd --state >/dev/null 2>&1; then
	run_root firewall-cmd --permanent --add-port="${FRONT_PORT}/tcp" >/dev/null 2>&1 || true
	run_root firewall-cmd --reload >/dev/null 2>&1 || true
elif command -v ufw >/dev/null 2>&1 && run_root ufw status 2>/dev/null | grep -q '^Status: active'; then
	run_root ufw allow "${FRONT_PORT}/tcp" >/dev/null 2>&1 || true
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
