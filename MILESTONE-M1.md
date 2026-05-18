# hs-poc 里程碑 M1：跨机器 WireGuard 数据面验证通过

> **日期**：2026-05-18  
> **版本**：hs-client v0.3（自研 Noise + wireguard-go netstack）  
> **目标**：验证自研 Go 客户端通过 noise 协议对接 Headscale，实现跨公网 WireGuard 数据面通信

---

## 1. 自研组件清单

### 1.1 Go 客户端 (`client/`) — **100% 自研**

| 文件 | 行数 | 职责 |
|------|------|------|
| `main.go` | 680 | CLI 入口、register/up/down/status/ping、netstack 集成、WG config 构建 |
| `noise/handshake.go` | — | Noise_IK_25519_ChaChaPoly_BLAKE2s 握手实现 |
| `noise/conn.go` | — | Noise 加密连接抽象 |
| `noise/dialer.go` | — | Noise over TCP 拨号 |
| `noise/h2frame.go` | — | HTTP/2 帧编解码 over Noise（自研，非标准库） |
| `go.mod` / `go.sum` | — | 依赖：wireguard-go、golang.org/x/net |

**总计**：9 个 Go 源文件，~2252 行（含 noise 协议栈 + WireGuard 集成）

### 1.2 Flutter Android 客户端 (`flutter/`) — **100% 自研**

| 文件 | 职责 |
|------|------|
| `lib/main.dart` | Flutter UI 入口 |
| `lib/hs_service.dart` | 后台服务管理 |
| `android/.../HsVpnService.kt` | Android VpnService，调用内嵌 hs-client |
| `android/.../MainActivity.kt` | VPN 授权流程 |
| `assets/hs-client-android` | 预编译 Android arm64 二进制 |

### 1.3 未使用第三方 WireGuard 客户端

- ❌ 未使用标准 `wg-quick` / `wireguard-tools`
- ❌ 未使用 Tailscale 官方客户端
- ❌ 未使用 wireguard-android 库
- ✅ **全程使用自研 hs-client + wireguard-go netstack**

---

## 2. 验证架构

```
┌─ 本机 (192.192.26.51) ─────────┐    ┌─ 腾讯云 (43.165.166.56) ──────┐
│                                 │    │                               │
│  hs-client (node-local)         │    │  Headscale (:8080)            │
│  IP: 100.64.0.27                │    │  hs-client (node-cloud)       │
│  WG Port: 51821                 │    │  IP: 100.64.0.26              │
│  Mode: netstack                 │    │  WG Port: 51832 (UFW ALLOW)   │
│                                 │    │  Mode: tmux 持久化             │
│         │                       │    │       │                       │
│         │  noise (TCP 8080)      │    │  noise (localhost:8080)       │
│         ├───────────────────────┼────┤       │                       │
│         │                       │    │       ▼                       │
│         │                       │    │  ┌─────────────┐              │
│         │                       │    │  │ Headscale    │              │
│         │                       │    │  │ /machine/map │              │
│         │                       │    │  └─────────────┘              │
│         │                       │    │                               │
│  ═══ WG 数据面 ═══              │    │  ═══ WG 数据面 ═══            │
│         │                       │    │       │                       │
│         │  Initiation ──────────┼────┼──────►│ 51832                 │
│         │  ◄────── Response ────┼────┼───────│                       │
│         │  Ping ────────────────┼────┼──────►│                       │
│         │  ◄────── Reply 13B ───┼────┼───────│                       │
│         │  Keepalive            │    │       │                       │
└─────────────────────────────────┘    └───────────────────────────────┘
```

---

## 3. 验证结果

### 3.1 Noise 控制面 ✅

```
hs-client register --server http://43.165.166.56:8080 --key hskey-auth-...
  → fetch server noise key
  → Noise_IK handshake
  → POST /machine/register → 200 MachineAuthorized=true
  → POST /machine/map → 200 (peers + WG config)
```

### 3.2 WG 数据面 ✅

```
本机 (100.64.0.27) → 腾讯云 (100.64.0.26):

08:07:41 peer(Z25+…uw2c) - Sending handshake initiation
08:07:41 peer(Z25+…uw2c) - Received handshake response
CROSS-PING 100.64.0.26: ✅ reply 13 bytes
08:07:51 peer(Z25+…uw2c) - Sending keepalive packet
```

### 3.3 关键配置参数

| 参数 | 值 |
|------|-----|
| Headscale URL | `http://43.165.166.56:8080` |
| Preauth Key | `hskey-auth-zHyqYnt5tbDB-...` (hs-poc user) |
| Cloud WG Port | 51832 (UFW: ALLOW) |
| Local WG Port | 51821 |
| WG IP Range | `100.64.0.0/24` |
| WG Mode | **netstack**（用户态，无内核 TUN 依赖） |
| Noise 协议 | IK_25519_ChaChaPoly_BLAKE2s |
| HTTP/2 帧 | 自研 h2frame.go |

### 3.4 踩坑记录

| 问题 | 根因 | 解决 |
|------|------|------|
| 握手超时 | 端口 51820 被 wg-poc 占用 | 换 51832（UFW 已放行） |
| 握手超时 | 端口 51830 不在 UFW 白名单 | 换 51832 |
| SSH 后台进程被杀 | `terminal(background=true)` + SSH 断连 → 远端进程被 SIGHUP | 改用 `tmux` 持久化云端进程 |
| Headscale endpoint=127.0.0.1 | 云端连自身 public IP 时，源 IP 被路由为 loopback | 加 `--peer-endpoint=IP=公网:端口` 覆盖 |
| peer 列表含 20+ 僵尸节点 | 未清理旧注册 | `headscale-bin nodes delete` 清理 |

---

## 4. 部署状态

| 组件 | 位置 | 状态 |
|------|------|------|
| Headscale | 腾讯云 `tmux` / `systemd` | 🟢 运行中 |
| hs-client (cloud) | 腾讯云 `tmux session: hs-cloud` | 🟢 运行中 (51832) |
| hs-client (local) | 本机，按需启动 | 🟡 前台验证通过 |
| hs-client 二进制 | `/opt/hermes-agent/hs-poc/client/hs-client` | ✅ 14.5MB ELF x86-64 |
| Flutter APK | `/opt/hermes-agent/hs-poc/flutter/` | ⏳ 下一步测试 |

### 云端持久化命令

```bash
# 腾讯云上 tmux 运行 hs-client
tmux new-session -d -s hs-cloud \
  "/home/ubuntu/hs-client up --server http://43.165.166.56:8080 --port 51832 2>&1 | tee /tmp/hs-cloud.log"
```

### 本机测试命令

```bash
# 注册
hs-client register --server http://43.165.166.56:8080 \
  --key "hskey-auth-..." --name local

# 启动（带 peer endpoint 覆盖）
hs-client up --server http://43.165.166.56:8080 \
  --port 51821 \
  --peer-endpoint=100.64.0.26=43.165.166.56:51832
```

---

## 5. Git 状态

```
7a02ee1 fix: CCD 修复 API key 格式 + noise h2frame 协议增强
86ce8f7 fix(client): 补 HTTP/2 连接前言，noise 注册成功
f0c18ad feat(client): 实现自研 Noise_IK_25519_ChaChaPoly_BLAKE2s 协议对接 Headscale
d7475ee feat: replace self-built WG with wireguard-android library
9f4d4ad feat: WireGuard crypto + Android VPN + Windows hs-client
```

**未提交文件**：`main.go`（含 `--peer-endpoint` 功能）

---

## 6. 下一步：Android 客户端测试

- [ ] 编译 Flutter APK（含 arm64 hs-client 二进制）
- [ ] 安装到 Android 真机/模拟器
- [ ] 注册节点到 Headscale
- [ ] 启动 VPN，验证跨机器 ping 通