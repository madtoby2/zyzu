package scheduler

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/madtoby2/zyzu/internal/config"
	"github.com/madtoby2/zyzu/internal/content"
	"github.com/madtoby2/zyzu/internal/poster"
	"github.com/madtoby2/zyzu/internal/scraper"
	"github.com/madtoby2/zyzu/internal/store"
	"github.com/madtoby2/zyzu/internal/video"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron    *cron.Cron
	Store   *store.Store
	Scraper *scraper.Scraper
	Poster  *poster.Poster
	Cfg     *config.Config
	Agg     *content.Aggregator
	Video   *video.Downloader

	mu           sync.Mutex
	running      bool
	lastRun      time.Time
	lastError    string
	NewCount     int
	UpdCount     int
	ContentCount int
}

func New(st *store.Store, scr *scraper.Scraper, p *poster.Poster, cfg *config.Config) *Scheduler {
	workDir := "videos"
	if d := os.Getenv("ZYZU_VIDEO_DIR"); d != "" {
		workDir = d
	}
	return &Scheduler{
		cron:    cron.New(cron.WithSeconds()),
		Store:   st,
		Scraper: scr,
		Poster:  p,
		Cfg:     cfg,
		Video:   video.New(workDir),
	}
}

func (s *Scheduler) Start() error {
	_, err := s.cron.AddFunc(s.Cfg.ScrapeCron, s.runScrape)
	if err != nil {
		return fmt.Errorf("add scrape cron: %w", err)
	}
	if s.Cfg.ContentCron != "" {
		_, err := s.cron.AddFunc(s.Cfg.ContentCron, s.runContent)
		if err != nil {
			return fmt.Errorf("add content cron: %w", err)
		}
	}
	s.cron.Start()
	log.Printf("[scheduler] scrape=%s content=%s mode=%s", s.Cfg.ScrapeCron, s.Cfg.ContentCron, s.Cfg.ContentMode)
	return nil
}

func (s *Scheduler) Stop()       { s.cron.Stop() }
func (s *Scheduler) RunNow()      { go s.runScrape() }
func (s *Scheduler) RunContentNow() { go s.runContent() }

func (s *Scheduler) Status() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]interface{}{
		"running":       s.running,
		"last_run":      s.lastRun,
		"last_error":    s.lastError,
		"new_count":     s.NewCount,
		"upd_count":     s.UpdCount,
		"content_count": s.ContentCount,
		"cron_scrape":   s.Cfg.ScrapeCron,
		"cron_content":  s.Cfg.ContentCron,
		"content_mode":  s.Cfg.ContentMode,
	}
}

func (s *Scheduler) runScrape() {
	s.mu.Lock()
	s.running, s.NewCount, s.UpdCount, s.lastError = true, 0, 0, ""
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.lastRun = time.Now()
		s.mu.Unlock()
	}()

	stations, err := s.Scraper.ScrapeAll()
	if err != nil {
		s.mu.Lock()
		s.lastError = err.Error()
		s.mu.Unlock()
		return
	}

	for i := range stations {
		st := &stations[i]
		isNew, _ := s.Store.UpsertStation(st)
		fullSt, err := s.Store.GetStationBySlug(st.Slug)
		if err != nil || fullSt.Blacklisted {
			continue
		}
		action := ""
		if isNew {
			action = "new"
		} else {
			posted, _ := s.Store.HasBeenPosted(st.Slug, 24*time.Hour)
			if posted {
				continue
			}
			action = "update"
		}
		msgID, err := s.Poster.PostStation(fullSt, s.Cfg.PostFormat, action)
		if err != nil {
			continue
		}
		s.Store.LogPost(fullSt.ID, action, msgID, st.Name)
		s.mu.Lock()
		if isNew {
			s.NewCount++
		} else {
			s.UpdCount++
		}
		s.mu.Unlock()
		time.Sleep(2 * time.Second)
	}
}

func (s *Scheduler) runContent() {
	mode := s.Cfg.ContentMode
	if mode == "" {
		mode = "split"
	}

	sources, err := content.GetActiveSources(s.Store, 5)
	if err != nil {
		log.Printf("[scheduler] get sources: %v", err)
		return
	}
	if len(sources) == 0 {
		return
	}

	s.Agg = content.New(sources)
	items, err := s.Agg.FetchLatest()
	if err != nil || len(items) == 0 {
		return
	}

	limit := s.Cfg.ContentLimit
	if limit <= 0 {
		limit = 10
	}
	if len(items) > limit {
		items = items[:limit]
	}

	var posted int

	switch mode {
	case "digest":
		title := fmt.Sprintf("📺 今日更新精选 · %s", time.Now().Format("01/02 15:04"))
		_, err = s.Poster.PostContentDigest(items, title)
		if err == nil {
			posted = len(items)
		}

	case "video":
		posted = s.runVideoPipeline(items)

	default: // "split" or "photo"
		posted = s.runPhotoPipeline(items)
	}

	if err != nil {
		log.Printf("[scheduler] content error: %v", err)
	}

	// Cleanup old videos, keep last 30
	s.Video.Cleanup(30)

	s.mu.Lock()
	s.ContentCount = posted
	s.mu.Unlock()
	log.Printf("[scheduler] content: %d posted (mode=%s)", posted, mode)
}

func (s *Scheduler) runVideoPipeline(items []content.ContentItem) int {
	posted := 0
	for _, item := range items {
		if len(item.Episodes) == 0 {
			continue
		}

		// Get the first episode m3u8 URL
		parts := strings.SplitN(item.Episodes[0], "$", 2)
		if len(parts) != 2 {
			continue
		}
		m3u8URL := parts[1]

		// Download + convert
		filePath, err := s.Video.Download(m3u8URL, item.Title)
		if err != nil {
			log.Printf("[video] download %s: %v", item.Title, err)
			continue
		}

		// Upload to TG
		caption := fmt.Sprintf("<b>%s</b>", escapeHTML(item.Title))
		if item.TypeName != "" {
			caption += fmt.Sprintf(" | %s", item.TypeName)
		}
		caption += fmt.Sprintf("\n📡 %s", item.Source)

		_, err = s.Poster.PostVideo(filePath, caption)
		if err != nil {
			log.Printf("[video] upload %s: %v", item.Title, err)
			continue
		}

		posted++
		log.Printf("[video] posted: %s (%.0fMB)", item.Title, float64(fileSize(filePath))/1024/1024)
		time.Sleep(3 * time.Second) // TG rate limit for large uploads
	}
	return posted
}

func (s *Scheduler) runPhotoPipeline(items []content.ContentItem) int {
	posted := 0
	for _, item := range items {
		caption := fmt.Sprintf("<b>%s</b>", escapeHTML(item.Title))
		if item.TypeName != "" {
			caption += fmt.Sprintf(" | %s", item.TypeName)
		}
		caption += fmt.Sprintf("\n📡 %s\n", item.Source)
		for _, ep := range item.Episodes {
			parts := strings.SplitN(ep, "$", 2)
			if len(parts) == 2 {
				caption += fmt.Sprintf("🎬 <a href=\"%s\">%s</a>\n", parts[1], parts[0])
			}
		}

		if item.CoverURL != "" {
			_, err := s.Poster.PostPhoto(item.CoverURL, caption)
			if err == nil {
				posted++
				time.Sleep(1500 * time.Millisecond)
				continue
			}
		}
		// Fallback to text
		s.Poster.PostHTML(caption)
		posted++
		time.Sleep(time.Second)
	}
	return posted
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("<", "&lt;", ">", "&gt;", "&", "&amp;")
	return r.Replace(s)
}
