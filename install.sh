#!/usr/bin/env bash
#
# Karots POS — one-shot installer for a fresh Linux PC.
# Distro-agnostic: Debian/Ubuntu/Xubuntu/Mint (apt), Fedora/RHEL (dnf),
# Arch/CachyOS (pacman) and openSUSE (zypper). Just drop this script and the
# `karots-pos` binary NEXT TO EACH OTHER on the machine and run it:
#
#   sudo ./install.sh                      # binary auto-found beside this script
#   sudo ./install.sh /path/to/karots-pos  # or point it at the binary
#
# What it does (all idempotent — safe to re-run):
#   1. Installs PostgreSQL, CUPS (printing), Chromium and helpers via the
#      distro's own package manager, and initialises + starts PostgreSQL.
#   2. Creates the pos_db database + pos_user role (generates a strong password)
#      and makes sure password login over loopback is allowed on every distro.
#   3. Moves the binary to /opt/karots-pos, writes a production .env with a
#      generated JWT secret and a backups/ folder, runs the one-time -init.
#   4. Installs + starts a systemd service so the till runs on boot.
#   5. Sets up a Chromium --kiosk that opens the till full-screen at login.
#
# Nothing here needs Go, Node or a build toolchain — the binary is self-contained.
# Override any default by exporting it first, e.g.  POS_PORT=8080 sudo -E ./install.sh
set -euo pipefail

# ---------------------------------------------------------------------------
# Config (override via environment)
# ---------------------------------------------------------------------------
INSTALL_DIR="${INSTALL_DIR:-/opt/karots-pos}"
DB_NAME="${DB_NAME:-pos_db}"
DB_USER="${DB_USER:-pos_user}"
POS_PORT="${POS_PORT:-3000}"
BACKUP_DIR="${BACKUP_DIR:-$INSTALL_DIR/backups}"
KIOSK="${KIOSK:-yes}"                     # set KIOSK=no to skip the Chromium kiosk
BIN_NAME="karots-pos"
SERVICE="/etc/systemd/system/karots-pos.service"

say()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!  \033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "run with sudo:  sudo ./install.sh"
command -v systemctl >/dev/null || die "this installer needs systemd (systemctl not found)"

# Package manager → distro family.
if   command -v apt-get >/dev/null; then PM=apt
elif command -v dnf     >/dev/null; then PM=dnf
elif command -v pacman  >/dev/null; then PM=pacman
elif command -v zypper  >/dev/null; then PM=zypper
else die "unsupported distro: need one of apt / dnf / pacman / zypper"
fi
say "Detected package manager: $PM"

# The desktop/service user is the human who ran sudo — NOT root. The kiosk and
# the service run as this user so it can reach the display and own its files.
APP_USER="${APP_USER:-${SUDO_USER:-}}"
[ -n "$APP_USER" ] && [ "$APP_USER" != "root" ] || die "run via 'sudo' from your normal user (need a non-root login for the kiosk)"
APP_HOME="$(getent passwd "$APP_USER" | cut -d: -f6)"
[ -n "$APP_HOME" ] || die "cannot resolve home directory for user '$APP_USER'"

# Locate the binary: explicit arg → beside this script → ~/Downloads → home.
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
BINARY="${1:-}"
if [ -z "$BINARY" ]; then
  for cand in "$SCRIPT_DIR/$BIN_NAME" "$APP_HOME/Downloads/$BIN_NAME" "./$BIN_NAME" "$APP_HOME/$BIN_NAME"; do
    [ -f "$cand" ] && { BINARY="$cand"; break; }
  done
fi
[ -n "$BINARY" ] && [ -f "$BINARY" ] || die "binary not found. Put '$BIN_NAME' next to install.sh (or in ~/Downloads), or pass its path."
say "Using binary:   $BINARY"
say "Install dir:    $INSTALL_DIR   (service+kiosk user: $APP_USER)"

# Portability guard: a dynamically-linked binary dies on older machines with a
# glibc-version error. A correct build is CGO_ENABLED=0 → fully static.
if ldd "$BINARY" >/dev/null 2>&1; then
  warn "This binary is DYNAMICALLY linked — it may fail on older machines with a"
  warn "'GLIBC_… not found' error. Rebuild with 'make build' (CGO_ENABLED=0 GOAMD64=v1)."
  warn "Continuing in 5s (Ctrl-C to abort)…"; sleep 5
else
  say "Binary is statically linked (portable). Good."
fi

# ---------------------------------------------------------------------------
# Package-manager helpers
# ---------------------------------------------------------------------------
pm_install() {   # refresh + install, the safe way per distro
  case $PM in
    apt)    DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" >/dev/null ;;
    dnf)    dnf install -y -q "$@" >/dev/null ;;
    pacman) pacman -Syu --noconfirm --needed "$@" >/dev/null ;;
    zypper) zypper --non-interactive install -y "$@" >/dev/null ;;
  esac
}
pm_try() { pm_install "$@" >/dev/null 2>&1; }   # best-effort (won't abort the run)

# ---------------------------------------------------------------------------
# 1. Packages
# ---------------------------------------------------------------------------
say "Installing PostgreSQL, CUPS, curl, openssl…"
case $PM in
  apt)    pm_install postgresql cups curl openssl ca-certificates ;;
  dnf)    pm_install postgresql-server postgresql cups curl openssl ;;
  pacman) pm_install postgresql cups curl openssl ;;
  zypper) pm_install postgresql-server postgresql cups curl openssl ;;
esac

say "Installing Chromium…"
CHROMIUM_BIN="$(command -v chromium || command -v chromium-browser || true)"
if [ -z "$CHROMIUM_BIN" ]; then
  case $PM in
    apt)    pm_try chromium || pm_try chromium-browser ;;
    *)      pm_try chromium ;;
  esac
  CHROMIUM_BIN="$(command -v chromium || command -v chromium-browser || true)"
  if [ -z "$CHROMIUM_BIN" ] && command -v snap >/dev/null; then
    snap install chromium >/dev/null 2>&1 && CHROMIUM_BIN=/snap/bin/chromium
  fi
fi
[ -n "$CHROMIUM_BIN" ] || warn "Chromium not installed — the kiosk step will be skipped. Install it manually and re-run."

# ---------------------------------------------------------------------------
# 2. Initialise + start PostgreSQL (Fedora/Arch need an explicit initdb)
# ---------------------------------------------------------------------------
say "Initialising PostgreSQL data directory (if needed)…"
case $PM in
  dnf)
    [ -f /var/lib/pgsql/data/PG_VERSION ] || postgresql-setup --initdb >/dev/null 2>&1 || true ;;
  pacman)
    if [ ! -f /var/lib/postgres/data/PG_VERSION ]; then
      install -d -o postgres -g postgres /var/lib/postgres/data
      runuser -u postgres -- initdb --locale=C.UTF-8 -E UTF8 -D /var/lib/postgres/data >/dev/null 2>&1 || true
    fi ;;
  zypper|apt)
    : ;;  # Debian creates a cluster on install; openSUSE initialises on first start
esac
systemctl enable --now postgresql >/dev/null 2>&1 || die "PostgreSQL failed to start — check: systemctl status postgresql"
systemctl enable --now cups       >/dev/null 2>&1 || true

# Wait for the socket to accept connections before we touch it.
for _ in $(seq 1 30); do runuser -u postgres -- psql -tAc 'SELECT 1' >/dev/null 2>&1 && break; sleep 1; done

# ---------------------------------------------------------------------------
# 3. Database  (reuse existing creds on re-run so we never lock ourselves out)
# ---------------------------------------------------------------------------
psqlq() { runuser -u postgres -- psql -tAc "$1"; }

if [ -f "$INSTALL_DIR/.env" ]; then
  say "Existing .env found — reusing its database password and JWT secret."
  DB_PASS="$(sed -nE 's#^DATABASE_URL=.*://[^:]+:([^@]+)@.*#\1#p' "$INSTALL_DIR/.env" | head -n1)"
  JWT_SECRET="$(sed -nE 's#^JWT_SECRET=(.*)#\1#p' "$INSTALL_DIR/.env" | head -n1)"
fi
DB_PASS="${DB_PASS:-$(openssl rand -hex 16)}"
JWT_SECRET="${JWT_SECRET:-$(openssl rand -hex 24)}"   # 48 hex chars ≥ 32 required

say "Ensuring PostgreSQL role '$DB_USER' and database '$DB_NAME'…"
if [ "$(psqlq "SELECT 1 FROM pg_roles WHERE rolname='$DB_USER'")" = "1" ]; then
  runuser -u postgres -- psql -qc "ALTER ROLE $DB_USER WITH LOGIN PASSWORD '$DB_PASS'" >/dev/null
else
  runuser -u postgres -- psql -qc "CREATE ROLE $DB_USER WITH LOGIN PASSWORD '$DB_PASS'" >/dev/null
fi
[ "$(psqlq "SELECT 1 FROM pg_database WHERE datname='$DB_NAME'")" = "1" ] \
  || runuser -u postgres -- psql -qc "CREATE DATABASE $DB_NAME OWNER $DB_USER" >/dev/null
DATABASE_URL="postgres://$DB_USER:$DB_PASS@localhost:5432/$DB_NAME?sslmode=disable"

# Ensure password auth over loopback. Debian allows it by default; Fedora/Arch's
# default pg_hba.conf uses ident/trust for 127.0.0.1 and would reject the app's
# password login, so prepend an explicit rule (first match wins) and reload.
HBA="$(psqlq 'SHOW hba_file' | tr -d '[:space:]' || true)"
if [ -n "$HBA" ] && [ -f "$HBA" ] && ! grep -q 'karots-pos install' "$HBA"; then
  say "Allowing password login for '$DB_USER' over loopback in pg_hba.conf…"
  tmp="$(mktemp)"
  {
    echo "# karots-pos install: password auth for the till over loopback"
    echo "host $DB_NAME $DB_USER 127.0.0.1/32 scram-sha-256"
    echo "host $DB_NAME $DB_USER ::1/128      scram-sha-256"
    cat "$HBA"
  } > "$tmp"
  install -o postgres -g postgres -m 600 "$tmp" "$HBA"; rm -f "$tmp"
  systemctl reload postgresql >/dev/null 2>&1 || runuser -u postgres -- psql -qc 'SELECT pg_reload_conf()' >/dev/null 2>&1 || true
fi

# ---------------------------------------------------------------------------
# 4. Files: binary, .env, backup folder
# ---------------------------------------------------------------------------
say "Installing binary and settings into $INSTALL_DIR…"
install -d -m 0755 "$INSTALL_DIR"
install -m 0755 "$BINARY" "$INSTALL_DIR/$BIN_NAME"
install -d -m 0755 "$BACKUP_DIR"            # <-- the backup folder

# .env — systemd-EnvironmentFile safe: KEY=value, no spaces, no inline comments.
umask 077
cat > "$INSTALL_DIR/.env" <<ENV
APP_ENV=production
DATABASE_URL=$DATABASE_URL
SERVER_PORT=$POS_PORT
JWT_SECRET=$JWT_SECRET
JWT_EXPIRES_IN=12h
JWT_REFRESH_EXPIRES_IN=168h
CORS_ORIGINS=http://localhost:$POS_PORT
COOKIE_SECURE=auto
BACKUP_DIR=$BACKUP_DIR
BACKUP_INTERVAL=6h
BACKUP_KEEP=28
ENV
umask 022

chown -R "$APP_USER":"$APP_USER" "$INSTALL_DIR"
chmod 600 "$INSTALL_DIR/.env"

# One-time setup: create the schema + hidden admin. The binary auto-loads ./.env
# from beside itself, so no manual export is needed. Migrations run on every boot.
say "Running one-time initialisation (-init)…"
if ! runuser -u "$APP_USER" -- bash -c "cd '$INSTALL_DIR' && ./$BIN_NAME -init"; then
  warn "-init returned an error (often fine if already initialised). Continuing."
fi

# ---------------------------------------------------------------------------
# 5. systemd service
# ---------------------------------------------------------------------------
say "Installing systemd service (auto-start on boot)…"
cat > "$SERVICE" <<UNIT
[Unit]
Description=Karots POS
After=network-online.target postgresql.service
Wants=network-online.target postgresql.service

[Service]
Type=simple
User=$APP_USER
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=$INSTALL_DIR/.env
ExecStart=$INSTALL_DIR/$BIN_NAME
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now karots-pos >/dev/null
systemctl restart karots-pos      # pick up a changed binary/.env on a re-run

# Open the firewall for other tills on the LAN (ufw or firewalld, if active).
if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q 'Status: active'; then
  ufw allow "$POS_PORT/tcp" >/dev/null 2>&1 || true
elif command -v firewall-cmd >/dev/null && systemctl is-active --quiet firewalld; then
  firewall-cmd --add-port="$POS_PORT/tcp" --permanent >/dev/null 2>&1 && firewall-cmd --reload >/dev/null 2>&1 || true
fi

# Wait for the server to answer before we call it done.
say "Waiting for the till to come up on http://localhost:$POS_PORT …"
up=""
for _ in $(seq 1 30); do
  curl -fsS "http://localhost:$POS_PORT/health" >/dev/null 2>&1 && { up=1; break; }
  sleep 1
done
[ -n "$up" ] && say "Till is up." || warn "Till did not answer yet — check:  journalctl -u karots-pos -f"

# ---------------------------------------------------------------------------
# 6. Chromium kiosk (opens the till full-screen at desktop login)
# ---------------------------------------------------------------------------
if [ "$KIOSK" = "yes" ] && [ -n "$CHROMIUM_BIN" ]; then
  say "Setting up the Chromium kiosk for user '$APP_USER'…"
  cat > "$INSTALL_DIR/kiosk.sh" <<KIOSK_SH
#!/usr/bin/env bash
# Launches the till full-screen once the server is answering. Installed by install.sh.
URL="http://localhost:$POS_PORT"
for _ in \$(seq 1 60); do curl -fsS "\$URL/health" >/dev/null 2>&1 && break; sleep 2; done
# Keep the screen awake (no blanking / DPMS) — it's a till.
xset s off -dpms s noblank 2>/dev/null || true
exec "$CHROMIUM_BIN" --kiosk --app="\$URL" --incognito \\
  --noerrdialogs --disable-infobars --disable-session-crashed-bubble \\
  --disable-features=TranslateUI --check-for-update-interval=31536000 \\
  --overscroll-history-navigation=0 --password-store=basic
KIOSK_SH
  chmod 0755 "$INSTALL_DIR/kiosk.sh"

  AUTOSTART_DIR="$APP_HOME/.config/autostart"
  install -d -m 0755 -o "$APP_USER" -g "$APP_USER" "$AUTOSTART_DIR"
  cat > "$AUTOSTART_DIR/karots-kiosk.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Karots POS Kiosk
Comment=Open the till full-screen on login
Exec=$INSTALL_DIR/kiosk.sh
Terminal=false
X-GNOME-Autostart-enabled=true
DESKTOP
  chown "$APP_USER":"$APP_USER" "$AUTOSTART_DIR/karots-kiosk.desktop"
  say "Kiosk installed. It opens automatically the next time '$APP_USER' logs in."
  say "Start it now without logging out:  sudo -u $APP_USER DISPLAY=:0 $INSTALL_DIR/kiosk.sh &"
fi

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
cat <<DONE

$(printf '\033[1;32m✓ Karots POS is installed.\033[0m')

  Till URL        http://localhost:$POS_PORT   (and http://<this-pc-ip>:$POS_PORT on the LAN)
  Files           $INSTALL_DIR   (binary, .env, backups/)
  Service         systemctl status karots-pos   ·   journalctl -u karots-pos -f
  First login     hidden system admin — phone 0000000001 (see SETUP.md for the PIN),
                  then create real staff in Admin → Users.
  Printing        install a RAW CUPS queue, then pick it in Admin → Settings → Printers
                  (see PRINTING.md). CUPS is already installed.
  Kiosk           $([ "$KIOSK" = yes ] && [ -n "$CHROMIUM_BIN" ] && echo "auto-opens at login for '$APP_USER'" || echo "skipped")

  Keep $INSTALL_DIR/.env private — it holds the DB password and JWT secret.
DONE
