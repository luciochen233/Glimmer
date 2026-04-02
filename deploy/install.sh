#!/bin/sh
# install.sh — Install urlshort as a system service on Ubuntu/Debian
# Works with systemd (Ubuntu 15.04+) and SysV init (older systems)
# Run as root: sudo ./install.sh

set -e

BINARY_SRC="./urlshort"
INSTALL_DIR="/opt/urlshort"
CONFIG_DIR="/etc/urlshort"
DATA_DIR="/var/lib/urlshort"
SERVICE_USER="urlshort"
SERVICE_NAME="urlshort"

# ── Helpers ───────────────────────────────────────────────────────────────────

die() { echo "ERROR: $1" >&2; exit 1; }
info() { echo "  --> $1"; }

require_root() {
    [ "$(id -u)" -eq 0 ] || die "This script must be run as root (use sudo)."
}

has_systemd() {
    command -v systemctl >/dev/null 2>&1 && systemctl --version >/dev/null 2>&1
}

# ── Preflight ─────────────────────────────────────────────────────────────────

require_root

[ -f "$BINARY_SRC" ] || die "Binary '$BINARY_SRC' not found. Build first: GOOS=linux GOARCH=arm go build -o urlshort ."
[ -f "./deploy/urlshort.service" ] || die "deploy/urlshort.service not found."
[ -f "./deploy/urlshort.init" ]    || die "deploy/urlshort.init not found."

echo "Installing urlshort..."

# ── Create service user ───────────────────────────────────────────────────────

if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    info "Creating system user '$SERVICE_USER'"
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
else
    info "User '$SERVICE_USER' already exists"
fi

# ── Create directories ────────────────────────────────────────────────────────

info "Creating directories"
mkdir -p "$INSTALL_DIR" "$CONFIG_DIR" "$DATA_DIR"
chown "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR"
chmod 750 "$DATA_DIR"

# ── Install binary ────────────────────────────────────────────────────────────

info "Installing binary to $INSTALL_DIR/urlshort"
cp "$BINARY_SRC" "$INSTALL_DIR/urlshort"
chown root:root "$INSTALL_DIR/urlshort"
chmod 755 "$INSTALL_DIR/urlshort"

# ── Install config (only if not already present) ──────────────────────────────

if [ ! -f "$CONFIG_DIR/config.toml" ]; then
    info "Installing default config to $CONFIG_DIR/config.toml"
    cp ./config.toml "$CONFIG_DIR/config.toml"
    # Point the database path at the data directory
    sed -i 's|path = .*|path = "/var/lib/urlshort/urls.db"|' "$CONFIG_DIR/config.toml"
    chown root:"$SERVICE_USER" "$CONFIG_DIR/config.toml"
    chmod 640 "$CONFIG_DIR/config.toml"
    echo ""
    echo "  ** IMPORTANT: Edit $CONFIG_DIR/config.toml and set admin.password_hash."
    echo "     Generate a hash with: $INSTALL_DIR/urlshort -hash-password 'yourpassword'"
    echo ""
else
    info "Config already exists at $CONFIG_DIR/config.toml — skipping"
fi

# ── Install and enable service ────────────────────────────────────────────────

if has_systemd; then
    info "Installing systemd service"
    cp ./deploy/urlshort.service /etc/systemd/system/urlshort.service
    chmod 644 /etc/systemd/system/urlshort.service
    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME"
    info "Service enabled. Start with: systemctl start $SERVICE_NAME"
    info "View logs with:              journalctl -u $SERVICE_NAME -f"
else
    info "systemd not found — installing SysV init script"
    cp ./deploy/urlshort.init /etc/init.d/urlshort
    chmod 755 /etc/init.d/urlshort
    update-rc.d urlshort defaults
    info "Service enabled. Start with: service $SERVICE_NAME start"
    info "View logs with:              tail -f /var/log/$SERVICE_NAME.log"
fi

echo ""
echo "Installation complete."
