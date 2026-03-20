# auth.caiths.com 接入文档

本文档面向需要接入 `auth.caiths.com` 的业务团队，说明微信扫码登录的接入方式、接口契约、安全要求与常见问题。

## 1. 服务定位

`auth.caiths.com` 是一个统一认证服务，当前提供微信公众号 OAuth 扫码登录能力。

它负责做两件事：

- 生成微信授权地址，并为本次登录分配一次性 `poll_key`
- 在用户完成微信授权后，签发一个短期 JWT 供业务系统换取自己的登录态

它不负责做的事情：

- 不直接写入你的业务数据库
- 不直接创建你的业务用户 session
- 不替代你的用户体系、角色体系或权限体系

## 2. 整体流程

```text
业务前端
  └─ GET /wx/login
       └─ 返回 wx_url + poll_key

用户微信扫码并授权
  └─ 微信回调 /wx/callback
       └─ auth 服务换取微信用户信息
       └─ 将结果写入 Redis，状态与 poll_key 绑定

业务前端
  └─ POST /wx/poll
       └─ waiting: 继续轮询
       └─ failed: 授权失败
       └─ ok: 返回短期 JWT

业务后端
  └─ 验证 JWT
       └─ 找到或创建业务用户
       └─ 建立自己的 session / access token
```

建议的职责划分：

- 前端只负责展示二维码、轮询状态、拿到 JWT 后提交给业务后端
- 业务后端只负责验签、映射本地用户、签发自己的登录态
- `auth.caiths.com` 只负责微信 OAuth 与一次性身份凭证签发

## 3. 接入前准备

在接入前，请先准备以下信息：

- `auth.caiths.com` 已将你的业务域名加入 `ALLOWED_ORIGINS`
- 你的业务后端与 `auth.caiths.com` 共享同一个 `JWT_SECRET`
- 你的业务前端有一个可承接扫码登录结果的页面
- 你的业务后端具备“根据 `open_id` / `union_id` 查找或创建用户”的能力

推荐约定：

- 优先使用 `union_id` 作为跨应用统一身份标识
- 使用 `open_id` 作为当前微信应用内标识
- 业务系统收到 JWT 后，立即换发自己的 session，不要长期直接持有 `auth` JWT

## 4. 接口概览

### 4.1 `GET /health`

健康检查接口。

请求示例：

```http
GET https://auth.caiths.com/health
```

响应示例：

```json
{
  "status": "ok"
}
```

### 4.2 `GET /wx/login`

初始化一次扫码登录，返回本次登录对应的微信授权地址与轮询 key。

请求示例：

```http
GET https://auth.caiths.com/wx/login
```

响应示例：

```json
{
  "poll_key": "d4cc3b03d01b5a8c40d1123873d57017",
  "wx_url": "https://open.weixin.qq.com/connect/oauth2/authorize?..."
}
```

字段说明：

- `poll_key`: 本次登录的唯一轮询标识，一次性使用，默认 10 分钟过期
- `wx_url`: 微信 OAuth 授权地址，前端通常将其转成二维码供用户扫码

### 4.3 `POST /wx/poll`

轮询当前扫码登录状态。

请求头：

```http
Content-Type: application/json
```

请求体：

```json
{
  "poll_key": "d4cc3b03d01b5a8c40d1123873d57017"
}
```

等待中响应：

```json
{
  "status": "waiting"
}
```

授权失败响应：

```json
{
  "status": "failed"
}
```

授权成功响应：

```json
{
  "status": "ok",
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

失败时也可能返回标准错误：

```json
{
  "error": "missing poll_key"
}
```

或：

```json
{
  "error": "poll_key not found or expired"
}
```

## 5. 前端接入步骤

### 第一步：请求登录初始化接口

```ts
type WxLoginInit = {
  poll_key: string;
  wx_url: string;
};

async function createWxLoginSession(): Promise<WxLoginInit> {
  const response = await fetch("https://auth.caiths.com/wx/login", {
    method: "GET",
    credentials: "omit",
  });

  if (!response.ok) {
    throw new Error(`init login failed: ${response.status}`);
  }

  const data = (await response.json()) as Partial<WxLoginInit>;

  if (!data.poll_key || !data.wx_url) {
    throw new Error("invalid login init response");
  }

  return {
    poll_key: data.poll_key,
    wx_url: data.wx_url,
  };
}
```

### 第二步：展示二维码

前端拿到 `wx_url` 后，将它渲染为二维码。扫码后，用户会进入微信授权页。

### 第三步：轮询登录状态

建议轮询间隔 2 到 3 秒，超过 10 分钟后引导用户重新生成二维码。

```ts
type WxPollResponse =
  | { status: "waiting" }
  | { status: "failed" }
  | { status: "ok"; token: string }
  | { error: string };

async function pollWxLogin(pollKey: string): Promise<WxPollResponse> {
  const response = await fetch("https://auth.caiths.com/wx/poll", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ poll_key: pollKey }),
  });

  return (await response.json()) as WxPollResponse;
}
```

### 第四步：将 JWT 提交到业务后端

前端拿到 `token` 后，不要直接把它当作最终业务登录态长期存储。正确做法是：

- 立即把 `token` 提交给你的业务后端
- 由业务后端完成验签
- 由业务后端生成自己的 cookie、session 或 access token

示例：

```ts
async function exchangeWxToken(token: string) {
  const response = await fetch("/api/auth/wechat/exchange", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    credentials: "include",
    body: JSON.stringify({ token }),
  });

  if (!response.ok) {
    throw new Error("exchange token failed");
  }

  return response.json();
}
```

## 6. 后端接入步骤

你的业务后端需要完成 3 件事：

- 验证 `auth.caiths.com` 签发的 JWT
- 根据 `open_id` / `union_id` 查找或创建业务用户
- 建立自己的业务登录态

### 6.1 JWT 字段说明

当前 JWT payload 包含：

```json
{
  "open_id": "oABC123...",
  "union_id": "uXYZ789...",
  "nickname": "用户昵称",
  "avatar": "https://...",
  "iss": "caiths-auth",
  "exp": 1710912345
}
```

字段说明：

- `open_id`: 微信应用内唯一标识
- `union_id`: 微信开放平台统一标识，若可用，建议优先作为跨产品身份锚点
- `nickname`: 微信昵称
- `avatar`: 微信头像地址
- `iss`: 固定为 `caiths-auth`
- `exp`: JWT 过期时间，当前有效期 5 分钟

### 6.2 验签示例（Node.js）

```ts
import jwt from "jsonwebtoken";

type WxClaims = {
  open_id: string;
  union_id?: string;
  nickname?: string;
  avatar?: string;
  iss: string;
  exp: number;
};

function verifyAuthToken(token: string): WxClaims {
  const claims = jwt.verify(token, process.env.JWT_SECRET as string) as WxClaims;

  if (claims.iss !== "caiths-auth") {
    throw new Error("invalid issuer");
  }

  if (!claims.open_id) {
    throw new Error("missing open_id");
  }

  return claims;
}
```

### 6.3 验签示例（Go）

```go
package auth

import (
    "fmt"
    "os"

    "github.com/golang-jwt/jwt/v5"
)

type WxClaims struct {
    OpenID   string `json:"open_id"`
    UnionID  string `json:"union_id"`
    Nickname string `json:"nickname"`
    Avatar   string `json:"avatar"`
    jwt.RegisteredClaims
}

func VerifyAuthToken(raw string) (*WxClaims, error) {
    claims := &WxClaims{}
    token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (interface{}, error) {
        return []byte(os.Getenv("JWT_SECRET")), nil
    })
    if err != nil {
        return nil, err
    }
    if !token.Valid {
        return nil, fmt.Errorf("invalid token")
    }
    if claims.Issuer != "caiths-auth" {
        return nil, fmt.Errorf("invalid issuer")
    }
    if claims.OpenID == "" {
        return nil, fmt.Errorf("missing open_id")
    }
    return claims, nil
}
```

### 6.4 用户映射建议

建议按以下顺序做业务用户映射：

- 有 `union_id` 时，先按 `union_id` 查询已有用户
- 查不到时，再按 `open_id` 查询
- 都查不到时，创建新用户
- 首次登录可同步 `nickname`、`avatar` 作为初始资料

## 7. 推荐时序

### 7.1 推荐前端时序

```text
点击“微信登录”
  -> 请求 /wx/login
  -> 展示二维码
  -> 每 2~3 秒轮询 /wx/poll
  -> 收到 token
  -> 调用业务后端 /auth/wechat/exchange
  -> 业务后端写入 session / cookie
  -> 前端刷新用户状态
```

### 7.2 推荐后端时序

```text
收到前端 token
  -> 用共享 JWT_SECRET 验签
  -> 校验 iss / exp / open_id
  -> 查找或创建本地用户
  -> 建立业务 session
  -> 返回业务用户信息
```

## 8. 状态码与错误处理建议

你可以按下面的策略处理：

- `GET /wx/login` 返回非 200：提示“登录服务暂时不可用，请稍后重试”
- `POST /wx/poll` 返回 `status = waiting`：继续轮询
- `POST /wx/poll` 返回 `status = failed`：提示“微信授权未完成，请重新扫码”
- `POST /wx/poll` 返回 `error = poll_key not found or expired`：提示二维码过期，重新生成
- 业务后端验签失败：直接拒绝登录，并清理前端临时 token

建议前端在以下场景主动终止轮询：

- 用户关闭二维码弹窗
- 用户切换到其它登录方式
- 轮询时间超过 10 分钟
- 已经拿到 `status = ok`

## 9. 安全要求

- 必须使用 HTTPS 调用 `auth.caiths.com`
- `JWT_SECRET` 只能保存在服务端，不能暴露到浏览器
- 不要把 `auth` JWT 当作长期业务 access token 使用
- 成功换取业务登录态后，应立即丢弃原始 `auth` JWT
- `poll_key` 只能用于当前这一次扫码流程，不要复用
- 业务系统应校验 `iss = caiths-auth`
- 建议同时校验 `exp`、`open_id` 是否存在

## 10. 回调页说明

`/wx/callback` 是微信授权完成后的用户可见页面，主要作用是：

- 告知用户“授权成功，请返回原页面继续操作”
- 在失败时告知错误并允许用户关闭窗口
- 不建议业务系统直接把它当作前端跳转页依赖

也就是说，业务接入的真实成功判定应以 `/wx/poll` 返回 `status = ok` 为准，而不是以回调页的视觉结果为准。

## 11. 联调检查清单

上线前建议逐项确认：

- 业务域名已加入 `ALLOWED_ORIGINS`
- 前端可以成功请求 `GET /wx/login`
- 前端展示的二维码可正常扫码
- 微信授权成功后，`POST /wx/poll` 能拿到 `status = ok`
- 业务后端能正确验签 JWT
- 业务系统能完成用户创建或绑定
- 成功登录后前端能刷新成已登录状态
- 二维码过期、取消授权、后端验签失败时都有明确提示

## 12. 常见问题

### 为什么前端不能直接信任 `token` 并当作业务登录态使用？

因为这个 JWT 是 `auth.caiths.com` 签发的短期身份凭证，设计目标是跨系统身份交换，不是直接替代你的业务登录态。

### `poll_key` 过期了怎么办？

重新调用 `GET /wx/login` 生成新的二维码和新的 `poll_key`。

### 为什么回调页显示成功，但前端还没登录？

因为回调页只代表微信授权已经完成，前端仍需要等 `/wx/poll` 返回 `status = ok`，再把返回的 JWT 提交给业务后端换取本地登录态。

### 业务后端应优先使用 `open_id` 还是 `union_id`？

如果能稳定拿到 `union_id`，优先用 `union_id` 作为跨产品统一身份标识；`open_id` 更适合做当前微信应用内标识。

## 13. 联系约定

如果你需要接入新的业务域名、调整 CORS 白名单、更新共享密钥或排查联调问题，请联系 `auth.caiths.com` 的维护方统一处理。
