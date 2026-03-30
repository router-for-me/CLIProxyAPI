#!/system/bin/sh
# CLIProxyAPI Magisk Module - Post FS Data Script
# This script runs after the file system is mounted

MODDIR="${0%/*}"

mkdir -p "$MODDIR/auths"
mkdir -p "$MODDIR/logs"
mkdir -p "$MODDIR/config_backup"

chmod 755 "$MODDIR/auths"
chmod 755 "$MODDIR/logs"
chmod 755 "$MODDIR/config_backup"

if [ -f "$MODDIR/cli-proxy-api" ]; then
    chmod 755 "$MODDIR/cli-proxy-api"
fi

if [ -f "$MODDIR/config.yaml" ]; then
    chmod 644 "$MODDIR/config.yaml"
fi

CONFIG_SRC="$MODDIR/config.yaml"
CONFIG_BAK="$MODDIR/config_backup/config.yaml.bak"

if [ -f "$CONFIG_BAK" ]; then
    cp "$CONFIG_BAK" "$CONFIG_SRC"
else
    if [ -f "$CONFIG_SRC" ]; then
        cp "$CONFIG_SRC" "$CONFIG_BAK"
    fi
fi

echo "[CLIProxyAPI] Module initialized"
