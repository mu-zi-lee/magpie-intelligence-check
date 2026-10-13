package check

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed web/*
var web embed.FS

func (s *Service) Handler(token string) http.Handler {
	return s.ManagedHandler(NewAccess(token, ""), nil)
}
func (s *Service) ManagedHandler(access *Access, console *Console) http.Handler {
	mux := http.NewServeMux()
	root, _ := fs.Sub(web, "web")
	mux.Handle("GET /assets/", http.FileServer(http.FS(root)))
	mux.HandleFunc("GET /modeltrace/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if name != "fingerprint-core.js" && name != "unified_bank.json" && name != "panel.js" {
			http.NotFound(w, r)
			return
		}
		b, err := web.ReadFile("web/modeltrace/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(name, ".js") {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.Write(b)
	})
	mux.HandleFunc("POST /api/console", func(w http.ResponseWriter, r *http.Request) {
		if console == nil {
			http.Error(w, "控制台管理未启用", 503)
			return
		}
		var input struct {
			Remote bool   `json:"remote"`
			Binary string `json:"binary"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Binary != "" {
			console.SetBinary(input.Binary)
		}
		link, err := console.Open(r.Context(), input.Remote)
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		writeJSON(w, map[string]string{"url": link})
	})
	mux.HandleFunc("POST /api/console/close", func(w http.ResponseWriter, r *http.Request) {
		if console != nil {
			// Let the tunnel deliver the acknowledgement before ending transport.
			time.AfterFunc(300*time.Millisecond, console.Close)
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := web.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		state, health := s.Snapshot()
		s.mu.Lock()
		lastActivity := s.lastActivity
		gatewayConfigured := s.state.Config.GatewayURL != "" && s.state.Config.GatewayKey != ""
		s.mu.Unlock()
		logHealth := "未配置日志源：后台仍会定时检测"
		if state.Config.LogPath != "" {
			f, err := os.Open(state.Config.LogPath)
			if err != nil {
				logHealth = "日志源不可读：后台仍会定时检测"
			} else {
				f.Close()
				logHealth = "日志源可读"
			}
		}
		writeJSON(w, map[string]any{"state": state, "health": health, "log_health": logHealth, "last_activity": lastActivity, "gateway_configured": gatewayConfigured, "prompts": Prompts, "guidance": Guidance})
	})
	mux.HandleFunc("GET /api/results/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		var result *Result
		for _, v := range s.state.Results {
			if v.ID == r.PathValue("id") {
				copy := v
				result = &copy
				break
			}
		}
		s.mu.Unlock()
		if result == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="result-`+result.ID+`.txt"`)
		io.WriteString(w, result.Answer)
	})
	mux.HandleFunc("GET /api/results/{id}/preview", s.htmlPreview)
	mux.HandleFunc("POST /api/results/clear", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			IDs []string `json:"ids"`
		}
		if !decode(w, r, &input) {
			return
		}
		if err := s.ClearResults(input.IDs); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/results/{id}/review", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Decision string `json:"decision"`
			Note     string `json:"note"`
		}
		if !decode(w, r, &input) {
			return
		}
		if err := s.ReviewResult(r.PathValue("id"), input.Decision, input.Note); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/target/delete", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model string `json:"model"`
		}
		if !decode(w, r, &input) {
			return
		}
		if err := s.DeleteTarget(input.Model); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		cfg := s.state.Config
		s.mu.Unlock()
		models, err := Models(r.Context(), cfg)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		writeJSON(w, models)
	})
	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		var c Config
		if !decode(w, r, &c) {
			return
		}
		if err := s.Configure(c); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/config/initial", func(w http.ResponseWriter, r *http.Request) {
		var c Config
		if !decode(w, r, &c) {
			return
		}
		if err := s.ConfigureInitial(c); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/connection/check", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		cfg := s.state.Config
		s.mu.Unlock()
		if cfg.GatewayURL == "" || cfg.GatewayKey == "" {
			http.Error(w, "请先保存 Magpie 网关 URL 和 Key", 400)
			return
		}
		models, err := Models(r.Context(), cfg)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "model_count": len(models)})
	})
	mux.HandleFunc("POST /api/target", func(w http.ResponseWriter, r *http.Request) {
		var t Target
		if !decode(w, r, &t) {
			return
		}
		if err := s.SetTarget(t.Model, t.Enabled); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/action", func(w http.ResponseWriter, r *http.Request) {
		var a struct{ Model, Action, Prompt string }
		if !decode(w, r, &a) {
			return
		}
		var err error
		if a.Action == "cancel-test" {
			err = s.CancelTest(a.Model)
		} else if a.Action == "modeltrace" {
			err = s.TriggerModelTrace(a.Model)
		} else if a.Action == "test" {
			if a.Prompt == "" {
				a.Prompt = "milk"
			}
			err = s.Trigger(a.Model, a.Prompt, true)
		} else {
			err = s.Action(a.Model, a.Action)
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob:; font-src 'self'; frame-src 'self'; frame-ancestors 'none'; connect-src 'self'")
		// A public readiness probe exposes no management state or credentials.
		if r.Method == "GET" && r.URL.Path == "/health" {
			w.Header().Set("X-Magpie-Quality", "ready")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		cookie, e := r.Cookie("quality_session")
		if e == nil && supplied == "" {
			supplied = cookie.Value
		}
		if strings.HasPrefix(r.URL.Path, "/open/") && r.Method == "GET" {
			if !access.Origin("http://"+r.Host) && !access.Origin("https://"+r.Host) {
				http.Error(w, "管理域名不匹配", 403)
				return
			}
			session, valid := access.Exchange(strings.TrimPrefix(r.URL.Path, "/open/"))
			if !valid {
				http.Error(w, "管理链接已失效，请重新启动入口", 401)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "quality_session", Value: session, Path: "/", HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteLaxMode, MaxAge: 1800})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		control := subtle.ConstantTimeCompare([]byte(supplied), []byte(access.control)) == 1
		if !control && !access.Session(supplied) {
			http.Error(w, "请使用插件生成的完整管理链接", 401)
			return
		}
		if r.URL.Path == "/api/config/initial" && !control {
			http.Error(w, "仅插件可初始化连接", 403)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/console") && !control {
			if r.URL.Path != "/api/console/close" {
				http.Error(w, "仅插件可签发管理入口", 403)
				return
			}
		}
		if r.Method != "GET" {
			origin := r.Header.Get("Origin")
			if origin != "" && !access.Origin(origin) {
				http.Error(w, "Origin 不匹配", 403)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	if err := d.Decode(v); err != nil {
		http.Error(w, "无效请求", 400)
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		http.Error(w, "无效请求", 400)
		return false
	}
	return true
}
