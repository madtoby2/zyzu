package scheduler

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/madtoby2/zyzu/internal/config"
	"github.com/madtoby2/zyzu/internal/content"
	"github.com/madtoby2/zyzu/internal/poster"
	"github.com/madtoby2/zyzu/internal/scraper"
	"github.com/madtoby2/zyzu/internal/store"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron    *cron.Cron
	Store   *store.Store
	Scraper *scraper.Scraper
	Poster  *poster.Poster
	Cfg     *config.Config
	Agg     *content.Aggregator

	mu        sync.Mutex
	running   bool
	lastRun   time.Time
	lastError string
	NewCount  int
	UpdCount  int
	ContentCount int
}

func New(st *store.Store, scr *scraper.Scraper, p *poster.Poster, cfg *config.Config) *Scheduler {
	return &Scheduler{
		cron:    cron.New(cron.WithSeconds()),
		Store:   st,
		Scraper: scr,
		Poster:  p,
		Cfg:     cfg,
	}
}

func (s *Scheduler) Start() error {
	// Station monitoring job
	_, err := s.cron.AddFunc(s.Cfg.ScrapeCron, s.runScrape)
	if err != nil {
		return fmt.Errorf("add scrape cron: %w", err)
	}

	// Content aggregation job
	if s.Cfg.ContentCron != "" {
		_, err := s.cron.AddFunc(s.Cfg.ContentCron, s.runContent)
		if err != nil {
			return fmt.Errorf("add content cron: %w", err)
		}
	}

	s.cron.Start()
	log.Printf("[scheduler] scrape=%s content=%s", s.Cfg.ScrapeCron, s.Cfg.ContentCron)
	return nil
}

func (s *Scheduler) Stop() { s.cron.Stop() }

func (s *Scheduler) RunNow()          { go s.runScrape() }
func (s *Scheduler) RunContentNow()   { go s.runContent() }

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
	}
}

func (s *Scheduler) runScrape() {
	s.mu.Lock()
	s.running = true
	s.NewCount, s.UpdCount = 0, 0
	s.lastError = ""
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.running = false
		s.lastRun = time.Now()
		s.mu.Unlock()
	}()

	log.Println("[scheduler] scraping stations...")
	stations, err := s.Scraper.ScrapeAll()
	if err != nil {
		s.mu.Lock()
		s.lastError = err.Error()
		s.mu.Unlock()
		log.Printf("[scheduler] scrape error: %v", err)
		return
	}

	for i := range stations {
		st := &stations[i]
		isNew, err := s.Store.UpsertStation(st)
		if err != nil {
			continue
		}
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
			log.Printf("[scheduler] post %s error: %v", st.Name, err)
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
	log.Printf("[scheduler] scrape done: %d new, %d updated", s.NewCount, s.UpdCount)
}

func (s *Scheduler) runContent() {
	log.Println("[scheduler] fetching content...")

	sources, err := content.GetActiveSources(s.Store, 5)
	if err != nil {
		log.Printf("[scheduler] get sources error: %v", err)
		return
	}
	if len(sources) == 0 {
		log.Println("[scheduler] no active sources")
		return
	}

	s.Agg = content.New(sources)
	items, err := s.Agg.FetchLatest()
	if err != nil {
		log.Printf("[scheduler] content fetch error: %v", err)
		return
	}
	if len(items) == 0 {
		log.Println("[scheduler] no new content")
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
	mode := s.Cfg.ContentMode
	if mode == "" {
		mode = "split"
	}

	if mode == "digest" {
		title := fmt.Sprintf("📺 今日更新精选 · %s", time.Now().Format("01/02 15:04"))
		_, err = s.Poster.PostContentDigest(items, title)
		if err == nil {
			posted = len(items)
		}
	} else {
		// split mode: individual photo posts
		posted, err = s.Poster.PostContentSplit(items)
	}

	if err != nil {
		log.Printf("[scheduler] content post error: %v", err)
		return
	}

	s.mu.Lock()
	s.ContentCount = posted
	s.mu.Unlock()

	log.Printf("[scheduler] content posted: %d items (mode=%s)", posted, mode)
}
