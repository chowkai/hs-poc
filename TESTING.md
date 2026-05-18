# hs-poc 真机测试指南

## Android APK 测试

### 1. 安装

```bash
# 方式 A：HTTP 下载（本机）
adb install http://192.192.26.51:8082/app-release.apk

# 方式 B：手动传文件
adb install app-release.apk
```

### 2. 启动前确认

- 腾讯云 Headscale 已运行：`curl http://43.165.166.56:8080/health` → `{"status":"ok"}`
- 手机有网络（WiFi 或 4G/5G）
- **无需 VPN 权限弹窗**（纯用户态 netstack）

### 3. 测试步骤

| 步骤 | 操作 | 预期结果 |
|------|------|----------|
| 1 | 打开 APP | 显示状态灯（红色=断开） |
| 2 | 检查 Server URL | 默认 `http://10.0.2.2:9090`，真机需改为 `http://43.165.166.56:8080` |
| 3 | 填入 Admin Key | 见下方密钥表 |
| 4 | 点 **Connect** | 状态灯变橙色→绿色，底部显示"Connected" |
| 5 | 点刷新查看 peers | 应能看到云服务器节点 |

### 4. 密钥

| 用途 | 密钥 |
|------|------|
| Admin Key | `hskey-api-F65eEHOQ-Rbz-XZZiYgZmXDgs4bPmR7QhL5zSWK3E7H1gqpC0SvLyo9gceSqRm25yowr-_lhpxPdY` |
| Preauth Key (注册节点) | `hskey-auth-zHyqYnt5tbDB-...`（Headscale 自动生成） |

### 5. 日志诊断

```bash
# 查看 APP 日志（需要 USB 连接）
adb logcat -s HsVpnService:D MainActivity:D flutter:D

# 只看关键事件
adb logcat | grep -E "HsVpn|MainActivity|hs-client|connect|register"
```

### 6. 已知限制

| 问题 | 原因 | 状态 |
|------|------|------|
| VPN 隧道走 GoBackend（TUN 模式） | 尚未切到 hs-client netstack | ⏳ 下一步优化 |
| 需要系统 VPN 权限弹窗 | VpnService 强制要求 | ⏳ 切 netstack 后消除 |
| 模拟器 GoBackend 报错 | x86_64 TUN 不兼容 | ✅ 真机应无此问题 |
| Server URL 默认 localhost | 真机需手动改 | ⚠️ 需改 UI 默认值 |

### 7. 测试后验证

连接成功后：
1. 查看 Headscale 节点列表：
   ```bash
   curl -s http://43.165.166.56:8080/api/v1/node \
     -H 'Authorization: Bearer hskey-api-F65eEHOQ-Rbz-XZZiYgZmXDgs4bPmR7QhL5zSWK3E7H1gqpC0SvLyo9gceSqRm25yowr-_lhpxPdY' \
     | python3 -m json.tool | grep -E 'name|ip_address|online'
   ```
2. 确认 Android 设备出现在列表中且 `online: true`
3. 尝试从云端 ping Android 设备 IP

---

## Windows 客户端测试

### 1. 编译

```bash
cd flutter
flutter build windows
```

### 2. 运行

```bash
cd build/windows/x64/runner/Debug
./hs_poc.exe
```

### 3. 测试步骤

同 Android，但 Server URL 默认为 `http://43.165.166.56:8080`（无需改）。

---

## 一键验证脚本（桌面端）

```bash
#!/bin/bash
# test_mesh.sh — 端到端组网验证
SERVER="http://43.165.166.56:8080"
KEY="hskey-api-F65eEHOQ-Rbz-XZZiYgZmXDgs4bPmR7QhL5zSWK3E7H1gqpC0SvLyo9gceSqRm25yowr-_lhpxPdY"

# 1. 检查服务端
echo "=== Check server ==="
curl -s $SERVER/health

# 2. 注册节点
echo "=== Register node ==="
NODE=$(./client/hs-client register --server $SERVER --key $KEY --name test-$(date +%s))
echo "Node: $NODE"

# 3. 查看所有节点
echo "=== All nodes ==="
curl -s $SERVER/api/v1/node -H "Authorization: Bearer $KEY" | python3 -m json.tool
```