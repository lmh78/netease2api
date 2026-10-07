package gateway

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const Model = "netease-fox"

//go:generate python ../../scripts/source_archive.py
//go:embed web/*
var assets embed.FS

type Server struct {
	Engine   *Engine
	AdminKey string
	Version  string
	slots    chan struct{}
	checkMu  sync.Mutex
}

func NewServer(engine *Engine, admin string) *Server {
	return &Server{Engine: engine, AdminKey: admin, Version: "0.3.0", slots: make(chan struct{}, 64)}
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func apiError(w http.ResponseWriter, status int, message, kind, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": kind, "param": nil, "code": code}})
}
func token(r *http.Request) string {
	p := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(p) == 2 && strings.EqualFold(p[0], "Bearer") {
		return strings.TrimSpace(p[1])
	}
	return ""
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("请求必须是有效 JSON，且不超过 4 MiB")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("请求包含多余 JSON 内容")
	}
	return nil
}
func (s *Server) require(next http.HandlerFunc, admin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret := token(r)
		ok := secret != "" && s.Engine.Store.Authenticate(secret)
		if admin {
			ok = secret != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(s.AdminKey)) == 1
		}
		if !ok {
			apiError(w, 401, "密钥无效或已停用", "authentication_error", "invalid_api_key")
			return
		}
		next(w, r)
	}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "version": s.Version})
	})
	mux.HandleFunc("GET /v1/models", s.require(s.models, false))
	mux.HandleFunc("POST /v1/chat/completions", s.require(s.chat, false))
	mux.HandleFunc("GET /admin/overview", s.require(s.overview, true))
	mux.HandleFunc("GET /admin/accounts", s.require(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.Engine.Store.Accounts()) }, true))
	mux.HandleFunc("POST /admin/accounts/import", s.require(s.importAccounts, true))
	mux.HandleFunc("PATCH /admin/accounts/{id}", s.require(s.updateAccount, true))
	mux.HandleFunc("DELETE /admin/accounts/{id}", s.require(s.deleteAccount, true))
	mux.HandleFunc("POST /admin/accounts/{id}/check", s.require(s.checkAccount, true))
	mux.HandleFunc("POST /admin/accounts/check", s.require(s.checkAll, true))
	mux.HandleFunc("GET /admin/keys", s.require(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.Engine.Store.Keys()) }, true))
	mux.HandleFunc("POST /admin/keys", s.require(s.createKey, true))
	mux.HandleFunc("DELETE /admin/keys/{id}", s.require(s.revokeKey, true))
	mux.HandleFunc("GET /admin/logs", s.require(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.Engine.Store.Logs()) }, true))
	files, _ := fs.Sub(assets, "web")
	mux.Handle("GET /", http.FileServer(http.FS(files)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(204)
				return
			}
		} else {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": Model, "object": "model", "created": 1791283200, "owned_by": "netease"}}})
}

type Message struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls json.RawMessage `json:"tool_calls"`
}
type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Stream         bool            `json:"stream"`
	N              int             `json:"n"`
	Tools          json.RawMessage `json:"tools"`
	Functions      json.RawMessage `json:"functions"`
	ResponseFormat json.RawMessage `json:"response_format"`
}

func nonemptyJSON(v json.RawMessage) bool {
	return len(v) > 0 && string(v) != "null" && string(v) != "[]"
}
func textContent(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return "", fmt.Errorf("仅支持文本消息")
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return "", fmt.Errorf("不支持图片、音频或其他多模态消息")
		}
		b.WriteString(p.Text)
	}
	return b.String(), nil
}
func (request *ChatRequest) prompt() (string, error) {
	if request.Model != Model {
		return "", fmt.Errorf("model 必须为 %s", Model)
	}
	if len(request.Messages) == 0 || len(request.Messages) > 100 {
		return "", fmt.Errorf("messages 需包含 1–100 条文本消息")
	}
	if request.N > 1 || request.N < 0 {
		return "", fmt.Errorf("仅支持 n=1")
	}
	if nonemptyJSON(request.Tools) || nonemptyJSON(request.Functions) {
		return "", fmt.Errorf("狐狸接口不支持工具调用")
	}
	if nonemptyJSON(request.ResponseFormat) {
		var format struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(request.ResponseFormat, &format) != nil || format.Type != "text" {
			return "", fmt.Errorf("仅支持 text 响应格式")
		}
	}
	var b strings.Builder
	roles := map[string]string{"user": "用户", "assistant": "助手", "system": "要求", "developer": "要求"}
	for i, m := range request.Messages {
		label, ok := roles[m.Role]
		if !ok || nonemptyJSON(m.ToolCalls) {
			return "", fmt.Errorf("不支持该消息角色或工具调用")
		}
		text, err := textContent(m.Content)
		if err != nil {
			return "", err
		}
		if len(request.Messages) > 1 {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(label + "：")
		}
		b.WriteString(text)
	}
	if request.Messages[len(request.Messages)-1].Role != "user" {
		return "", fmt.Errorf("最后一条消息必须来自 user")
	}
	result := strings.TrimSpace(b.String())
	if result == "" {
		return "", fmt.Errorf("提问内容不能为空")
	}
	if len([]rune(result)) > 200 {
		return "", fmt.Errorf("上游输入最多 200 字，完整上下文超过限制；请缩短 messages")
	}
	return result, nil
}
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var request ChatRequest
	if err := decode(w, r, &request); err != nil {
		apiError(w, 400, err.Error(), "invalid_request_error", "invalid_json")
		return
	}
	prompt, err := request.prompt()
	if err != nil {
		apiError(w, 400, err.Error(), "invalid_request_error", "unsupported_request")
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		apiError(w, 429, "网关并发请求已满，请稍后重试", "rate_limit_error", "gateway_busy")
		return
	}
	id := "chatcmpl-" + uuid.NewString()
	created := time.Now().Unix()
	w.Header().Set("X-Request-ID", id)
	w.Header().Set("X-Netease2api-Stream-Mode", "buffered")
	if !request.Stream {
		answer, err := s.Engine.Complete(r.Context(), prompt, id)
		if err != nil {
			s.writeFailure(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "object": "chat.completion", "created": created, "model": Model, "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": answer}, "logprobs": nil, "finish_reason": "stop"}}, "usage": nil})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		apiError(w, 500, "服务不支持 SSE", "server_error", "stream_unavailable")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	type result struct {
		answer string
		err    error
	}
	ch := make(chan result, 1)
	go func() { answer, err := s.Engine.Complete(ctx, prompt, id); ch <- result{answer, err} }()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(value any) bool {
		b, _ := json.Marshal(value)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	chunk := func(delta map[string]string, finish any) map[string]any {
		return map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "logprobs": nil, "finish_reason": finish}}}
	}
	if !send(chunk(map[string]string{"role": "assistant", "content": ""}, nil)) {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := io.WriteString(w, ": waiting\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case result := <-ch:
			if result.err != nil {
				send(map[string]any{"error": map[string]any{"message": publicError(result.err), "type": "server_error", "code": "upstream_error"}})
			} else {
				runes := []rune(result.answer)
				for start := 0; start < len(runes); start += 32 {
					if !send(chunk(map[string]string{"content": string(runes[start:min(start+32, len(runes))])}, nil)) {
						return
					}
				}
				if !send(chunk(map[string]string{}, "stop")) {
					return
				}
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
	}
}
func (s *Server) writeFailure(w http.ResponseWriter, err error) {
	status := 502
	code := "upstream_error"
	message := publicError(err)
	if errors.Is(err, ErrUnavailable) {
		status = 503
		code = "no_available_accounts"
		message = err.Error()
		w.Header().Set("Retry-After", "30")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		status = 504
		code = "upstream_timeout"
	}
	apiError(w, status, message, "server_error", code)
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	accounts := s.Engine.Store.Accounts()
	ready, busy, success, failed := 0, 0, 0, 0
	for _, a := range accounts {
		if a.Enabled && (a.Status == "ready" || a.Status == "unverified") {
			ready++
		}
		if a.Busy {
			busy++
		}
		success += a.Success
		failed += a.Failed
	}
	writeJSON(w, 200, map[string]any{"version": s.Version, "model": Model, "accounts": len(accounts), "available": ready, "busy": busy, "successful_requests": success, "failed_attempts": failed, "max_input_chars": 200, "stream_mode": "buffered", "base_path": "/v1", "uptime_note": "使用历史查询取得完整回答"})
}
func (s *Server) importAccounts(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Content string `json:"content"`
	}
	if err := decode(w, r, &payload); err != nil {
		apiError(w, 400, err.Error(), "invalid_request_error", "invalid_import")
		return
	}
	added, updated, err := s.Engine.Store.Import(payload.Content)
	if err != nil {
		apiError(w, 400, err.Error(), "invalid_request_error", "invalid_import")
		return
	}
	writeJSON(w, 200, map[string]int{"added": added, "updated": updated})
}
func mutationError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		apiError(w, 404, "未找到该项目", "invalid_request_error", "not_found")
	} else {
		apiError(w, 409, err.Error(), "invalid_request_error", "conflict")
	}
}
func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decode(w, r, &payload); err != nil {
		apiError(w, 400, err.Error(), "invalid_request_error", "invalid_json")
		return
	}
	if err := s.Engine.Store.Update(r.PathValue("id"), payload.Enabled, false); err != nil {
		mutationError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Store.Update(r.PathValue("id"), nil, true); err != nil {
		mutationError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) checkAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	found := false
	for _, a := range s.Engine.Store.Accounts() {
		if a.ID == id {
			found = true
			if a.Busy {
				apiError(w, 409, "账号正在使用", "invalid_request_error", "account_busy")
				return
			}
			break
		}
	}
	if !found {
		mutationError(w, os.ErrNotExist)
		return
	}
	if err := s.Engine.Check(r.Context(), id); err != nil {
		s.writeFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) checkAll(w http.ResponseWriter, r *http.Request) {
	if !s.checkMu.TryLock() {
		apiError(w, 409, "批量检查正在进行", "invalid_request_error", "check_busy")
		return
	}
	defer s.checkMu.Unlock()
	good, bad, skipped := 0, 0, 0
	for _, a := range s.Engine.Store.Accounts() {
		if !a.Enabled || a.Busy {
			skipped++
			continue
		}
		if r.Context().Err() != nil {
			return
		}
		if err := s.Engine.Check(r.Context(), a.ID); err != nil {
			bad++
		} else {
			good++
		}
	}
	writeJSON(w, 200, map[string]int{"ok": good, "failed": bad, "skipped": skipped})
}
func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name string `json:"name"`
	}
	if err := decode(w, r, &payload); err != nil {
		apiError(w, 400, err.Error(), "invalid_request_error", "invalid_json")
		return
	}
	view, secret, err := s.Engine.Store.CreateKey(payload.Name)
	if err != nil {
		apiError(w, 400, err.Error(), "invalid_request_error", "invalid_name")
		return
	}
	writeJSON(w, 201, map[string]any{"key": secret, "record": view})
}
func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Store.RevokeKey(r.PathValue("id")); err != nil {
		mutationError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
