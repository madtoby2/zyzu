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
	contentRun   bool
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

func (s *Scheduler) Stop()          { s.cron.Stop() }
func (s *Scheduler) RunNow()        { go s.runScrape() }
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
		// Scraping updates the station catalog only. Station announcements are
		// intentionally not sent to content channels; use the manual station
		// push action when an announcement is explicitly needed.
		s.mu.Lock()
		if isNew {
			s.NewCount++
		} else {
			s.UpdCount++
		}
		s.mu.Unlock()
	}
}

func (s *Scheduler) runContent() {
	s.mu.Lock()
	if s.contentRun {
		s.mu.Unlock()
		log.Printf("[scheduler] content: skipped because a run is already in progress")
		return
	}
	s.contentRun = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.contentRun = false
		s.mu.Unlock()
	}()

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
	// Keep this state in SQLite so a later cron run (or a process restart)
	// cannot upload the same source item again.
	filtered := items[:0]
	for _, item := range items {
		key := content.DedupKey(item)
		posted, checkErr := s.Store.HasContentPosted(key)
		if checkErr != nil {
			log.Printf("[content] dedup check %s: %v", item.Title, checkErr)
			continue
		}
		if posted {
			continue
		}
		cooldown, cooldownErr := s.Store.ContentInFailureCooldown(key, 6*time.Hour)
		if cooldownErr != nil || cooldown {
			if cooldown {
				log.Printf("[content] skip %s: download failure cooldown", item.Title)
			}
			continue
		}
		item.Key = key
		filtered = append(filtered, item)
	}
	items = filtered
	// Apply the limit after deduplication. Otherwise old/already-posted items
	// at the head of the feed can consume the whole batch and hide new items.
	if len(items) > limit {
		items = items[:limit]
	}
	if len(items) == 0 {
		log.Printf("[scheduler] content: no new items")
		return
	}

	var posted int

	switch mode {
	case "digest":
		title := fmt.Sprintf("📺 今日更新精选 · %s", time.Now().Format("01/02 15:04"))
		_, err = s.Poster.PostContentDigest(items, title, "default")
		if err == nil {
			posted = len(items)
			for _, item := range items {
				_ = s.Store.LogContentPost(item.Key)
			}
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

		var filePath string
		var err error
		for _, episode := range item.Episodes {
			parts := strings.SplitN(episode, "$", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
				continue
			}
			filePath, err = s.Video.Download(parts[1], item.Title)
			if err == nil {
				break
			}
			log.Printf("[video] download %s (%s): %v", item.Title, parts[0], err)
		}
		if err != nil || filePath == "" {
			_ = s.Store.LogContentFailure(content.DedupKey(item))
			continue
		}

		cat := item.Category
		if cat == "" {
			cat = "default"
		}
		caption := formatVideoCaption(s.Cfg.VideoFormat, item)
		if item.TypeName != "" {
			caption += fmt.Sprintf(" | %s", item.TypeName)
		}
		caption += fmt.Sprintf("\n📡 %s", item.Source)

		// The configured template is authoritative; discard legacy appended source text.
		caption = formatVideoCaption(s.Cfg.VideoFormat, item)
		_, err = s.Poster.PostVideo(filePath, caption, cat, item.CoverURL)
		if err != nil {
			log.Printf("[video] upload %s: %v", item.Title, err)
			continue
		}

		posted++
		if err := s.Store.LogContentPost(content.DedupKey(item)); err != nil {
			log.Printf("[content] record %s: %v", item.Title, err)
		}
		log.Printf("[video] posted: %s (%.0fMB)", item.Title, float64(fileSize(filePath))/1024/1024)
		time.Sleep(3 * time.Second) // TG rate limit for large uploads
	}
	return posted
}

func formatVideoCaption(format string, item content.ContentItem) string {
	if strings.TrimSpace(format) == "" {
		format = "[资源码] {code}\n{title}\n分类：{category}\n来源：{source}"
	}
	code := ""
	if item.VodID > 0 {
		code = fmt.Sprintf("%d", item.VodID)
	}
	return strings.NewReplacer("{code}", code, "{id}", code, "{title}", escapeHTML(item.Title), "{name}", escapeHTML(item.Title), "{category}", escapeHTML(item.Category), "{type}", escapeHTML(item.TypeName), "{remarks}", escapeHTML(item.Remarks), "{source}", escapeHTML(item.Source), "{source_url}", escapeHTML(item.SourceURL), "{intro}", escapeHTML(item.Intro), "{cover}", escapeHTML(item.CoverURL), "{updated_at}", escapeHTML(item.VodTime)).Replace(format)
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
			_, err := s.Poster.PostPhoto(item.CoverURL, caption, item.Category)
			if err == nil {
				posted++
				if logErr := s.Store.LogContentPost(content.DedupKey(item)); logErr != nil {
					log.Printf("[content] record %s: %v", item.Title, logErr)
				}
				time.Sleep(1500 * time.Millisecond)
				continue
			}
		}
		// Fallback to text
		if _, err := s.Poster.PostHTML(caption); err == nil {
			posted++
			if logErr := s.Store.LogContentPost(content.DedupKey(item)); logErr != nil {
				log.Printf("[content] record %s: %v", item.Title, logErr)
			}
		}
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
