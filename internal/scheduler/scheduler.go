package scheduler

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/madtoby2/zyzu/internal/config"
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

	mu        sync.Mutex
	running   bool
	lastRun   time.Time
	lastError string
	NewCount  int
	UpdCount  int
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
	_, err := s.cron.AddFunc(s.Cfg.ScrapeCron, s.run)
	if err != nil {
		return fmt.Errorf("add cron: %w", err)
	}
	s.cron.Start()
	log.Printf("[scheduler] started with cron: %s", s.Cfg.ScrapeCron)
	return nil
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
}

func (s *Scheduler) RunNow() {
	go s.run()
}

func (s *Scheduler) Status() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]interface{}{
		"running":    s.running,
		"last_run":   s.lastRun,
		"last_error": s.lastError,
		"new_count":  s.NewCount,
		"upd_count":  s.UpdCount,
		"cron":       s.Cfg.ScrapeCron,
	}
}

func (s *Scheduler) run() {
	s.mu.Lock()
	s.running = true
	s.NewCount = 0
	s.UpdCount = 0
	s.lastError = ""
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.running = false
		s.lastRun = time.Now()
		s.mu.Unlock()
	}()

	log.Println("[scheduler] starting scrape cycle...")

	stations, err := s.Scraper.ScrapeAll()
	if err != nil {
		s.mu.Lock()
		s.lastError = err.Error()
		s.mu.Unlock()
		log.Printf("[scheduler] scrape error: %v", err)
		return
	}

	log.Printf("[scheduler] scraped %d stations, processing...", len(stations))

	for i := range stations {
		st := &stations[i]

		isNew, err := s.Store.UpsertStation(st)
		if err != nil {
			log.Printf("[scheduler] upsert %s error: %v", st.Slug, err)
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

		log.Printf("[scheduler] posted: %s (%s) msgID=%d", st.Name, action, msgID)
		time.Sleep(2 * time.Second)
	}

	log.Printf("[scheduler] cycle done: %d new, %d updated", s.NewCount, s.UpdCount)
}
