# 轻量监控与告警（飞书）

## 文件
- `monitor.sh`：监控主脚本
- `monitor.conf.example`：配置模板
- `install.sh`：安装脚本

## 监控项
- API 存活与响应时间
- Docker 容器运行状态与重启次数
- CPU / 内存 / 磁盘
- 网络错误计数
- UFW 状态与 80/443 暴露检查
- fail2ban 与 sshd jail
- TLS 证书到期时间
- 源站直连检查

## 增强点
- 每次执行写心跳
- 每次执行输出摘要：`status=OK|WARN|FAIL`
- 失败恢复后发送飞书恢复通知
- cron 保留独立运行日志
- 自动安装 logrotate，避免日志无限增长
- 使用 `flock` 防止并发重入

## 安装
```bash
cd /opt/CLIProxyAPI/deploy/monitor
sudo bash install.sh
```

## 配置
```bash
sudo vi /opt/CLIProxyAPI/monitor/monitor.conf
```

至少填写：
- `FEISHU_WEBHOOK`
- `DOMAIN`
- `ORIGIN_IP`
- `API_KEY`（如接口需要鉴权）

可选配置：
- `RECOVERY_ALERTS=1`：异常恢复后发送恢复通知
- `ALERT_COOLDOWN=1800`：同类告警冷却时间

## 手动测试
```bash
/opt/CLIProxyAPI/monitor/monitor.sh
tail -n 50 /var/log/cliproxyapi-monitor.log
tail -n 50 /var/log/cliproxyapi-monitor-run.log
```

## 默认阈值
- CPU `>= 90%`
- 内存 `>= 90%`
- 磁盘 `>= 85%`
- API 连续失败 `>= 3` 次
- API 响应时间 `>= 2000ms`
- 证书剩余天数 `<= 15`

## 日志
- 告警与摘要日志：`/var/log/cliproxyapi-monitor.log`
- cron 运行日志：`/var/log/cliproxyapi-monitor-run.log`

## 日志轮转
安装脚本会写入：

`/etc/logrotate.d/cliproxyapi-monitor`

策略：
- 每日轮转
- 保留 14 份
- 压缩旧日志
- `copytruncate` 避免中断写入

## 定时任务
`/etc/cron.d/cliproxyapi-monitor`
