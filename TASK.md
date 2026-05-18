# TASK: Headscale 组网 PoC 全量实现

## 【IMPLEMENT - DO NOT JUST PLAN. WRITE CODE NOW.】

---

## ⛔ CRITICAL CONSTRAINTS

- **服务端**: 单 Go 二进制，内嵌 Headscale Server + DERP Relay，部署在 43.165.166.56
- **客户端**: 单 Go 二进制 (hs-client)，Flutter UI 通过 Process.start 调用
- **Flutter**: 单代码库覆盖 Linux + Windows + Android
- **验证标准**: 两台设备都注册到 Headscale，互相 ping 通

---

## Phase 0: 环境准备 (先做)

### 1. 检查系统 Go 版本

```bash
go version
# 如果 < 1.23 → 下载 go1.25.5:
# curl -sL https://go.dev/dl/go1.25.5.linux-amd64.tar.gz | tar -C /usr/local -xzf -
```

### 2. 初始化 Go workspace

```bash
cd /opt/hermes-agent/hs-poc
# server 模块
mkdir -p server && cd server && go mod init hs-poc-server
# client 模块
mkdir -p client && cd client && go mod init hs-poc-client
```

### 3. 初始化 Flutter 项目

```bash
cd /opt/hermes-agent/hs-poc
flutter create --project-name hs_poc flutter
```

---

## Phase 1: 服务端 `hs-server` (Go)

### 1.1 NEW FILE: `server/main.go`

```go
// hs-server: Headscale Server + DERP Relay 一体化
// 
// 启动流程:
// 1. 初始化 Headscale（注册 HTTP API、配置数据库）
// 2. 启动 DERP Relay 服务器
// 3. 启动 Headscale HTTP API
// 4. 所有服务运行在单进程中

package main

func main() {
    // TODO: 实现
}
```

### 1.2 技术要点

Tailscale/Headscale 都是 Apache 2.0，可直接 import：

| 依赖 | Go import | 用途 |
|------|-----------|------|
| Headscale | `github.com/juanfont/headscale` | 控制面：用户/节点管理、ACL、API |
| DERP Server | `tailscale.com/derp/derphttp` | DERP 中继服务端 |
| STUN | `tailscale.com/net/stun` | NAT 类型检测 |
| WireGuard | `golang.zx2c4.com/wireguard` | 内核隧道 |

**关键 Headscale 初始化代码路径**:
- 配置: `headscale.Config` 结构体
- 数据库初始化: SQLite，路径 `./hs-data/headscale.db`
- HTTP Router: 注册到自定义端口 `:8080`
- API Key: 预生成一个 admin key 供客户端注册用

**DERP 服务端嵌入**:
- 启动端口 `:50443`（DERP over HTTPS）
- 启动端口 `:3478`（STUN）
- 可以嵌入为 goroutine，和 Headscale 同进程

### 1.3 NEW FILE: `server/headscale/config.go`

```go
package headscale

// 配置结构体，YAML 可序列化
type Config struct {
    ServerURL    string `yaml:"server_url"`    // https://43.165.166.56:8080
    ListenAddr   string `yaml:"listen_addr"`   // :8080
    DERPListen   string `yaml:"derp_listen"`   // :50443
    STUNListen   string `yaml:"stun_listen"`   // :3478
    DataDir      string `yaml:"data_dir"`      // ./hs-data
    AdminKey     string `yaml:"admin_key"`     // 预生成的 API key
}
```

### 1.4 DoD — 服务端

- [ ] `go build -o hs-server ./server/` 编译通过
- [ ] 启动后 `curl http://43.165.166.56:8080/health` 返回 200
- [ ] `curl http://43.165.166.56:8080/api/v1/node` 能列出节点（即使为空）
- [ ] DERP 端口 50443 可达

---

## Phase 2: 客户端二进制 `hs-client` (Go)

### 2.1 NEW FILE: `client/main.go`

```go
// hs-client: Headscale 客户端节点
//
// 命令行接口（供 Flutter 调用）:
//   hs-client register   --server https://43.165.166.56:8080 --key <admin-key>
//   hs-client up         -- 启动 WireGuard 隧道
//   hs-client down       -- 断开
//   hs-client status     -- 输出 JSON: {"connected":true,"ip":"10.0.0.2","peers":[...]}
//   hs-client ping <ip>  -- ping 对端节点

package main

func main() {
    // TODO: 实现 CLI
}
```

### 2.2 技术要点

使用 Tailscale 的 Go 客户端库（`tailscale.com/client/tailscale`）：

| 功能 | 实现方式 |
|------|---------|
| 注册节点 | HTTP POST `/api/v1/node/register?key=<preauth>` |
| 获取 WireGuard 配置 | Headscale API 返回 peer 的公钥 + 端点 |
| 创建 WG 接口 | `golang.zx2c4.com/wireguard/device` |
| 状态查询 | HTTP GET `/api/v1/node` → JSON |
| Ping | `os/exec` 调用系统 ping |

**客户端注册流程**:
```
1. POST /api/v1/node/register?user=default&key=<preauth>
2. 获取返回的 node_key + wireguard 配置
3. 启动 goroutine: 定期 POST /api/v1/node/{id}/keepalive
4. 用返回的 peer 列表配置 WireGuard
```

**WireGuard 配置生成** (客户端侧):
```go
cfg := wgtypes.Config{
    PrivateKey: &privateKey,
    ListenPort: &port,
    Peers: []wgtypes.PeerConfig{
        {
            PublicKey: peerKey,
            Endpoint:  peerEndpoint,
            AllowedIPs: []net.IPNet{
                {IP: net.ParseIP("10.0.0.0"), Mask: net.CIDRMask(24, 32)},
            },
        },
    },
}
```

### 2.3 DoD — 客户端

- [ ] `go build -o hs-client ./client/` 编译通过
- [ ] `hs-client register --server ... --key ...` 注册成功，返回节点 ID
- [ ] `hs-client up` 创建 WireGuard 接口
- [ ] `hs-client status` 输出有效 JSON
- [ ] 两个客户端互相 `hs-client ping 10.0.0.x` 成功

---

## Phase 3: Flutter 多端 UI

### 3.1 REWRITE: `flutter/lib/main.dart`

功能要求：
- **状态指示器**: 红色(断)/橙色(连接中)/绿色(已连接)
- **Connect/Disconnect 按钮**: 调用 hs-client up/down
- **节点列表**: 显示所有在线 Peer（IP + 主机名）
- **Ping 按钮**: Ping 选中的对端节点，显示延迟

### 3.2 NEW FILE: `flutter/lib/hs_service.dart`

```dart
/// Headscale 客户端服务
/// 通过 Process.start 调用 hs-client 二进制
class HsService {
  static const clientBin = 'hs-client';  // 或 'hs-client.exe' (Windows)

  Future<void> register(String serverUrl, String key) async { ... }
  Future<void> connect() async { ... }
  Future<void> disconnect() async { ... }
  Future<HsStatus> status() async { ... }
  Future<String> ping(String targetIP) async { ... }
}
```

### 3.3 平台适配

在 `hs_service.dart` 中根据平台选择二进制：
```dart
String get clientPath {
  if (Platform.isWindows) return 'assets/hs-client.exe';
  if (Platform.isLinux) return 'assets/hs-client-linux';
  if (Platform.isAndroid) return 'assets/hs-client-android';
  throw UnsupportedError('Unsupported platform');
}
```

### 3.4 DoD — Flutter

- [ ] `flutter analyze` 无错误
- [ ] `flutter build linux` 编译通过
- [ ] 界面显示连接状态 + 节点列表
- [ ] 点击 Ping 能显示结果

---

## Phase 4: 端到端验证

```bash
# 1. 部署服务端到腾讯云
scp server/hs-server ubuntu@43.165.166.56:~/
ssh ubuntu@43.165.166.56 './hs-server &'

# 2. 客户端 A 注册 & 连接 (本机 Linux)
cd client && go build -o hs-client .
./hs-client register --server https://43.165.166.56:8080 --key <admin-key>
./hs-client up

# 3. 客户端 B 注册 & 连接 (Windows / 另一台)
hs-client.exe register --server https://43.165.166.56:8080 --key <admin-key>
hs-client.exe up

# 4. 验证组网
./hs-client ping 10.0.0.3
# → Reply from 10.0.0.3: time=12ms  ✅
```

---

## 【VERIFICATION — YOU MUST RUN】

```bash
# 服务端编译
cd /opt/hermes-agent/hs-poc/server && go build -o hs-server . 2>&1

# 客户端编译
cd /opt/hermes-agent/hs-poc/client && go build -o hs-client . 2>&1

# Flutter 分析
cd /opt/hermes-agent/hs-poc/flutter && flutter analyze 2>&1
```