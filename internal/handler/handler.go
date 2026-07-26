package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/madtoby2/zyzu/internal/config"
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
		r.Get("/ws", h.hub.HandleWS)
	})
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
	if err != nil {
		jsonError(w, "probe failed: "+err.Error(), 502)
		return
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
	jsonOK(w, map[string]string{"availability": availability, "response_time": fmt.Sprintf("%dms", time.Since(start).Milliseconds())})
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
	h.store.LogPost(st.ID, "manual", msgID, st.Name)
	h.hub.Broadcast("manual_post", map[string]interface{}{
		"name":       st.Name,
		"message_id": msgID,
	})
	jsonOK(w, map[string]interface{}{"message_id": msgID})
}

func (h *Handler) getHistory(w http.ResponseWriter, r *http.Request) {
	logs, err := h.store.GetPostHistory(100)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	jsonOK(w, logs)
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
	if v, ok := body["bot_token"]; ok {
		s := v.(string)
		if s != "" && !strings.Contains(s, "****") {
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
