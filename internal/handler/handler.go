package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
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
		r.Get("/api/scheduled-messages", h.listScheduledMessages)
		r.Post("/api/scheduled-messages", h.createScheduledMessage)
		r.Put("/api/scheduled-messages/{id}", h.updateScheduledMessage)
		r.Delete("/api/scheduled-messages/{id}", h.deleteScheduledMessage)
		r.Post("/api/scheduled-messages/{id}/send", h.sendScheduledMessageNow)
		r.Get("/ws", h.hub.HandleWS)
	})
}

func (h *Handler) listScheduledMessages(w http.ResponseWriter, r *http.Request) {
	messages, err := h.store.ListScheduledMessages()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, messages)
}

func (h *Handler) createScheduledMessage(w http.ResponseWriter, r *http.Request) {
	message, err := h.decodeScheduledMessage(r)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.CreateScheduledMessage(message); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.hub.Broadcast("scheduled_message_changed", map[string]interface{}{"action": "created", "id": message.ID})
	jsonOK(w, message)
}

func (h *Handler) updateScheduledMessage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid scheduled message id", http.StatusBadRequest)
		return
	}
	message, err := h.decodeScheduledMessage(r)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	message.ID = id
	if err := h.store.UpdateScheduledMessage(message); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	h.hub.Broadcast("scheduled_message_changed", map[string]interface{}{"action": "updated", "id": id})
	jsonOK(w, message)
}

func (h *Handler) deleteScheduledMessage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid scheduled message id", http.StatusBadRequest)
		return
	}
	if err := h.store.DeleteScheduledMessage(id); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	h.hub.Broadcast("scheduled_message_changed", map[string]interface{}{"action": "deleted", "id": id})
	jsonOK(w, map[string]string{"status": "deleted"})
}

func (h *Handler) sendScheduledMessageNow(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid scheduled message id", http.StatusBadRequest)
		return
	}
	if _, err := h.store.GetScheduledMessage(id); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	go func() {
		sendErr := h.sched.SendScheduledMessageNow(id)
		status := "sent"
		if sendErr != nil {
			status = "failed"
		}
		h.hub.Broadcast("scheduled_message_sent", map[string]interface{}{"id": id, "status": status})
	}()
	jsonOK(w, map[string]string{"status": "sending"})
}

func (h *Handler) decodeScheduledMessage(r *http.Request) (*store.ScheduledMessage, error) {
	var message store.ScheduledMessage
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&message); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}
	message.ChannelCategory = strings.TrimSpace(message.ChannelCategory)
	message.Content = strings.TrimSpace(message.Content)
	message.ScheduleType = strings.TrimSpace(strings.ToLower(message.ScheduleType))
	message.DailyTime = strings.TrimSpace(message.DailyTime)
	if message.ChannelID == 0 || message.ChannelCategory == "" {
		return nil, fmt.Errorf("请选择频道")
	}
	configured := false
	for _, channelID := range h.cfg.ChannelMap[message.ChannelCategory] {
		if channelID == message.ChannelID {
			configured = true
			break
		}
	}
	if !configured {
		return nil, fmt.Errorf("频道不存在或已被删除")
	}
	if message.Content == "" {
		return nil, fmt.Errorf("发言文案不能为空")
	}
	if len([]rune(message.Content)) > 4096 {
		return nil, fmt.Errorf("发言文案不能超过 4096 个字符")
	}
	switch message.ScheduleType {
	case "interval":
		if message.IntervalMinutes < 5 || message.IntervalMinutes > 10080 {
			return nil, fmt.Errorf("发送间隔必须在 5 分钟到 7 天之间")
		}
		message.DailyTime = ""
	case "daily":
		if _, err := time.Parse("15:04", message.DailyTime); err != nil {
			return nil, fmt.Errorf("每日发送时间格式不正确")
		}
		message.IntervalMinutes = 0
	default:
		return nil, fmt.Errorf("请选择发送频率")
	}
	return &message, nil
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
	if h.cfg.ChannelIntervals == nil {
		h.cfg.ChannelIntervals = map[string]int{}
	}
	if h.cfg.ChannelIntervals[cat] <= 0 {
		h.cfg.ChannelIntervals[cat] = h.cfg.ChannelIntervalMinutes(cat)
	}
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
		delete(h.cfg.ChannelIntervals, cat)
	} else {
		h.cfg.ChannelMap[cat] = out
	}
	if err := h.cfg.Save("config.json"); err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	if err := h.store.DisableScheduledMessagesForChannel(body.ID); err != nil {
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
	// Retired endpoints may fail with DNS/TLS/HTTP errors while an equivalent
	// public API remains available on the apex host or over HTTP. Try only
	// explicit URL fallbacks; never disable certificate verification globally.
	if err != nil || resp.StatusCode >= 400 {
		if resp != nil {
			resp.Body.Close()
		}
		var lastErr error = err
		var fallback string
		for _, candidate := range probeFallbacks(st.APIURL) {
			req2, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, candidate, nil)
			if reqErr != nil {
				lastErr = reqErr
				continue
			}
			resp, reqErr = client.Do(req2)
			if reqErr != nil {
				lastErr = reqErr
				continue
			}
			if resp.StatusCode >= 400 {
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
				resp.Body.Close()
				continue
			}
			fallback = candidate
			break
		}
		if fallback == "" {
			if lastErr != nil {
				jsonError(w, "probe failed: "+lastErr.Error(), 502)
			} else {
				jsonError(w, "probe failed", 502)
			}
			return
		}
		if updateErr := h.store.UpdateAPIURL(slug, fallback); updateErr != nil {
			resp.Body.Close()
			jsonError(w, "probe succeeded but endpoint save failed: "+updateErr.Error(), 500)
			return
		}
		migrated = true
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	resp.Body.Close()
	availability := "0%"
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		availability = "100%"
	}
	var api struct {
		Total int `json:"total"`
	}
	if json.Unmarshal(body, &api) == nil && api.Total > 0 {
		_ = h.store.UpdateResourceCount(slug, strconv.Itoa(api.Total))
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

func probeFallbacks(raw string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(candidate string) {
		if candidate != "" && candidate != raw && !seen[candidate] {
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	if fallback := apexAPIFallback(raw); fallback != "" && fallback != raw {
		add(fallback)
		if u, err := url.Parse(fallback); err == nil && u.Scheme == "https" {
			u.Scheme = "http"
			add(u.String())
		}
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme == "https" {
		u.Scheme = "http"
		add(u.String())
	}
	return out
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
	h.enrichStationHealth(stations)
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
		"scrape_cron":       h.cfg.ScrapeCron,
		"content_cron":      h.cfg.ContentCron,
		"content_mode":      h.cfg.ContentMode,
		"content_limit":     h.cfg.ContentLimit,
		"listen_addr":       h.cfg.ListenAddr,
		"channel_ids":       h.cfg.ChannelIDs,
		"channel_map":       h.cfg.ChannelMap,
		"channel_intervals": h.cfg.ChannelIntervals,
		"post_format":       h.cfg.PostFormat,
		"video_format":      h.cfg.VideoFormat,
		"video_formats":     h.cfg.VideoFormats,
		"separate_cover":    h.cfg.SeparateCover,
		"bot_token":         maskToken(h.cfg.BotToken),
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
	if v, ok := body["video_formats"]; ok {
		if raw, ok := v.(map[string]interface{}); ok {
			formats := make(map[string]string, len(raw))
			for category, value := range raw {
				if format, ok := value.(string); ok {
					formats[category] = format
				}
			}
			h.cfg.VideoFormats = formats
		}
	}
	if v, ok := body["separate_cover"]; ok {
		if enabled, ok := v.(bool); ok {
			h.cfg.SeparateCover = enabled
		}
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
	if v, ok := body["channel_intervals"]; ok {
		if raw, ok := v.(map[string]interface{}); ok {
			intervals := make(map[string]int, len(raw))
			for category, value := range raw {
				if minutes, ok := value.(float64); ok && minutes > 0 {
					intervals[category] = int(minutes)
				}
			}
			h.cfg.ChannelIntervals = intervals
		}
	}
	h.cfg.Save("config.json")
	h.hub.Broadcast("config_updated", map[string]string{"status": "ok"})
	jsonOK(w, map[string]string{"status": "ok"})
}

func (h *Handler) getStatus(w http.ResponseWriter, r *http.Request) {
	status := h.sched.Status()
	status["disk"] = diskStatus(".")
	jsonOK(w, status)
}

func (h *Handler) enrichStationHealth(stations []store.Station) {
	failures, err := h.store.GetSourceFailures()
	if err != nil {
		return
	}
	now := time.Now()
	for i := range stations {
		st := &stations[i]
		score := 100
		if st.Blacklisted {
			score = 0
		}
		if st.Availability == "0%" {
			score -= 45
		} else if availability := parsePercent(st.Availability); availability > 0 && availability < 80 {
			score -= 20
		}
		if count := parseLeadingInt(st.ResourceCount); count == 0 {
			score -= 15
		}
		if response := parseResponseMillis(st.ResponseTime); response > 3000 {
			score -= 15
		}
		key := strings.TrimRight(strings.ToLower(strings.TrimSpace(st.APIURL)), "/")
		if failure, ok := failures[key]; ok {
			st.DownloadFailCount = failure.FailCount
			st.LastDownloadError = failure.LastError
			st.LastDownloadFailedAt = &failure.FailedAt
			st.LastDownloadFailureID = failure.SourceKey
			if now.Sub(failure.FailedAt) < 6*time.Hour {
				st.DownloadCooldown = true
				score -= 35
			} else {
				score -= 15
			}
		}
		if score < 0 {
			score = 0
		}
		st.HealthScore = score
		switch {
		case score >= 85:
			st.HealthLabel = "优秀"
		case score >= 65:
			st.HealthLabel = "可用"
		case score >= 40:
			st.HealthLabel = "观察"
		default:
			st.HealthLabel = "异常"
		}
	}
}

func parsePercent(value string) int {
	value = strings.TrimSpace(strings.TrimSuffix(value, "%"))
	n, _ := strconv.Atoi(value)
	return n
}

func parseLeadingInt(value string) int {
	value = strings.TrimSpace(value)
	var digits strings.Builder
	for _, r := range value {
		if r < '0' || r > '9' {
			break
		}
		digits.WriteRune(r)
	}
	n, _ := strconv.Atoi(digits.String())
	return n
}

func parseResponseMillis(value string) int {
	value = strings.TrimSpace(strings.TrimSuffix(value, "ms"))
	n, _ := strconv.Atoi(value)
	return n
}

func diskStatus(path string) map[string]interface{} {
	out, err := exec.Command("df", "-Pk", path).Output()
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	lines := strings.Fields(string(out))
	if len(lines) < 12 {
		return map[string]interface{}{"ok": false, "error": "unexpected df output"}
	}
	totalKB, _ := strconv.ParseInt(lines[7], 10, 64)
	usedKB, _ := strconv.ParseInt(lines[8], 10, 64)
	availKB, _ := strconv.ParseInt(lines[9], 10, 64)
	usedPct := strings.TrimSuffix(lines[10], "%")
	usedPercent, _ := strconv.Atoi(usedPct)
	return map[string]interface{}{
		"ok":           true,
		"total_gb":     float64(totalKB) / 1024 / 1024,
		"used_gb":      float64(usedKB) / 1024 / 1024,
		"available_gb": float64(availKB) / 1024 / 1024,
		"used_percent": usedPercent,
		"low_space":    availKB < 8*1024*1024,
	}
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
