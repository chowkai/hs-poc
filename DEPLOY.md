# hs-poc 部署与测试指南

## 1. 服务端部署（腾讯云 43.165.166.56）

### 1.1 端口

| 端口 | 协议 | 服务 | 云安全组 | UFW |
|------|------|------|---------|-----|
| 8080 | TCP | Headscale HTTP API | ✅ 已开 | ✅ 已开 |
| 50443 | TCP | DERP Relay | ⚠️ 待确认 | ✅ 已开 |
| 3478 | UDP | STUN | ⚠️ 待确认 | ✅ 已开 |

### 1.2 部署步骤

```bash
# 1. 编译（本机）
cd /opt/hermes-agent/hs-poc/server
CGO_ENABLED=0 go build -o hs-server .

# 2. 上传到腾讯云
scp -i ~/.ssh/tencent-tokyo-wg hs-server ubuntu@43.165.166.56:~/

# 3. SSH 登入腾讯云
ssh -i ~/.ssh/tencent-tokyo-wg ubuntu@43.165.166.56

# 4. 创建数据目录 + 生成 Admin Key
mkdir -p ~/hs-data
ADMIN_KEY=$(openssl rand -hex 16)
echo "ADMIN_KEY=$ADMIN_KEY" > ~/hs-data/admin-key.txt
chmod 600 ~/hs-data/admin-key.txt

# 5. 创建 systemd 服务
sudo tee /etc/systemd/system/hs-server.service << 'SVC'
[Unit]
Description=HS-PoC Headscale Server
After=network.target

[Service]
Type=simple
User=ubuntu
WorkingDirectory=/home/ubuntu
Environment="LISTEN_ADDR=:8080"
Environment="SERVER_URL=http://43.165.166.56:8080"
Environment="ADMIN_KEY=hskey-api-F65eEHOQ-Rbz-XZZiYgZmXDgs4bPmR7QhL5zSWK3E7H1gqpC0SvLyo9gceSqRm25yowr-_lhpxPdY"
Environment="DATA_DIR=/home/ubuntu/hs-data"
ExecStart=/home/ubuntu/hs-server
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
SVC

# 6. 启动服务
sudo systemctl daemon-reload
sudo systemctl enable hs-server
sudo systemctl start hs-server

# 7. 开放防火墙
sudo ufw allow 8080/tcp comment "hs-poc Headscale API"
sudo ufw allow 50443/tcp comment "hs-poc DERP Relay"
sudo ufw allow 3478/udp comment "hs-poc STUN"
```

### 1.3 运维命令

```bash
# 查看状态
sudo systemctl status hs-server

# 查看日志
sudo journalctl -u hs-server -f

# 重启
sudo systemctl restart hs-server

# 重置数据库（清空所有节点）
sudo systemctl stop hs-server
rm ~/hs-data/headscale.db
sudo systemctl start hs-server
```

### 1.4 验证 API

```bash
# 健康检查
curl http://43.165.166.56:8080/health
# → {"status":"ok"}

# 注册测试节点
curl -s -X POST 'http://43.165.166.56:8080/api/v1/node/register?name=test-device' \
  -H 'Authorization: Bearer hskey-api-F65eEHOQ-Rbz-XZZiYgZmXDgs4bPmR7QhL5zSWK3E7H1gqpC0SvLyo9gceSqRm25yowr-_lhpxPdY'

# 列出所有节点
curl -s http://43.165.166.56:8080/api/v1/node \
  -H 'Authorization: Bearer hskey-api-F65eEHOQ-Rbz-XZZiYgZmXDgs4bPmR7QhL5zSWK3E7H1gqpC0SvLyo9gceSqRm25yowr-_lhpxPdY'
```

---

## 2. 客户端测试

### 2.1 Go CLI 测试（本机 Linux）

```bash
cd /opt/hermes-agent/hs-poc/client

# 注册
./hs-client register \
  --server http://43.165.166.56:8080 \
  --key hskey-api-F65eEHOQ-Rbz-XZZiYgZmXDgs4bPmR7QhL5zSWK3E7H1gqpC0SvLyo9gceSqRm25yowr-_lhpxPdY \
  --name hermes-dev

# 输出节点 ID（如 node-1）

# 查看状态
./hs-client status
# → {"connected":false,"ip":"10.0.0.2","peers":[]}

# 启动 WireGuard（需要 root）
sudo ./hs-client up \
  --server http://43.165.166.56:8080 \
  --key hskey-api-F65eEHOQ-Rbz-XZZiYgZmXDgs4bPmR7QhL5zSWK3E7H1gqpC0SvLyo9gceSqRm25yowr-_lhpxPdY

# 断开
./hs-client down
```

### 2.2 Go CLI 测试（Windows）

```powershell
# 编译 Windows 版
cd client
set GOOS=windows
go build -o hs-client.exe .

# 复制到 Windows 机器，运行
hs-client.exe register --server http://43.165.166.56:8080 --key hskey-api-F65eEHOQ-Rbz-XZZiYgZmXDgs4bPmR7QhL5zSWK3E7H1gqpC0SvLyo9gceSqRm25yowr-_lhpxPdY --name windows-dev
```

### 2.3 两台设备组网测试

```
设备 A (Linux):         设备 B (Windows):
./hs-client register    hs-client.exe register
→ node-1, 10.0.0.2     → node-2, 10.0.0.3
sudo ./hs-client up     hs-client.exe up
                        hs-client.exe ping 10.0.0.2
                        → Reply from 10.0.0.2 ✅
```

### 2.4 Flutter UI 测试

```bash
cd /opt/hermes-agent/hs-poc/flutter

# Linux
flutter run -d linux

# 操作流程：
# 1. 确认 Server URL: http://43.165.166.56:8080
# 2. Admin Key: hskey-api-F65eEHOQ-Rbz-XZZiYgZmXDgs4bPmR7QhL5zSWK3E7H1gqpC0SvLyo9gceSqRm25yowr-_lhpxPdY
# 3. 点击 Connect → 状态变绿
# 4. 查看 Peers 列表
# 5. 点击 Ping 图标测试连通性
```

---

## 3. 当前部署状态

| 项目 | 状态 | 详情 |
|------|------|------|
| hs-server | 🟢 运行中 | 43.165.166.56:8080 |
| Admin Key | 🔑 已配置 | `8f132c...` |
| UFW 防火墙 | ✅ 已开放 | 8080, 50443, 3478 |
| 腾讯云安全组 | ⚠️ 需确认 | 50443, 3478 可能需手动开 |
| Gitea 仓库 | ⏳ 待创建 | http://10.0.0.86:3000/admin/hs-poc |

---

## 4. API 参考

### 注册节点

```
POST /api/v1/node/register?name=<name>
Authorization: Bearer <admin_key>

Response:
{
  "node": {
    "id": "node-1",
    "name": "my-device",
    "ip": "10.0.0.2",
    "public_key": "abc123...",
    "last_seen": "2026-05-15T07:00:00+08:00"
  }
}
```

### 列出节点

```
GET /api/v1/node
Authorization: Bearer <admin_key>

Response:
{
  "nodes": [
    {"id":"node-1","name":"my-device","ip":"10.0.0.2","public_key":"...","last_seen":"..."},
    {"id":"node-2","name":"other","ip":"10.0.0.3","public_key":"...","last_seen":"..."}
  ]
}
```

### 获取节点

```
GET /api/v1/node/{id}
Authorization: Bearer <admin_key>
```

### 心跳

```
POST /api/v1/node/{id}/keepalive
Authorization: Bearer <admin_key>

Response: {"status":"ok"}
```

### 健康检查

```
GET /health

Response: {"status":"ok"}
```

---

## 5. 故障排查

### 外网无法访问 8080

1. 检查 UFW: `sudo ufw status | grep 8080`
2. 检查服务: `sudo systemctl status hs-server`
3. 检查进程: `ss -tlnp | grep 8080`
4. **腾讯云安全组**: 登入控制台 → 安全组 → 添加入方向规则 TCP:8080

### 注册失败

```bash
# 确认 Admin Key 正确
ssh ubuntu@43.165.166.56 cat ~/hs-data/admin-key.txt

# 重置数据库
sudo systemctl stop hs-server
rm ~/hs-data/headscale.db
sudo systemctl start hs-server
```

### WireGuard TUN 创建失败

```bash
# 需要 root 权限或 CAP_NET_ADMIN
sudo setcap cap_net_admin+ep ./hs-client
# 或直接用 sudo 运行
sudo ./hs-client up
```

---

**最后更新**: 2026-05-15 07:30 CST
**部署者**: Hermes AI Team (Project Director + CCD Developer)