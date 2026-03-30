#!/system/bin/sh
# CLIProxyAPI Magisk Module - Service Script
# Magisk 在 late_start 服务阶段执行此脚本
# 不要在此文件中添加交互式命令

MODDIR="${0%/*}"

# 等待系统启动完成
while [ "$(getprop sys.boot_completed)" != "1" ]; do
    sleep 1
done

# 额外等待确保系统稳定
sleep 3

# 启动服务
if [ -f "$MODDIR/cli-proxy-api" ]; then
    chmod 755 "$MODDIR/cli-proxy-api"
    
    mkdir -p "$MODDIR/auths"
    mkdir -p "$MODDIR/logs"
    
    cd "$MODDIR"
    
    export HOME="$MODDIR"
    export CLI_PROXY_API_NO_BROWSER="true"
    
    "$MODDIR/cli-proxy-api" >> "$MODDIR/logs/service.log" 2>&1 &
    
    echo "[CLIProxyAPI] Service started"
fi
