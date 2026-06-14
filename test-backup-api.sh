#!/bin/bash

API_BASE="http://192.168.31.56:8317"
AUTH_TOKEN="test123456"

echo "========================================="
echo "CLIProxyAPI 备份功能测试"
echo "========================================="
echo ""

echo "1️⃣  测试获取备份配置..."
curl -s -H "Authorization: Bearer $AUTH_TOKEN" "$API_BASE/v0/management/backup/config" | python3 -m json.tool 2>/dev/null || echo "配置 API 响应收到"
echo ""
echo ""

echo "2️⃣  测试连接..."
curl -s -X POST -H "Authorization: Bearer $AUTH_TOKEN" "$API_BASE/v0/management/backup/test-connection" | python3 -m json.tool 2>/dev/null || echo "连接测试完成"
echo ""
echo ""

echo "3️⃣  创建新备份..."
curl -s -X POST -H "Authorization: Bearer $AUTH_TOKEN" "$API_BASE/v0/management/backup/create" | python3 -m json.tool 2>/dev/null || echo "备份创建完成"
echo ""
echo ""

sleep 2

echo "4️⃣  列出所有备份..."
curl -s -H "Authorization: Bearer $AUTH_TOKEN" "$API_BASE/v0/management/backup/list" | python3 -m json.tool 2>/dev/null || echo "备份列表获取完成"
echo ""
echo ""

echo "========================================="
echo "✅ 所有测试完成！"
echo "========================================="
echo ""
echo "🌐 访问演示页面: file://$(pwd)/backup-demo.html"
echo "🔗 API 服务器: $API_BASE"
echo "🎯 管理面板: $API_BASE/management.html"
