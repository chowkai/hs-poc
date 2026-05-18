# hs-poc — Headscale 组网 PoC

单二进制服务端（Headscale Server + DERP Relay）+ 单代码库 Flutter 客户端，实现真正的 WireGuard Mesh 组网。

## 架构

```
┌─────────────────────────────────────────────────┐
│  腾讯云 43.165.166.56                           │
│                                                 │
│  hs-server (单 Go 二进制)                        │
│  ├─ Headscale HTTP API (:8080)                  │
│  ├─ DERP Relay (:50443 TCP)                     │
│  └─ STUN (:3478 UDP)                            │
│                                                 │
│  Admin Key: 8f132c90060f736bf191eb6146714a53    │
└──────────┬──────────────────────────────────────┘
           │
    ┌──────┴──────┐
    │             │
┌───▼───┐    ┌───▼───┐
│Client A│   │Client B│    Flutter UI (同一代码库)
│Linux   │   │Windows │    Process.start → hs-client
│10.0.0.2│   │10.0.0.3│
└───┬────┘   └───┬────┘
    │   ping ✅   │
    └────────────┘
```

## 端口规划

| 端口 | 协议 | 服务 | 方向 |
|------|------|------|------|
| 8080 | TCP | Headscale HTTP API | 客户端 → 服务器 |
| 50443 | TCP | DERP Relay | 客户端 ↔ 服务器 |
| 3478 | UDP | STUN (NAT 检测) | 客户端 ↔ 服务器 |

> 已在腾讯云安全组和 UFW 中开放。

## 项目结构

```
hs-poc/
├── README.md
├── DEPLOY.md              # 部署与测试指南
├── TASK.md                # CCD 任务规格
├── server/                # Go 服务端
│   ├── main.go            # HTTP API + STUN + DERP
│   ├── headscale/         # 配置 + SQLite
│   └── hs-server          # 已编译二进制 (12MB)
├── client/                # Go 客户端
│   ├── main.go            # CLI: register/up/down/status/ping
│   └── hs-client          # 已编译二进制 (9.4MB)
└── flutter/               # Flutter 多端 UI
    ├── lib/main.dart       # 状态灯 + 连接 + 节点 + Ping
    ├── lib/hs_service.dart # Process.start 管理 hs-client
    └── assets/             # 预编译客户端二进制
```

## 快速验证

```bash
# 1. 服务端已运行在腾讯云
curl http://43.165.166.56:8080/health
# → {"status":"ok"}

# 2. 注册节点
./client/hs-client register \
  --server http://43.165.166.56:8080 \
  --key 8f132c90060f736bf191eb6146714a53 \
  --name my-device

# 3. 查看所有节点
curl -s http://43.165.166.56:8080/api/v1/node \
  -H 'Authorization: Bearer 8f132c90060f736bf191eb6146714a53'

# 4. Flutter 启动
cd flutter && flutter run -d linux
```

## 详细部署

见 [DEPLOY.md](./DEPLOY.md)

## 仓库

```bash
git clone http://10.0.0.86:3000/admin/hs-poc.git
```

## 构建

### 服务端

```bash
cd server && go build -o hs-server .
```

### 客户端（多平台）

```bash
cd client
GOOS=linux go build -o ../flutter/assets/hs-client-linux .
GOOS=windows go build -o ../flutter/assets/hs-client.exe .
```

### Flutter

```bash
cd flutter
flutter build linux    # Linux
flutter build windows  # Windows
flutter build apk      # Android
```