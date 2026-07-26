package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/madtoby2/zyzu/internal/config"
	"github.com/madtoby2/zyzu/internal/poster"
	"github.com/madtoby2/zyzu/internal/scheduler"
	"github.com/madtoby2/zyzu/internal/server"
	"github.com/madtoby2/zyzu/internal/store"
)

type Handler struct {
	store *store.Store
	sched *scheduler.Scheduler
	cfg   *config.Config
	hub   *server.WSHub
}

func New(st *store.Store, sched *scheduler.Scheduler, cfg *config.Config, hub *server.WSHub) *Handler {
	return &Handler{store: st, sched: sched, cfg: cfg, hub: hub}
}

func (h *Handler) Register(r chi.Router) {
	// Public read-only
	r.Get("/api/stations", h.getStations)
	r.Get("/api/stations/stats", h.getStats)
	r.Get("/api/history", h.getHistory)
	r.Get("/api/event-log", h.getEventLog)
	r.Get("/api/status", h.getStatus)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// Protected write operations
	r.Group(func(r chi.Router) {
		r.Use(h.authMiddleware)
		r.Post("/api/stations/{slug}/blacklist", h.toggleBlacklist)
		r.Post("/api/stations/{slug}/category", h.updateCategory)
		r.Post("/api/stations/{slug}/probe", h.probeStation)
		r.Post("/api/stations/{slug}/post", h.manualPost)
		r.Post("/api/trigger", h.triggerScrape)
		r.Post("/api/content/trigger", h.triggerContent)
		r.Get("/api/config", h.getConfig)
		r.Put("/api/config", h.updateConfig)
		r.Post("/api/event-log", h.postEventLog)
		r.Delete("/api/event-log", h.clearEventLog)
		r.Post("/api/channels", h.addChannel)
		r.Delete("/api/channels", h.deleteChannel)
		r.Post("/api/telethon/request-code", h.telethonRequestCode)
		r.Post("/api/telethon/login", h.telethonLogin)
		r.Get("/api/telethon/status", h.telethonStatus)
		r.Get("/ws", h.hub.HandleWS)
	})
}

func (h *Handler) telethonRequestCode(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Phone string `json:"phone"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	out, err := poster.TelethonAction("request_code", b.Phone, "", "")
	if err != nil {
		jsonError(w, string(out), 500)
		return
	}
	jsonOK(w, string(out))
}
func (h *Handler) telethonLogin(w http.ResponseWriter, r *http.Request) {
	var b struct{ Phone, Code, Password string }
	json.NewDecoder(r.Body).Decode(&b)
	out, err := poster.TelethonAction("login", b.Phone, b.Code, b.Password)
	if err != nil {
		jsonError(w, string(out), 500)
		return
	}
	jsonOK(w, string(out))
}
func (h *Handler) telethonStatus(w http.ResponseWriter, r *http.Request) {
	out, err := poster.TelethonAction("status", "", "", "")
	if err != nil {
		jsonError(w, string(out), 500)
		return
	}
	jsonOK(w, string(out))
}

func (h *Handler) addChannel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Category string `json:"category"`
		ID       int64  `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Category) == "" || body.ID == 0 {
		jsonError(w, "category and id required", 400)
		return
	}
	if h.cfg.ChannelMap == nil {
		h.cfg.ChannelMap = map[string][]int64{}
	}
	cat := strings.TrimSpace(body.Category)
	for _, id := range h.cfg.ChannelMap[cat] {
		if id == body.ID {
			jsonOK(w, h.cfg.ChannelMap)
			return
		}
	}
	h.cfg.ChannelMap[cat] = append(h.cfg.ChannelMap[cat], body.ID)
	if err := h.cfg.Save("config.json"); err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	h.hub.Broadcast("channel_changed", map[string]interface{}{"action": "add", "category": cat, "id": body.ID})
	jsonOK(w, h.cfg.ChannelMap)
}

func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Category string `json:"category"`
		ID       int64  `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Category) == "" || body.ID == 0 {
		jsonError(w, "category and id required", 400)
		return
	}
	cat := strings.TrimSpace(body.Category)
	ids := h.cfg.ChannelMap[cat]
	out := ids[:0]
	for _, id := range ids {
		if id != body.ID {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		delete(h.cfg.ChannelMap, cat)
	} else {
		h.cfg.ChannelMap[cat] = out
	}
	if err := h.cfg.Save("config.json"); err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	h.hub.Broadcast("channel_changed", map[string]interface{}{"action": "delete", "category": cat, "id": body.ID})
	jsonOK(w, h.cfg.ChannelMap)
}

func (h *Handler) updateCategory(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var body struct {
		Category string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", 400)
		return
	}
	body.Category = strings.TrimSpace(body.Category)
	if body.Category == "" {
		jsonError(w, "category required", 400)
		return
	}
	if err := h.store.SetCategory(slug, body.Category); err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	h.hub.Broadcast("category_changed", map[string]string{"slug": slug, "category": body.Category})
	jsonOK(w, map[string]string{"status": "ok"})
}

func (h *Handler) probeStation(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	st, err := h.store.GetStationBySlug(slug)
	if err != nil {
		jsonError(w, "station not found: "+err.Error(), 404)
		return
	}
	// A lightweight probe reuses the station API URL and records a clear result.
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, st.APIURL, nil)
	if err != nil {
		jsonError(w, err.Error(), 400)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	migrated := false
	// Retired API subdomains commonly fail with DNS, TLS, timeout, or an HTTP
	// error while the same path remains available on the apex domain.
	if err != nil || resp.StatusCode >= 400 {
		if resp != nil {
			resp.Body.Close()
		}
		fallback := apexAPIFallback(st.APIURL)
		if fallback == "" || fallback == st.APIURL {
			if err != nil {
				jsonError(w, "probe failed: "+err.Error(), 502)
			} else {
				jsonError(w, fmt.Sprintf("probe failed: HTTP %d", resp.StatusCode), 502)
			}
			return
		}
		req2, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, fallback, nil)
		if reqErr != nil {
			jsonError(w, "probe failed: "+reqErr.Error(), 502)
			return
		}
		resp, err = client.Do(req2)
		if err != nil {
			jsonError(w, "probe failed: "+err.Error(), 502)
			return
		}
		if resp.StatusCode >= 400 {
			status := resp.StatusCode
			resp.Body.Close()
			jsonError(w, fmt.Sprintf("probe failed: HTTP %d", status), 502)
			return
		}
		if updateErr := h.store.UpdateAPIURL(slug, fallback); updateErr != nil {
			resp.Body.Close()
			jsonError(w, "probe succeeded but endpoint save failed: "+updateErr.Error(), 500)
			return
		}
		migrated = true
	}
	resp.Body.Close()
	availability := "0%"
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		availability = "100%"
	}
	if err := h.store.UpdateHealth(slug, availability, fmt.Sprintf("%dms", time.Since(start).Milliseconds())); err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	result := map[string]string{"availability": availability, "response_time": fmt.Sprintf("%dms", time.Since(start).Milliseconds())}
	if migrated {
		result["migrated"] = "true"
	}
	jsonOK(w, result)
}

func apexAPIFallback(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || !strings.HasPrefix(u.Hostname(), "api.") {
		return ""
	}
	u.Host = strings.TrimPrefix(u.Host, "api.")
	return u.String()
}

func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.cfg.APIKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get("X-API-Key")
		if key == "" {
			key = r.URL.Query().Get("api_key")
		}
		if key != h.cfg.APIKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) getStations(w http.ResponseWriter, r *http.Request) {
	includeBlacklisted := r.URL.Query().Get("all") == "1"
	stations, err := h.store.GetStations(includeBlacklisted)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	jsonOK(w, stations)
}

func (h *Handler) getStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.store.GetStats()
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	jsonOK(w, stats)
}

func (h *Handler) toggleBlacklist(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var body struct {
		Blacklisted bool `json:"blacklisted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", 400)
		return
	}
	if err := h.store.SetBlacklist(slug, body.Blacklisted); err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	h.hub.Broadcast("blacklist_changed", map[string]interface{}{
		"slug":        slug,
		"blacklisted": body.Blacklisted,
	})
	jsonOK(w, map[string]string{"status": "ok"})
}

func (h *Handler) manualPost(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	st, err := h.store.GetStationBySlug(slug)
	if err != nil {
		jsonError(w, "station not found: "+err.Error(), 404)
		return
	}
	if st.Blacklisted {
		jsonError(w, "station is blacklisted", 400)
		return
	}

	msgID, err := h.sched.Poster.PostStation(st, h.cfg.PostFormat, "manual")
	if err != nil {
		jsonError(w, "post failed: "+err.Error(), 500)
		return
	}
	h.store.LogPost(st.ID, "manual", msgID, h.sched.Poster.StationMessage(st, h.cfg.PostFormat, "manual"))
	h.hub.Broadcast("manual_post", map[string]interface{}{
		"name":       st.Name,
		"message_id": msgID,
	})
	jsonOK(w, map[string]interface{}{"message_id": msgID})
}

func (h *Handler) getHistory(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, parseErr := strconv.Atoi(raw); parseErr == nil && n > 0 && n < limit {
			limit = n
		}
	}
	logs, err := h.store.GetPostHistory(limit)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	jsonOK(w, logs)
}

func (h *Handler) getEventLog(w http.ResponseWriter, r *http.Request) {
	logs, err := h.store.GetEventHistory(200)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	jsonOK(w, logs)
}

func (h *Handler) clearEventLog(w http.ResponseWriter, r *http.Request) {
	if err := h.store.ClearEventHistory(); err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	jsonOK(w, map[string]string{"status": "cleared"})
}

func (h *Handler) postEventLog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Message) == "" {
		jsonError(w, "message required", 400)
		return
	}
	level := strings.TrimSpace(body.Level)
	if level != "info" && level != "ok" && level != "warn" && level != "err" {
		level = "info"
	}
	if err := h.store.LogEvent(level, strings.TrimSpace(body.Message)); err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	jsonOK(w, map[string]string{"status": "logged"})
}

func (h *Handler) triggerScrape(w http.ResponseWriter, r *http.Request) {
	h.sched.RunNow()
	h.hub.Broadcast("scrape_triggered", map[string]string{"status": "started"})
	jsonOK(w, map[string]string{"status": "scrape started"})
}

func (h *Handler) triggerContent(w http.ResponseWriter, r *http.Request) {
	h.sched.RunContentNow()
	h.hub.Broadcast("content_triggered", map[string]string{"status": "started"})
	jsonOK(w, map[string]string{"status": "content fetch started"})
}

func (h *Handler) getConfig(w http.ResponseWriter, r *http.Request) {
	safe := map[string]interface{}{
		"scrape_cron":   h.cfg.ScrapeCron,
		"content_cron":  h.cfg.ContentCron,
		"content_mode":  h.cfg.ContentMode,
		"content_limit": h.cfg.ContentLimit,
		"listen_addr":   h.cfg.ListenAddr,
		"channel_ids":   h.cfg.ChannelIDs,
		"channel_map":   h.cfg.ChannelMap,
		"post_format":   h.cfg.PostFormat,
		"video_format":  h.cfg.VideoFormat,
		"bot_token":     maskToken(h.cfg.BotToken),
	}
	jsonOK(w, safe)
}

func (h *Handler) updateConfig(w http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", 400)
		return
	}
	if v, ok := body["scrape_cron"]; ok {
		h.cfg.ScrapeCron = v.(string)
	}
	if v, ok := body["content_cron"]; ok {
		h.cfg.ContentCron = v.(string)
	}
	if v, ok := body["content_mode"]; ok {
		h.cfg.ContentMode = v.(string)
	}
	if v, ok := body["content_limit"]; ok {
		switch val := v.(type) {
		case float64:
			h.cfg.ContentLimit = int(val)
		}
	}
	if v, ok := body["post_format"]; ok {
		h.cfg.PostFormat = v.(string)
	}
	if v, ok := body["video_format"]; ok {
		h.cfg.VideoFormat = v.(string)
	}
	if v, ok := body["bot_token"]; ok {
		s := v.(string)
		// The UI receives a masked token; never persist that placeholder.
		if s != "" && !strings.Contains(s, "****") && s != "***" {
			h.cfg.BotToken = s
		}
	}
	if v, ok := body["channel_id"]; ok {
		switch val := v.(type) {
		case float64:
			h.cfg.ChannelID = int64(val)
		case int64:
			h.cfg.ChannelID = val
		}
	}
	if v, ok := body["channel_ids"]; ok {
		if arr, ok := v.([]interface{}); ok {
			ids := make([]int64, 0, len(arr))
			for _, item := range arr {
				switch val := item.(type) {
				case float64:
					ids = append(ids, int64(val))
				}
			}
			h.cfg.ChannelIDs = ids
		}
	}
	if v, ok := body["channel_map"]; ok {
		if raw, ok := v.(map[string]interface{}); ok {
			m := make(map[string][]int64, len(raw))
			for category, values := range raw {
				arr, ok := values.([]interface{})
				if !ok {
					continue
				}
				ids := make([]int64, 0, len(arr))
				for _, item := range arr {
					if n, ok := item.(float64); ok {
						ids = append(ids, int64(n))
					}
				}
				m[category] = ids
			}
			h.cfg.ChannelMap = m
		}
	}
	h.cfg.Save("config.json")
	h.hub.Broadcast("config_updated", map[string]string{"status": "ok"})
	jsonOK(w, map[string]string{"status": "ok"})
}

func (h *Handler) getStatus(w http.ResponseWriter, r *http.Request) {
	status := h.sched.Status()
	jsonOK(w, status)
}

func jsonOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "data": data})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": msg})
}

func maskToken(t string) string {
	if len(t) <= 8 {
		return "***"
	}
	return t[:4] + "****" + t[len(t)-4:]
}
