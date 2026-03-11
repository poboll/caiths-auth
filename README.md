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

```bash
# 编译
go build -o caiths-auth main.go

# 运行
./caiths-auth
```

## 安全说明

- `poll_key` 一次性使用，poll 成功后立即删除
- JWT 有效期 5 分钟，产品收到后应立即换成自己的 session
- CORS 白名单控制允许的调用域名
- Redis 使用独立 DB（默认 DB=1）避免冲突
