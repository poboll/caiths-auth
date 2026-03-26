package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

//go:embed html_templates/index.html
var indexHTML []byte

//go:embed html_templates/docs.html
var docsHTML []byte

//go:embed docs/AUTH_INTEGRATION_GUIDE.md
var docsContent []byte

type wxTokenResp struct {
	AccessToken string `json:"access_token"`
	OpenID      string `json:"openid"`
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
}

type wxUserInfo struct {
	OpenID     string `json:"openid"`
	Nickname   string `json:"nickname"`
	HeadImgURL string `json:"headimgurl"`
	UnionID    string `json:"unionid"`
	ErrCode    int    `json:"errcode"`
	ErrMsg     string `json:"errmsg"`
}

type pollStatus struct {
	Status   string `json:"status"` // "waiting" | "ok" | "failed"
	OpenID   string `json:"open_id,omitempty"`
	UnionID  string `json:"union_id,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Avatar   string `json:"avatar,omitempty"`
}

const (
	pollKeyPrefix = "caiths_auth:poll:"
	pollTTL       = 10 * time.Minute
	jwtTTL        = 5 * time.Minute
	pollKeyLen    = 32
)

var rdb *redis.Client

func main() {
	_ = godotenv.Load()

	dbIndex, _ := strconv.Atoi(env("REDIS_DB", "1"))
	rdb = redis.NewClient(&redis.Options{
		Addr:     env("REDIS_ADDR", "localhost:6379"),
		Password: env("REDIS_PASSWORD", ""),
		DB:       dbIndex,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("GET /docs", handleDocs)
	mux.HandleFunc("GET /api/docs/content", handleDocsContent)
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /wx/login", handleLogin)
	mux.HandleFunc("GET /wx/callback", handleCallback)
	mux.HandleFunc("POST /wx/poll", handlePoll)

	port := env("PORT", "4000")
	log.Printf("caiths-auth listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, corsMiddleware(mux)))
}

func corsMiddleware(next http.Handler) http.Handler {
	origins := strings.Split(env("ALLOWED_ORIGINS", ""), ",")
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(docsHTML)
}

func handleDocsContent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write(docsContent)
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	pollKey := randHex(pollKeyLen)

	data, _ := json.Marshal(pollStatus{Status: "waiting"})
	if err := rdb.Set(r.Context(), pollKeyPrefix+pollKey, data, pollTTL).Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "redis unavailable"})
		return
	}

	callbackURL := env("WX_CALLBACK_URL", "")
	if callbackURL == "" {
		callbackURL = env("SERVER_URL", "") + "/wx/callback"
	}
	redirectURI := url.QueryEscape(callbackURL)
	wxURL := fmt.Sprintf(
		"https://open.weixin.qq.com/connect/oauth2/authorize?appid=%s&redirect_uri=%s&response_type=code&scope=snsapi_userinfo&state=%s#wechat_redirect",
		env("WX_APP_ID", ""), redirectURI, pollKey,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"wx_url":   wxURL,
		"poll_key": pollKey,
	})
}

func handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	pollKey := r.URL.Query().Get("state")

	htmlPage := func(title, desc string, isError bool) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		icon := `<svg fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"></path></svg>`
		if isError {
			icon = `<svg fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"></path></svg>`
		}

		autoClose := ""
		if !isError {
			autoClose = `setTimeout(function(){ window.close(); }, 2000);`
		}

		html := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no">
    <title>%s</title>
    <link rel="icon" href="data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><rect width='100' height='100' rx='20' fill='%%23000'/><path d='M 65 35 A 22 22 0 1 0 65 65' fill='none' stroke='%%23fff' stroke-width='14' stroke-linecap='round'/></svg>" media="(prefers-color-scheme: light)">
    <link rel="icon" href="data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><rect width='100' height='100' rx='20' fill='%%23fff'/><path d='M 65 35 A 22 22 0 1 0 65 65' fill='none' stroke='%%23000' stroke-width='14' stroke-linecap='round'/></svg>" media="(prefers-color-scheme: dark)">
    <style>
        :root {
            --bg: #ffffff;
            --fg: #000000;
            --border: #eaeaea;
            --text-muted: #666666;
            --icon-bg: #f5f5f5;
        }
        @media (prefers-color-scheme: dark) {
            :root {
                --bg: #000000;
                --fg: #ffffff;
                --border: #333333;
                --text-muted: #a0a0a0;
                --icon-bg: #1a1a1a;
            }
        }
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            background-color: var(--bg);
            color: var(--fg);
            display: flex;
            align-items: center;
            justify-content: center;
            min-height: 100vh;
            padding: 20px;
            -webkit-font-smoothing: antialiased;
            position: relative;
        }
        body::before {
            content: "";
            position: absolute;
            top: 0;
            left: 0;
            right: 0;
            height: 100vh;
            background-image: linear-gradient(var(--border) 1px, transparent 1px), linear-gradient(90deg, var(--border) 1px, transparent 1px);
            background-size: 40px 40px;
            background-position: center top;
            opacity: 0.5;
            mask-image: linear-gradient(to bottom, rgba(0,0,0,1) 0%%, rgba(0,0,0,0) 100%%);
            -webkit-mask-image: linear-gradient(to bottom, rgba(0,0,0,1) 0%%, rgba(0,0,0,0) 100%%);
            pointer-events: none;
            z-index: -1;
        }
        .card {
            width: 100%%;
            max-width: 360px;
            padding: 40px 32px;
            background: var(--bg);
            border: 1px solid var(--border);
            border-radius: 16px;
            text-align: center;
            box-shadow: 0 12px 40px rgba(0,0,0,0.04);
        }
        @media (prefers-color-scheme: dark) {
            .card { box-shadow: 0 12px 40px rgba(0,0,0,0.2); }
        }
        .icon {
            width: 48px;
            height: 48px;
            margin: 0 auto 24px;
            border-radius: 50%%;
            background: var(--icon-bg);
            border: 1px solid var(--border);
            display: flex;
            align-items: center;
            justify-content: center;
        }
        .icon svg {
            width: 20px;
            height: 20px;
        }
        h1 {
            font-size: 20px;
            font-weight: 600;
            letter-spacing: -0.02em;
            margin-bottom: 8px;
        }
        p {
            font-size: 14px;
            color: var(--text-muted);
            margin-bottom: 32px;
            line-height: 1.6;
        }
        .btn {
            appearance: none;
            background: var(--fg);
            color: var(--bg);
            border: none;
            padding: 0 16px;
            height: 44px;
            border-radius: 8px;
            font-size: 14px;
            font-weight: 500;
            width: 100%%;
            cursor: pointer;
            transition: opacity 0.2s, transform 0.1s;
        }
        .btn:hover { opacity: 0.85; }
        .btn:active { transform: scale(0.98); }
    </style>
</head>
<body>
    <div class="card">
        <div class="icon">%s</div>
        <h1>%s</h1>
        <p>%s</p>
        <button class="btn" onclick="window.close()">关闭窗口</button>
    </div>
    <script>
        %s
    </script>
</body>
</html>`
		fmt.Fprintf(w, html, title, icon, title, desc, autoClose)
	}

	fail := func(reason string) {
		markFailed(r.Context(), pollKey)
		htmlPage("登录失败", reason, true)
	}

	if code == "" || pollKey == "" {
		fail("参数缺失")
		return
	}

	var tokenResp wxTokenResp
	tokenURL := fmt.Sprintf(
		"https://api.weixin.qq.com/sns/oauth2/access_token?appid=%s&secret=%s&code=%s&grant_type=authorization_code",
		env("WX_APP_ID", ""), env("WX_APP_SECRET", ""), code,
	)
	if err := fetchJSON(tokenURL, &tokenResp); err != nil || tokenResp.ErrCode != 0 {
		fail("获取授权失败")
		return
	}

	var user wxUserInfo
	userURL := fmt.Sprintf(
		"https://api.weixin.qq.com/sns/userinfo?access_token=%s&openid=%s&lang=zh_CN",
		tokenResp.AccessToken, tokenResp.OpenID,
	)
	if err := fetchJSON(userURL, &user); err != nil || user.ErrCode != 0 {
		fail("获取用户信息失败")
		return
	}

	data, _ := json.Marshal(pollStatus{
		Status:   "ok",
		OpenID:   user.OpenID,
		UnionID:  user.UnionID,
		Nickname: user.Nickname,
		Avatar:   user.HeadImgURL,
	})
	rdb.Set(r.Context(), pollKeyPrefix+pollKey, data, pollTTL)

	htmlPage("授权成功", "您已成功授权，请关闭此窗口并返回原应用继续操作。", false)
}

func handlePoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PollKey string `json:"poll_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PollKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing poll_key"})
		return
	}

	raw, err := rdb.Get(r.Context(), pollKeyPrefix+body.PollKey).Bytes()
	if err == redis.Nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "poll_key not found or expired"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "redis unavailable"})
		return
	}

	var s pollStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "invalid state"})
		return
	}

	if s.Status != "ok" {
		writeJSON(w, http.StatusOK, map[string]string{"status": s.Status})
		return
	}

	rdb.Del(r.Context(), pollKeyPrefix+body.PollKey)
	token, err := signJWT(s)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "jwt signing failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "token": token})
}

func signJWT(s pollStatus) (string, error) {
	claims := jwt.MapClaims{
		"open_id":  s.OpenID,
		"union_id": s.UnionID,
		"nickname": s.Nickname,
		"avatar":   s.Avatar,
		"iss":      "caiths-auth",
		"exp":      time.Now().Add(jwtTTL).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(env("JWT_SECRET", "")))
}

func markFailed(ctx context.Context, pollKey string) {
	if pollKey == "" {
		return
	}
	data, _ := json.Marshal(pollStatus{Status: "failed"})
	rdb.Set(ctx, pollKeyPrefix+pollKey, data, 2*time.Minute)
}

func fetchJSON(rawURL string, v any) error {
	resp, err := http.Get(rawURL) //nolint:noctx
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func randHex(n int) string {
	const chars = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
