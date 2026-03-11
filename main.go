package main

import (
	"context"
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

func handleLogin(w http.ResponseWriter, r *http.Request) {
	pollKey := randHex(pollKeyLen)

	data, _ := json.Marshal(pollStatus{Status: "waiting"})
	if err := rdb.Set(r.Context(), pollKeyPrefix+pollKey, data, pollTTL).Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "redis unavailable"})
		return
	}

	redirectURI := url.QueryEscape(env("SERVER_URL", "") + "/wx/callback")
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

	htmlPage := func(msg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"></head><body><script>window.close();</script><p>%s</p></body></html>`, msg)
	}

	fail := func(reason string) {
		markFailed(r.Context(), pollKey)
		htmlPage("登录失败：" + reason)
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

	htmlPage("授权成功，请返回页面继续操作。")
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
