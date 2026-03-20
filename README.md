# caiths-auth

轻量级微信公众号统一认证服务，为多个产品提供微信扫码登录能力。

## 架构

- **语言**: Go 1.22
- **依赖**: Redis
- **内存占用**: ~5MB
- **并发**: 单实例足够处理低并发场景

## 快速开始

### 1. 环境配置

```bash
cp .env.example .env
# 编辑 .env 填入真实配置
```

### 2. 安装依赖

```bash
go mod tidy
```

### 3. 运行服务

```bash
go run main.go
```

## API 接口

### GET /wx/login

发起微信登录，返回二维码 URL 和轮询 key。

**响应**:
```json
{
  "wx_url": "https://open.weixin.qq.com/connect/oauth2/authorize?...",
  "poll_key": "abc123..."
}
```

### POST /wx/poll

轮询登录状态。

**请求**:
```json
{
  "poll_key": "abc123..."
}
```

**响应**:
```json
{
  "status": "waiting"
}
```
或
```json
{
  "status": "ok",
  "token": "eyJhbGc..."
}
```

## 产品接入

### 1. 共享 JWT_SECRET

将 `caiths-auth` 的 `JWT_SECRET` 配置到产品的 `.env` 中。

### 2. 前端集成

```typescript
// 1. 获取登录 URL
const { wx_url, poll_key } = await fetch('https://auth.caiths.com/wx/login').then(r => r.json());

// 2. 渲染二维码
QRCode.toCanvas(canvas, wx_url);

// 3. 轮询登录状态
const interval = setInterval(async () => {
  const res = await fetch('https://auth.caiths.com/wx/poll', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ poll_key })
  }).then(r => r.json());
  
  if (res.status === 'ok') {
    clearInterval(interval);
    // 4. 发送 token 到自己的后端
    await yourBackend.login({ wx_token: res.token });
  }
}, 3000);
```

### 3. 后端验证

```go
import "github.com/golang-jwt/jwt/v5"

func verifyWxToken(tokenString string) (*WxClaims, error) {
    token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
        return []byte(os.Getenv("JWT_SECRET")), nil
    })
    if err != nil || !token.Valid {
        return nil, err
    }
    
    claims := token.Claims.(jwt.MapClaims)
    openID := claims["open_id"].(string)
    
    // 根据 open_id 查找或创建用户
    user := findOrCreateUser(openID)
    
    // 建立自己的 session
    return user, nil
}
```

## JWT Payload

```json
{
  "open_id": "oABC123...",
  "union_id": "uXYZ789...",
  "nickname": "用户昵称",
  "avatar": "https://...",
  "iss": "caiths-auth",
  "exp": 1234567890
}
```

## 部署

### 生产环境部署

**服务器要求**：
- Go 1.22+
- Redis 6.0+
- 反向代理（Nginx/Caddy）

**部署步骤**：

```bash
# 1. 上传代码到服务器
scp -r . user@server:/path/to/caiths-auth

# 2. 配置环境变量
cp .env.example .env
# 编辑 .env 填入生产配置

# 3. 编译
go build -o caiths-auth main.go

# 4. 使用 systemd 管理服务
sudo tee /etc/systemd/system/caiths-auth.service > /dev/null <<EOF
[Unit]
Description=Caiths Auth Service
After=network.target redis.service

[Service]
Type=simple
User=www-data
WorkingDirectory=/path/to/caiths-auth
ExecStart=/path/to/caiths-auth/caiths-auth
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF

# 5. 启动服务
sudo systemctl daemon-reload
sudo systemctl enable caiths-auth
sudo systemctl start caiths-auth
```

**Nginx 反向代理配置**：

```nginx
server {
    listen 443 ssl http2;
    server_name auth.caiths.com;

    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    location / {
        proxy_pass http://127.0.0.1:4000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

### 本地开发

```bash
# 安装依赖
go mod tidy

# 运行
go run main.go
```

## 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `PORT` | 服务端口 | `4000` |
| `REDIS_ADDR` | Redis 地址 | `localhost:6379` |
| `REDIS_PASSWORD` | Redis 密码 | `` |
| `REDIS_DB` | Redis 数据库编号 | `1` |
| `WX_APP_ID` | 微信公众号 AppID | 必填 |
| `WX_APP_SECRET` | 微信公众号 AppSecret | 必填 |
| `WX_CALLBACK_URL` | 微信回调地址 | `SERVER_URL/wx/callback` |
| `SERVER_URL` | 服务器地址 | 必填 |
| `JWT_SECRET` | JWT 签名密钥 | 必填 |
| `ALLOWED_ORIGINS` | CORS 白名单（逗号分隔） | 必填 |

## 回调页面

微信授权成功/失败后会显示一个精简的回调页面：
- 采用黑白灰高质感设计，自动适配深色模式
- 成功时自动关闭窗口（2秒后）
- 失败时显示错误原因，需手动关闭
- 响应式布局，移动端友好

## 安全说明

- `poll_key` 一次性使用，poll 成功后立即删除
- JWT 有效期 5 分钟，产品收到后应立即换成自己的 session
- CORS 白名单控制允许的调用域名
- Redis 使用独立 DB（默认 DB=1）避免冲突
- 建议使用 HTTPS 部署，保护用户隐私
