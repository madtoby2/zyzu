package scheduler

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"log"
	urlpkg "net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/madtoby2/zyzu/internal/captionai"
	"github.com/madtoby2/zyzu/internal/config"
	"github.com/madtoby2/zyzu/internal/content"
	"github.com/madtoby2/zyzu/internal/poster"
	"github.com/madtoby2/zyzu/internal/scraper"
	"github.com/madtoby2/zyzu/internal/store"
	"github.com/madtoby2/zyzu/internal/translator"
	"github.com/madtoby2/zyzu/internal/video"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron      *cron.Cron
	Store     *store.Store
	Scraper   *scraper.Scraper
	Poster    *poster.Poster
	Cfg       *config.Config
	Agg       *content.Aggregator
	Video     *video.Downloader
	Translate *translator.Translator
	CaptionAI *captionai.Client

	mu           sync.Mutex
	scheduledMu  sync.Mutex
	running      bool
	contentRun   bool
	scheduledRun bool
	categoryRuns map[string]bool
	categoryLast map[string]time.Time
	channelJobs  map[string]ChannelJobStatus
	skippedItems map[string]time.Time
	lastRun      time.Time
	lastError    string
	NewCount     int
	UpdCount     int
	ContentCount int
}

type ChannelJobStatus struct {
	State     string    `json:"state"`
	Title     string    `json:"title"`
	Source    string    `json:"source"`
	SizeMB    float64   `json:"size_mb,omitempty"`
	Key       string    `json:"key,omitempty"`
	Episode   string    `json:"episode,omitempty"`
	Index     int       `json:"episode_index,omitempty"`
	Total     int       `json:"episode_total,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ContentPreview struct {
	Key          string    `json:"key"`
	Title        string    `json:"title"`
	Source       string    `json:"source"`
	SourceURL    string    `json:"source_url"`
	Category     string    `json:"category"`
	TypeName     string    `json:"type_name"`
	Class        string    `json:"class"`
	Actor        string    `json:"actor"`
	Year         string    `json:"year"`
	Remarks      string    `json:"remarks"`
	Intro        string    `json:"intro"`
	CoverURL     string    `json:"cover_url"`
	VodTime      string    `json:"vod_time"`
	EpisodeCount int       `json:"episode_count"`
	Posted       bool      `json:"posted"`
	Skipped      bool      `json:"skipped"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type episodeCandidate struct {
	Name         string
	URLs         []string
	FirstIndex   int
	LogicalIndex int
}

func New(st *store.Store, scr *scraper.Scraper, p *poster.Poster, cfg *config.Config) *Scheduler {
	workDir := "videos"
	if d := os.Getenv("ZYZU_VIDEO_DIR"); d != "" {
		workDir = d
	}
	return &Scheduler{
		cron:         cron.New(cron.WithSeconds()),
		Store:        st,
		Scraper:      scr,
		Poster:       p,
		Cfg:          cfg,
		Video:        video.New(workDir),
		Translate:    translator.New(st),
		CaptionAI:    captionai.New(os.Getenv("ZYZU_CAPTION_AI_BASE_URL"), os.Getenv("ZYZU_CAPTION_AI_KEY"), os.Getenv("ZYZU_CAPTION_AI_MODEL")),
		categoryRuns: make(map[string]bool),
		categoryLast: make(map[string]time.Time),
		channelJobs:  make(map[string]ChannelJobStatus),
		skippedItems: make(map[string]time.Time),
	}
}

func (s *Scheduler) Start() error {
	_, err := s.cron.AddFunc(s.Cfg.ScrapeCron, s.runScrape)
	if err != nil {
		return fmt.Errorf("add scrape cron: %w", err)
	}
	if s.Cfg.ContentCron != "" {
		_, err := s.cron.AddFunc(s.Cfg.ContentCron, func() { s.runContent(false) })
		if err != nil {
			return fmt.Errorf("add content cron: %w", err)
		}
	}
	if _, err := s.cron.AddFunc("0 * * * * *", s.runScheduledMessages); err != nil {
		return fmt.Errorf("add scheduled messages cron: %w", err)
	}
	if _, err := s.cron.AddFunc("0 */5 * * * *", func() { s.RefreshSeriesDirectory("电视剧", false) }); err != nil {
		return fmt.Errorf("add directory maintenance cron: %w", err)
	}
	s.cron.Start()
	go s.RefreshSeriesDirectory("电视剧", false)
	log.Printf("[scheduler] scrape=%s content=%s mode=%s", s.Cfg.ScrapeCron, s.Cfg.ContentCron, s.Cfg.ContentMode)
	return nil
}

func (s *Scheduler) Stop()          { s.cron.Stop() }
func (s *Scheduler) RunNow()        { go s.runScrape() }
func (s *Scheduler) RunContentNow() { go s.runContent(true) }
func (s *Scheduler) RunContentCategoryNow(category string) {
	go s.runContentCategories(true, []string{category})
}

func (s *Scheduler) SkipCurrentChannelItem(category string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.channelJobs[category]
	if !ok || strings.TrimSpace(job.Key) == "" {
		return false
	}
	s.skippedItems[job.Key] = time.Now()
	job.State = "skipped"
	job.UpdatedAt = time.Now()
	s.channelJobs[category] = job
	return true
}

func (s *Scheduler) isSkipped(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	skippedAt, ok := s.skippedItems[key]
	if !ok {
		return false
	}
	if time.Since(skippedAt) > 24*time.Hour {
		delete(s.skippedItems, key)
		return false
	}
	return true
}

func (s *Scheduler) ContentPreview(category string, limit int) ([]ContentPreview, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	allSources, err := content.GetActiveSources(s.Store, 0)
	if err != nil {
		return nil, err
	}
	activeSources := make([]store.Station, 0, len(allSources))
	for _, source := range allSources {
		if !stationCategoryMatches(category, source.Category) {
			continue
		}
		sourceKey := sourceFailureKeyForStation(source)
		cooldown, cooldownErr := s.Store.SourceInFailureCooldown(sourceKey, 6*time.Hour)
		if cooldownErr != nil {
			log.Printf("[preview] source cooldown check %s: %v", source.Name, cooldownErr)
		}
		if cooldown {
			continue
		}
		activeSources = append(activeSources, source)
	}
	if len(activeSources) == 0 {
		return []ContentPreview{}, nil
	}
	agg := content.NewPreview(selectContentSources(activeSources, s.Cfg, 3, 3), category)
	items, err := agg.FetchLatest()
	if err != nil {
		return nil, err
	}
	result := make([]ContentPreview, 0, limit)
	for _, item := range items {
		if !stationCategoryMatches(category, item.Category) {
			continue
		}
		key := content.DedupKey(item)
		posted, err := s.contentPostedForCategory(category, item)
		if err != nil {
			return nil, err
		}
		result = append(result, ContentPreview{
			Key:          key,
			Title:        item.Title,
			Source:       item.Source,
			SourceURL:    item.SourceURL,
			Category:     item.Category,
			TypeName:     item.TypeName,
			Class:        item.Class,
			Actor:        item.Actor,
			Year:         item.Year,
			Remarks:      item.Remarks,
			Intro:        item.Intro,
			CoverURL:     item.CoverURL,
			VodTime:      item.VodTime,
			EpisodeCount: len(item.Episodes),
			Posted:       posted,
			Skipped:      s.isSkipped(key),
			UpdatedAt:    time.Now(),
		})
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (s *Scheduler) contentPostedForCategory(category string, item content.ContentItem) (bool, error) {
	if s.Cfg.ChannelPolicy(category).AllowSeries && shouldPostSeriesEpisodes(category, item) {
		hasPending, err := s.hasPendingEpisode(item)
		return !hasPending, err
	}
	return s.Store.HasContentPosted(content.DedupKey(item))
}

func (s *Scheduler) SendScheduledMessageNow(id int64) error {
	s.scheduledMu.Lock()
	defer s.scheduledMu.Unlock()
	message, err := s.Store.GetScheduledMessage(id)
	if err != nil {
		return err
	}
	messageID, sendErr := s.Poster.PostToChannel(message.Content, message.ChannelID)
	if sendErr != nil {
		_ = s.Store.MarkScheduledMessageResult(message.ID, false, message.NextRunAt, "failed", sendErr.Error())
		_ = s.Store.LogEvent("err", fmt.Sprintf("定时发言“%s”手动发送失败：%v", message.ChannelCategory, sendErr))
		return sendErr
	}
	_ = s.Store.MarkScheduledMessageResult(message.ID, true, message.NextRunAt, "manual_sent", "")
	_ = s.Store.LogEvent("ok", fmt.Sprintf("定时发言已发送到“%s”，消息 #%d", message.ChannelCategory, messageID))
	log.Printf("[scheduled] manual message=%d channel=%s telegram_message=%d", message.ID, message.ChannelCategory, messageID)
	return nil
}

func (s *Scheduler) Status() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	channelRunning := make(map[string]bool, len(s.categoryRuns))
	channelJobs := make(map[string]ChannelJobStatus, len(s.channelJobs))
	anyContentRunning := s.contentRun
	for category, running := range s.categoryRuns {
		channelRunning[category] = running
		anyContentRunning = anyContentRunning || running
	}
	for category, job := range s.channelJobs {
		channelJobs[category] = job
	}
	return map[string]interface{}{
		"running":         s.running || anyContentRunning,
		"scrape_running":  s.running,
		"content_running": anyContentRunning,
		"channel_running": channelRunning,
		"channel_jobs":    channelJobs,
		"last_run":        s.lastRun,
		"last_error":      s.lastError,
		"new_count":       s.NewCount,
		"upd_count":       s.UpdCount,
		"content_count":   s.ContentCount,
		"cron_scrape":     s.Cfg.ScrapeCron,
		"cron_content":    s.Cfg.ContentCron,
		"content_mode":    s.Cfg.ContentMode,
	}
}

func (s *Scheduler) runScheduledMessages() {
	s.mu.Lock()
	if s.scheduledRun {
		s.mu.Unlock()
		return
	}
	s.scheduledRun = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.scheduledRun = false
		s.mu.Unlock()
	}()

	now := time.Now().UTC()
	messages, err := s.Store.GetDueScheduledMessages(now)
	if err != nil {
		log.Printf("[scheduled] list due messages: %v", err)
		return
	}
	for _, message := range messages {
		nextRun := store.NextScheduledRun(message.ScheduleType, message.IntervalMinutes, message.DailyTime, now)
		s.scheduledMu.Lock()
		if err := s.Store.MarkScheduledMessageSending(message.ID, nextRun); err != nil {
			s.scheduledMu.Unlock()
			log.Printf("[scheduled] claim message=%d: %v", message.ID, err)
			continue
		}
		messageID, sendErr := s.Poster.PostToChannel(message.Content, message.ChannelID)
		if sendErr != nil {
			_ = s.Store.MarkScheduledMessageResult(message.ID, false, nextRun, "failed", sendErr.Error())
			s.scheduledMu.Unlock()
			_ = s.Store.LogEvent("err", fmt.Sprintf("定时发言“%s”发送失败：%v", message.ChannelCategory, sendErr))
			log.Printf("[scheduled] message=%d channel=%s: %v", message.ID, message.ChannelCategory, sendErr)
			continue
		}
		_ = s.Store.MarkScheduledMessageResult(message.ID, true, nextRun, "sent", "")
		s.scheduledMu.Unlock()
		_ = s.Store.LogEvent("ok", fmt.Sprintf("定时发言已发送到“%s”，消息 #%d", message.ChannelCategory, messageID))
		log.Printf("[scheduled] message=%d channel=%s telegram_message=%d next=%s", message.ID, message.ChannelCategory, messageID, nextRun.Format(time.RFC3339))
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

func (s *Scheduler) runContent(force bool) {
	s.runContentCategories(force, nil)
}

func (s *Scheduler) runContentCategories(force bool, onlyCategories []string) {
	s.mu.Lock()
	if s.contentRun {
		s.mu.Unlock()
		log.Printf("[scheduler] content scan: skipped because another scan is in progress")
		return
	}
	s.contentRun = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.contentRun = false
		s.mu.Unlock()
	}()

	dueCategories := s.dueContentCategoriesFor(force, onlyCategories)
	if len(dueCategories) == 0 {
		log.Printf("[scheduler] content: no channel is due")
		return
	}

	mode := s.Cfg.ContentMode
	if mode == "" {
		mode = "split"
	}

	allSources, err := content.GetActiveSources(s.Store, 0)
	if err != nil {
		log.Printf("[scheduler] get sources: %v", err)
		return
	}
	activeSources := make([]store.Station, 0, len(allSources))
	for _, source := range allSources {
		sourceKey := sourceFailureKeyForStation(source)
		cooldown, cooldownErr := s.Store.SourceInFailureCooldown(sourceKey, 6*time.Hour)
		if cooldownErr != nil {
			log.Printf("[content] source cooldown check %s: %v", source.Name, cooldownErr)
		}
		if cooldown {
			log.Printf("[content] skip source %s: download failure cooldown", source.Name)
			continue
		}
		activeSources = append(activeSources, source)
	}
	// Keep fast global sources, then add explicitly categorized sources for
	// each configured channel. This makes the UI's station categories affect
	// what the corresponding channel actually receives.
	sources := selectContentSources(activeSources, s.Cfg, 5, 2)
	if len(sources) == 0 {
		return
	}

	s.Agg = content.New(sources)
	items, err := s.Agg.FetchLatest()
	if err != nil || len(items) == 0 {
		return
	}

	// Keep this state in SQLite so a later cron run (or a process restart)
	// cannot upload the same source item again.
	filtered := items[:0]
	for _, item := range items {
		key := content.DedupKey(item)
		if s.isSkipped(key) {
			continue
		}
		if shouldPostSeriesEpisodes("", item) {
			hasPending, checkErr := s.hasPendingEpisode(item)
			if checkErr != nil {
				log.Printf("[content] episode dedup check %s: %v", item.Title, checkErr)
				continue
			}
			if !hasPending {
				continue
			}
		} else {
			posted, checkErr := s.Store.HasContentPosted(key)
			if checkErr != nil {
				log.Printf("[content] dedup check %s: %v", item.Title, checkErr)
				continue
			}
			if posted {
				continue
			}
		}
		if !force {
			cooldown, cooldownErr := s.Store.ContentInFailureCooldown(key, 6*time.Hour)
			if cooldownErr != nil || cooldown {
				if cooldown {
					log.Printf("[content] skip %s: download failure cooldown", item.Title)
				}
				continue
			}
		}
		item.Key = key
		filtered = append(filtered, item)
	}
	// Resume completed-but-not-yet-posted files first after a crash or restart.
	sort.SliceStable(filtered, func(i, j int) bool {
		return s.Video.HasComplete(filtered[i].Title) && !s.Video.HasComplete(filtered[j].Title)
	})

	if len(filtered) == 0 {
		log.Printf("[scheduler] content: no new items")
		return
	}

	claimed := make(map[string]bool)
	dispatched := 0
	for _, category := range dueCategories {
		policy := s.Cfg.ChannelPolicy(category)
		candidates := selectCategoryCandidates(filtered, category, policy.PerRunLimit, claimed)
		if len(candidates) == 0 {
			log.Printf("[scheduler] channel=%s: no new matching item", category)
			continue
		}

		s.mu.Lock()
		if s.categoryRuns[category] {
			s.mu.Unlock()
			continue
		}
		s.categoryRuns[category] = true
		s.categoryLast[category] = time.Now()
		s.channelJobs[category] = ChannelJobStatus{
			State:     "queued",
			Title:     candidates[0].Title,
			Source:    candidates[0].Source,
			Key:       candidates[0].Key,
			UpdatedAt: time.Now(),
		}
		s.mu.Unlock()

		dispatched++
		go s.runContentCategory(mode, category, candidates)
	}
	if dispatched == 0 {
		log.Printf("[scheduler] content: no channel job dispatched")
		return
	}
	log.Printf("[scheduler] content: dispatched %d independent channel job(s)", dispatched)
}

func (s *Scheduler) dueContentCategories(force bool) []string {
	return s.dueContentCategoriesFor(force, nil)
}

func (s *Scheduler) dueContentCategoriesFor(force bool, only []string) []string {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	onlySet := make(map[string]bool)
	for _, category := range only {
		category = strings.TrimSpace(category)
		if category != "" {
			onlySet[category] = true
		}
	}
	categories := make([]string, 0, len(s.Cfg.ChannelMap))
	for category, ids := range s.Cfg.ChannelMap {
		if len(onlySet) > 0 && !onlySet[category] {
			continue
		}
		if len(ids) == 0 || s.categoryRuns[category] {
			continue
		}
		if s.Cfg.ChannelPolicy(category).Paused {
			continue
		}
		interval := time.Duration(s.Cfg.ChannelIntervalMinutes(category)) * time.Minute
		if force || s.categoryLast[category].IsZero() || now.Sub(s.categoryLast[category]) >= interval {
			categories = append(categories, category)
		}
	}
	sort.Strings(categories)
	return categories
}

func selectCategoryCandidates(items []content.ContentItem, category string, limit int, claimed map[string]bool) []content.ContentItem {
	if limit <= 0 {
		return nil
	}
	result := make([]content.ContentItem, 0, limit)
	seenSource := make(map[string]bool)
	add := func(item content.ContentItem) bool {
		if claimed[item.Key] || !stationCategoryMatches(category, item.Category) {
			return false
		}
		claimed[item.Key] = true
		result = append(result, item)
		return len(result) == limit
	}

	// Prefer different资源站 so one dead CDN cannot consume every retry.
	for _, item := range items {
		source := strings.TrimSpace(item.SourceURL)
		if source == "" {
			source = strings.TrimSpace(item.Source)
		}
		if seenSource[source] || claimed[item.Key] || !stationCategoryMatches(category, item.Category) {
			continue
		}
		seenSource[source] = true
		if add(item) {
			return result
		}
	}
	for _, item := range items {
		if add(item) {
			break
		}
	}
	return result
}

func (s *Scheduler) runContentCategory(mode, category string, items []content.ContentItem) {
	posted := 0
	defer func() {
		s.Video.Cleanup(30)
		s.mu.Lock()
		s.categoryRuns[category] = false
		s.ContentCount += posted
		if posted == 0 {
			job := s.channelJobs[category]
			job.State = "failed"
			job.UpdatedAt = time.Now()
			s.channelJobs[category] = job
		}
		s.mu.Unlock()
		log.Printf("[scheduler] channel=%s: %d posted (mode=%s)", category, posted, mode)
	}()

	switch mode {
	case "digest":
		item := items[0]
		title := fmt.Sprintf("📺 %s更新精选 · %s", category, time.Now().Format("01/02 15:04"))
		if _, err := s.Poster.PostContentDigest([]content.ContentItem{item}, title, category); err != nil {
			log.Printf("[scheduler] channel=%s digest: %v", category, err)
			return
		}
		_ = s.Store.LogContentPost(item.Key)
		posted = 1
	case "video":
		limit := s.Cfg.ChannelPolicy(category).PerRunLimit
		for _, item := range items {
			count := s.runVideoCategory(category, []content.ContentItem{item})
			posted += count
			if posted >= limit {
				break
			}
		}
	default:
		for _, item := range items {
			if s.runPhotoPipeline([]content.ContentItem{item}) > 0 {
				posted = 1
				break
			}
		}
	}
}

func (s *Scheduler) setChannelJob(category, state string, item content.ContentItem, sizeBytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channelJobs[category] = ChannelJobStatus{
		State:     state,
		Title:     item.Title,
		Source:    item.Source,
		SizeMB:    float64(sizeBytes) / 1024 / 1024,
		Key:       item.Key,
		Episode:   item.EpisodeName,
		Index:     item.EpisodeIndex,
		Total:     item.EpisodeTotal,
		UpdatedAt: time.Now(),
	}
}

func (s *Scheduler) runVideoPipeline(items []content.ContentItem) int {
	groups := make(map[string][]content.ContentItem)
	var categoryOrder []string
	for _, item := range items {
		category := item.Category
		if category == "" {
			category = "default"
		}
		if _, ok := groups[category]; !ok {
			categoryOrder = append(categoryOrder, category)
		}
		groups[category] = append(groups[category], item)
	}

	// Download categories independently so a slow full-length movie cannot
	// block adult or TV channels. Telethon uploads remain serialized in Poster.
	results := make(chan int, len(categoryOrder))
	var wg sync.WaitGroup
	for _, category := range categoryOrder {
		categoryItems := groups[category]
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.runVideoCategory(category, categoryItems)
		}()
	}
	wg.Wait()
	close(results)

	posted := 0
	for count := range results {
		posted += count
	}
	return posted
}

func (s *Scheduler) runVideoCategory(category string, items []content.ContentItem) int {
	posted := 0
	for _, item := range items {
		if len(item.Episodes) == 0 {
			continue
		}
		sourceKey := sourceFailureKeyForItem(item)
		s.setChannelJob(category, "downloading", item, 0)
		log.Printf("[video] processing category=%s source=%s title=%s", category, item.Source, item.Title)

		policy := s.Cfg.ChannelPolicy(category)
		seriesMode := policy.AllowSeries && shouldPostSeriesEpisodes(category, item)
		seriesPosted := 0
		candidates := episodeCandidates(item.Episodes, seriesMode)
		if len(candidates) == 0 {
			log.Printf("[video] skipped category=%s source=%s title=%s: no usable playback URL in %d entries", category, item.Source, item.Title, len(item.Episodes))
			continue
		}
		for _, candidate := range candidates {
			if seriesMode && policy.PerSeriesLimit > 0 && seriesPosted >= policy.PerSeriesLimit {
				break
			}
			episodeItem := item
			episodeItem.EpisodeName = candidate.Name
			episodeItem.EpisodeIndex = candidate.LogicalIndex
			episodeItem.EpisodeTotal = len(candidates)
			episodeKey := content.DedupKey(item)
			if seriesMode {
				episodeKey = episodeDedupKey(item, candidate.Name, candidate.FirstIndex)
				postedBefore, checkErr := s.Store.HasContentPosted(episodeKey)
				if checkErr != nil {
					log.Printf("[video] episode dedup check %s (%s): %v", item.Title, candidate.Name, checkErr)
					continue
				}
				if !postedBefore {
					postedBefore, checkErr = s.Store.HasContentPosted(legacyEpisodeDedupKey(item, candidate.Name, candidate.FirstIndex))
					if checkErr != nil {
						log.Printf("[video] legacy episode dedup check %s (%s): %v", item.Title, candidate.Name, checkErr)
						continue
					}
				}
				if postedBefore {
					continue
				}
			}
			episodeItem.Key = episodeKey
			if s.isSkipped(episodeKey) {
				continue
			}
			s.setChannelJob(category, "downloading", episodeItem, 0)
			var filePath string
			var err error
			for _, episodeURL := range candidate.URLs {
				filePath, err = s.downloadWithRetry(category, episodeItem, episodeURL)
				if err == nil {
					break
				}
				log.Printf("[video] download %s (%s): %v", item.Title, candidate.Name, err)
			}
			if err != nil {
				if !seriesMode {
					s.setChannelJob(category, "retrying", item, 0)
					_ = s.Store.LogContentFailure(content.DedupKey(item))
					reason := compactError(err)
					_ = s.Store.LogEvent("err", fmt.Sprintf("视频下载失败，已跳过当前资源：%s · %s · %s", item.Source, item.Title, reason))
				}
				continue
			}
			downloadedSize := fileSize(filePath)
			s.setChannelJob(category, "uploading", episodeItem, downloadedSize)
			captionItem := episodeItem
			captionItem.Title = s.Translate.Text(captionItem.Title)
			captionItem.Intro = s.Translate.Text(captionItem.Intro)
			if captionTextEquivalent(captionItem.Title, captionItem.Intro) {
				captionItem.Intro = ""
			}
			if isAdultCategory(category) && s.CaptionAI.Enabled() {
				aiCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				aiCopy, aiErr := s.CaptionAI.Generate(aiCtx, captionItem, category)
				cancel()
				if aiErr != nil {
					log.Printf("[caption-ai] fallback category=%s title=%s: %v", category, item.Title, aiErr)
					_ = s.Store.LogEvent("warn", fmt.Sprintf("AI 文案生成失败，已使用频道模板：%s · %s", category, item.Title))
				} else {
					captionItem.AICopy = aiCopy
					log.Printf("[caption-ai] generated category=%s title=%s model=%s", category, item.Title, s.CaptionAI.Model)
				}
			}
			caption := formatVideoCaption(s.Cfg.VideoFormatFor(category), captionItem, category)
			videoMessageID, err := s.Poster.PostVideo(filePath, caption, category, episodeItem.CoverURL, s.Cfg.SeparateCover)
			if err != nil {
				log.Printf("[video] upload %s (%s): %v", item.Title, candidate.Name, err)
				if removeErr := os.Remove(filePath); removeErr != nil && !os.IsNotExist(removeErr) {
					log.Printf("[video] cleanup failed upload %s: %v", filePath, removeErr)
				} else {
					log.Printf("[video] removed failed upload: %s", filePath)
				}
				s.setChannelJob(category, "retrying", episodeItem, downloadedSize)
				_ = s.Store.LogContentFailure(episodeKey)
				if !seriesMode {
					break
				}
				continue
			}
			uploadedSize := fileSize(filePath)
			// Videos are temporary upload artifacts; remove them immediately after
			// Telegram confirms the upload to prevent the VPS disk filling up.
			if removeErr := os.Remove(filePath); removeErr != nil {
				log.Printf("[video] cleanup %s: %v", filePath, removeErr)
			}

			posted++
			seriesPosted++
			_ = s.Store.ClearSourceFailure(sourceKey)
			if err := s.Store.LogContentPost(episodeKey); err != nil {
				log.Printf("[content] record %s (%s): %v", item.Title, candidate.Name, err)
			}
			if seriesMode {
				directoryItem := episodeItem
				directoryItem.Title = s.Translate.Text(directoryItem.Title)
				seriesKey := seriesIdentity(episodeItem.Title) + "\x00" + strings.TrimSpace(episodeItem.Year)
				if directoryErr := s.updateSeriesDirectory(category, directoryItem, seriesKey, videoMessageID); directoryErr != nil {
					log.Printf("[directory] update %s (%s): %v", item.Title, candidate.Name, directoryErr)
					_ = s.Store.LogEvent("warn", fmt.Sprintf("电视剧目录更新失败：%s · %s", item.Title, compactError(directoryErr)))
				}
			}
			s.setChannelJob(category, "completed", episodeItem, uploadedSize)
			log.Printf("[video] posted: %s (%s, category=%s, %.0fMB)", item.Title, candidate.Name, category, float64(uploadedSize)/1024/1024)
			time.Sleep(3 * time.Second) // TG rate limit for large uploads
			if !seriesMode {
				break
			}
		}
	}
	return posted
}

func (s *Scheduler) downloadWithRetry(category string, item content.ContentItem, mediaURL string) (string, error) {
	var lastErr error
	delays := []time.Duration{5 * time.Second, 15 * time.Second}
	for attempt := 0; attempt <= len(delays); attempt++ {
		filePath, err := s.Video.Download(mediaURL, videoFileName(item))
		if err == nil {
			return filePath, nil
		}
		lastErr = err
		if !isTransientMediaError(err) || attempt == len(delays) {
			break
		}
		delay := delays[attempt]
		log.Printf("[video] transient download failure; retrying in %s category=%s title=%s attempt=%d: %v", delay, category, item.Title, attempt+2, err)
		s.setChannelJob(category, "retrying", item, 0)
		time.Sleep(delay)
		s.setChannelJob(category, "downloading", item, 0)
	}
	return "", lastErr
}

func isTransientMediaError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"404 not found", "http 404", "server returned 404",
		"429 too many requests", "http 429",
		"server returned 500", "server returned 502", "server returned 503", "server returned 504",
		"http 500", "http 502", "http 503", "http 504",
		"connection reset", "connection refused", "timed out", "timeout", "temporarily unavailable",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (s *Scheduler) updateSeriesDirectory(category string, item content.ContentItem, seriesKey string, videoMessageID int) error {
	channelID := s.Cfg.PickChannel(category)
	if channelID == 0 || videoMessageID <= 0 {
		return fmt.Errorf("channel or video message is unavailable")
	}
	entries, err := s.Store.SeriesDirectory(category, channelID, 20)
	if err != nil {
		return err
	}
	directoryMessageID := 0
	for _, entry := range entries {
		if entry.DirectoryMsgID > 0 {
			directoryMessageID = entry.DirectoryMsgID
			break
		}
	}
	if err := s.Store.UpsertSeriesDirectory(store.SeriesDirectoryEntry{
		Category: category, ChannelID: channelID, SeriesKey: seriesKey, Title: item.Title,
		Year: item.Year, Remarks: item.Remarks, Completed: isCompletedSeries(item.Remarks, item.EpisodeName),
		Episode: item.EpisodeName, VideoMessageID: videoMessageID,
		DirectoryMsgID: directoryMessageID, Episodes: []store.SeriesDirectoryEpisode{{Episode: item.EpisodeName, EpisodeIndex: item.EpisodeIndex, VideoMessageID: videoMessageID}},
	}); err != nil {
		return err
	}
	entries, err = s.Store.SeriesDirectory(category, channelID, 20)
	if err != nil {
		return err
	}
	text := buildSeriesDirectoryText(channelID, entries)
	// The TV directory must remain the newest message after every episode.
	// Editing the existing pinned message leaves it at its original position,
	// so delete the old directory and publish a fresh one after the upload.
	messageID, err := s.Poster.RecreatePinnedDirectory(text, channelID, directoryMessageID)
	if err != nil {
		return err
	}
	log.Printf("[directory] recreated after episode category=%s channel=%d old=%d new=%d", category, channelID, directoryMessageID, messageID)
	if messageID != directoryMessageID {
		return s.Store.SetSeriesDirectoryMessage(category, channelID, messageID)
	}
	return nil
}

func (s *Scheduler) RefreshSeriesDirectory(category string, recreate bool) error {
	channelIDs := s.Cfg.ChannelsFor(category)
	if len(channelIDs) == 0 {
		return fmt.Errorf("channel is not configured for %s", category)
	}
	for _, channelID := range channelIDs {
		entries, err := s.Store.SeriesDirectory(category, channelID, 20)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			continue
		}
		messageID := 0
		for _, entry := range entries {
			if entry.DirectoryMsgID > 0 {
				messageID = entry.DirectoryMsgID
				break
			}
		}
		text := buildSeriesDirectoryText(channelID, entries)
		var nextID int
		if recreate {
			nextID, err = s.Poster.RecreatePinnedDirectory(text, channelID, messageID)
		} else {
			nextID, err = s.Poster.UpsertPinnedDirectory(text, channelID, messageID)
		}
		if err != nil {
			return err
		}
		if nextID != messageID {
			if err := s.Store.SetSeriesDirectoryMessage(category, channelID, nextID); err != nil {
				return err
			}
		}
		log.Printf("[directory] maintained category=%s channel=%d message=%d recreate=%t", category, channelID, nextID, recreate)
	}
	return nil
}

func buildSeriesDirectoryText(channelID int64, entries []store.SeriesDirectoryEntry) string {
	var b strings.Builder
	b.WriteString("<b>📺 电视剧目录</b>\n\n")
	for _, entry := range entries {
		label := strings.TrimSpace(entry.Title)
		if entry.Year != "" {
			label += "（" + entry.Year + "）"
		}
		fmt.Fprintf(&b, "• %s ·", escapeHTML(label))
		for _, episode := range entry.Episodes {
			fmt.Fprintf(&b, " <a href=\"%s\">%s</a>", telegramMessageLink(channelID, episode.VideoMessageID), escapeHTML(compactEpisodeLabel(episode.Episode, episode.EpisodeIndex)))
		}
		b.WriteString("\n")
		if b.Len() > 3700 {
			break
		}
	}
	b.WriteString("\n<i>点击集数直接观看；新上传剧集会自动追加。</i>")
	return b.String()
}

var episodeNumberPattern = regexp.MustCompile(`(?i)(?:第\s*)?(\d{1,4})(?:\s*[集期话]|\b)`)
var completedSeriesPattern = regexp.MustCompile(`(?i)(?:已?完结|已?完結|已完|全集|全\s*\d{1,4}\s*集|complete(?:d)?)`)

func isCompletedSeries(remarks, episode string) bool {
	return completedSeriesPattern.MatchString(strings.TrimSpace(remarks + " " + episode))
}

func compactEpisodeLabel(name string, index int) string {
	if match := episodeNumberPattern.FindStringSubmatch(strings.TrimSpace(name)); len(match) > 1 {
		if number, err := strconv.Atoi(match[1]); err == nil {
			return fmt.Sprintf("%02d", number)
		}
	}
	if index > 0 {
		return fmt.Sprintf("%02d", index)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "正片"
	}
	return name
}

func telegramMessageLink(channelID int64, messageID int) string {
	id := strconv.FormatInt(channelID, 10)
	id = strings.TrimPrefix(id, "-100")
	return fmt.Sprintf("https://t.me/c/%s/%d", id, messageID)
}

func (s *Scheduler) hasPendingEpisode(item content.ContentItem) (bool, error) {
	for _, candidate := range episodeCandidates(item.Episodes, true) {
		posted, err := s.Store.HasContentPosted(episodeDedupKey(item, candidate.Name, candidate.FirstIndex))
		if err != nil {
			return false, err
		}
		if !posted {
			posted, err = s.Store.HasContentPosted(legacyEpisodeDedupKey(item, candidate.Name, candidate.FirstIndex))
			if err != nil {
				return false, err
			}
		}
		if !posted {
			return true, nil
		}
	}
	return false, nil
}

func episodeCandidates(episodes []string, collapseAlternates bool) []episodeCandidate {
	candidates := make([]episodeCandidate, 0, len(episodes))
	byID := make(map[string]int)
	for index, episode := range episodes {
		name, episodeURL, ok := splitEpisode(episode)
		if !ok {
			continue
		}
		id := episodeIdentity(name, index)
		if collapseAlternates {
			if existing, ok := byID[id]; ok {
				candidates[existing].URLs = append(candidates[existing].URLs, episodeURL)
				continue
			}
			byID[id] = len(candidates)
		}
		candidates = append(candidates, episodeCandidate{
			Name:         name,
			URLs:         []string{episodeURL},
			FirstIndex:   index,
			LogicalIndex: len(candidates) + 1,
		})
	}
	return candidates
}

func splitEpisode(raw string) (name, url string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	parts := strings.SplitN(raw, "$", 2)
	if len(parts) == 1 {
		if parsed, err := urlpkg.ParseRequestURI(raw); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
			return "正片", raw, true
		}
		return "", "", false
	}
	name = strings.TrimSpace(parts[0])
	url = strings.TrimSpace(parts[1])
	if name == "" {
		name = "正片"
	}
	return name, url, url != ""
}

func episodeDedupKey(item content.ContentItem, episodeName string, index int) string {
	// A series is a logical directory. The same show/episode published by
	// another station (or with a changed vod_id) is an alternate source, not a
	// new video. Keep year in the identity to distinguish remakes.
	raw := fmt.Sprintf("series:%s\x00year:%s\x00episode:%s",
		seriesIdentity(item.Title), strings.TrimSpace(item.Year), episodeIdentity(episodeName, index))
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum[:])
}

func legacyEpisodeDedupKey(item content.ContentItem, episodeName string, index int) string {
	raw := fmt.Sprintf("%s\x00episode:%s", content.DedupKey(item), episodeIdentity(episodeName, index))
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum[:])
}

func seriesIdentity(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	var normalized strings.Builder
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

func episodeIdentity(episodeName string, index int) string {
	name := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(episodeName)), ""))
	if name == "" {
		return fmt.Sprintf("index:%d", index+1)
	}
	return "name:" + name
}

func uniqueEpisodeCount(episodes []string) int {
	return len(episodeCandidates(episodes, true))
}

func shouldPostSeriesEpisodes(routeCategory string, item content.ContentItem) bool {
	if len(item.Episodes) <= 1 {
		return false
	}
	raw := strings.ToLower(routeCategory + " " + item.Category + " " + item.TypeName + " " + item.Class)
	return strings.Contains(raw, "电视剧") ||
		strings.Contains(raw, "电视") ||
		strings.Contains(raw, "tv") ||
		strings.Contains(raw, "动漫") ||
		strings.Contains(raw, "anime") ||
		strings.Contains(raw, "综艺") ||
		strings.Contains(raw, "variety") ||
		strings.Contains(raw, "纪录片") ||
		strings.Contains(raw, "documentary")
}

func videoFileName(item content.ContentItem) string {
	if strings.TrimSpace(item.EpisodeName) == "" {
		return item.Title
	}
	return strings.TrimSpace(item.Title + " " + item.EpisodeName)
}

func sourceFailureKeyForStation(source store.Station) string {
	key := strings.TrimSpace(source.APIURL)
	if key == "" {
		key = strings.TrimSpace(source.Slug)
	}
	return strings.TrimRight(strings.ToLower(key), "/")
}

func sourceFailureKeyForItem(item content.ContentItem) string {
	key := strings.TrimSpace(item.SourceURL)
	if key == "" {
		key = strings.TrimSpace(item.Source)
	}
	return strings.TrimRight(strings.ToLower(key), "/")
}

func compactError(err error) string {
	if err == nil {
		return "unknown error"
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if len(msg) > 240 {
		return msg[:240]
	}
	return msg
}

func selectRoutableItems(items []content.ContentItem, limit int, cfg *config.Config) []content.ContentItem {
	if limit <= 0 {
		return nil
	}

	type categoryBucket struct {
		sourceOrder []string
		items       map[string][]content.ContentItem
		nextSource  int
	}
	buckets := make(map[string]*categoryBucket)
	var categoryOrder []string
	for _, item := range items {
		category := item.Category
		if category == "" {
			category = "default"
		}
		if len(cfg.ChannelsFor(category)) == 0 {
			continue
		}
		bucket, ok := buckets[category]
		if !ok {
			bucket = &categoryBucket{items: make(map[string][]content.ContentItem)}
			buckets[category] = bucket
			categoryOrder = append(categoryOrder, category)
		}
		sourceKey := strings.TrimSpace(item.SourceURL)
		if sourceKey == "" {
			sourceKey = strings.TrimSpace(item.Source)
		}
		if _, ok := bucket.items[sourceKey]; !ok {
			bucket.sourceOrder = append(bucket.sourceOrder, sourceKey)
		}
		bucket.items[sourceKey] = append(bucket.items[sourceKey], item)
	}

	result := make([]content.ContentItem, 0, limit)
	for len(result) < limit {
		added := false
		for _, category := range categoryOrder {
			bucket := buckets[category]
			if len(bucket.sourceOrder) == 0 {
				continue
			}
			for checked := 0; checked < len(bucket.sourceOrder); checked++ {
				index := bucket.nextSource % len(bucket.sourceOrder)
				bucket.nextSource = (index + 1) % len(bucket.sourceOrder)
				sourceKey := bucket.sourceOrder[index]
				sourceItems := bucket.items[sourceKey]
				if len(sourceItems) == 0 {
					continue
				}
				result = append(result, sourceItems[0])
				bucket.items[sourceKey] = sourceItems[1:]
				added = true
				break
			}
			if len(result) == limit {
				break
			}
		}
		if !added {
			break
		}
	}
	return result
}

func selectContentSources(all []store.Station, cfg *config.Config, baseLimit, perCategory int) []store.Station {
	selected := make([]store.Station, 0, baseLimit+len(cfg.ChannelMap)*perCategory)
	seen := make(map[string]bool)
	add := func(source store.Station) {
		key := strings.TrimRight(strings.ToLower(strings.TrimSpace(source.APIURL)), "/")
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		selected = append(selected, source)
	}

	for i := 0; i < len(all) && i < baseLimit; i++ {
		add(all[i])
	}
	for channelCategory := range cfg.ChannelMap {
		matched := 0
		for _, source := range all {
			if !stationCategoryMatches(channelCategory, source.Category) {
				continue
			}
			before := len(selected)
			add(source)
			if len(selected) > before {
				matched++
			}
			if matched == perCategory {
				break
			}
		}
	}
	return selected
}

func stationCategoryMatches(channelCategory, stationCategory string) bool {
	channelCategory = strings.TrimSpace(strings.ToLower(channelCategory))
	stationCategory = strings.TrimSpace(strings.ToLower(stationCategory))
	switch channelCategory {
	case "adult", "成人":
		return stationCategory == "adult" || stationCategory == "成人"
	case "movie", "电影":
		return stationCategory == "movie" || stationCategory == "电影"
	case "tv", "电视剧", "电视":
		return stationCategory == "tv" || stationCategory == "电视剧" || stationCategory == "电视"
	case "anime", "动漫", "动画":
		return stationCategory == "anime" || stationCategory == "动漫" || stationCategory == "动画"
	case "variety", "综艺":
		return stationCategory == "variety" || stationCategory == "综艺"
	default:
		return channelCategory == stationCategory
	}
}

func isAdultCategory(category string) bool {
	switch strings.TrimSpace(strings.ToLower(category)) {
	case "adult", "成人", "av":
		return true
	default:
		return false
	}
}

func formatVideoCaption(format string, item content.ContentItem, routeCategory string) string {
	if strings.TrimSpace(format) == "" {
		format = "🎬 {code}\n{channel}\n{title}\n简介：{intro}\n分类：{category}\n更新时间：{updated_at}"
	}
	channelLabel, categoryLabel := videoCaptionLabels(routeCategory)

	// Migrate the previously hard-coded heading at render time so existing
	// installations immediately produce the correct text for every channel.
	format = strings.ReplaceAll(format, "Madtoby的AV精选", "Madtoby的{channel}")
	code := ""
	if item.VodID > 0 {
		code = fmt.Sprintf("%d", item.VodID)
	}
	episodeIndex, episodeTotal := "", ""
	if item.EpisodeIndex > 0 {
		episodeIndex = fmt.Sprintf("%d", item.EpisodeIndex)
	}
	if item.EpisodeTotal > 0 {
		episodeTotal = fmt.Sprintf("%d", item.EpisodeTotal)
	}
	rating := strings.TrimSpace(item.Score)
	if rating == "" {
		rating = randomScore()
	}
	values := map[string]string{
		"{code}": code, "{id}": code,
		"{title}": escapeHTML(item.Title), "{name}": escapeHTML(item.Title),
		"{episode}": escapeHTML(item.EpisodeName), "{episode_name}": escapeHTML(item.EpisodeName),
		"{episode_index}": episodeIndex, "{episode_total}": episodeTotal,
		"{channel}": escapeHTML(channelLabel), "{category}": escapeHTML(categoryLabel),
		"{raw_category}": escapeHTML(item.Category), "{type}": escapeHTML(item.TypeName),
		"{class}": escapeHTML(item.Class), "{tags}": escapeHTML(contentTags(item, routeCategory)),
		"{actor}": escapeHTML(item.Actor), "{director}": escapeHTML(item.Director),
		"{area}": escapeHTML(item.Area), "{language}": escapeHTML(item.Language),
		"{year}": escapeHTML(item.Year), "{score}": escapeHTML(item.Score),
		"{rating}": escapeHTML(rating), "{random_score}": escapeHTML(randomScore()),
		"{emoji}": escapeHTML(randomEmoji()), "{duration}": escapeHTML(item.Duration),
		"{remarks}": escapeHTML(item.Remarks), "{source}": escapeHTML(item.Source),
		"{source_url}": escapeHTML(item.SourceURL), "{intro}": escapeHTML(item.Intro),
		"{cover}": "", "{cover_url}": escapeHTML(item.CoverURL),
		"{updated_at}": escapeHTML(item.VodTime),
		"{ai_copy}":    escapeHTML(item.AICopy),
	}
	return cleanCaptionTemplate(format, values)
}

var captionHTMLPattern = regexp.MustCompile(`<[^>]+>`)
var captionComparablePattern = regexp.MustCompile(`[\p{Z}\p{P}\p{S}]+`)

func captionTextEquivalent(title, intro string) bool {
	normalize := func(value string) string {
		value = captionHTMLPattern.ReplaceAllString(value, "")
		value = strings.ToLower(strings.TrimSpace(value))
		return captionComparablePattern.ReplaceAllString(value, "")
	}
	title = normalize(title)
	intro = normalize(intro)
	if title == "" || intro == "" {
		return false
	}
	if title == intro {
		return true
	}
	// Providers often use "title + title" or add a short resource code as
	// the description. Hide it when it carries no meaningful extra text.
	return len([]rune(intro)) <= len([]rune(title))*2+12 && strings.Count(intro, title) > 0
}

var captionTokenPattern = regexp.MustCompile(`\{[a-zA-Z_][a-zA-Z0-9_]*(?::\d+)?\}`)

func cleanCaptionTemplate(format string, values map[string]string) string {
	lines := strings.Split(format, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		tokens := captionTokenPattern.FindAllString(line, -1)
		if len(tokens) > 0 {
			hasValue := false
			for _, token := range tokens {
				if strings.HasPrefix(token, "{random") || strings.TrimSpace(values[token]) != "" {
					hasValue = true
					break
				}
			}
			if !hasValue {
				continue
			}
		}
		line = captionTokenPattern.ReplaceAllStringFunc(line, func(token string) string {
			if strings.HasPrefix(token, "{random") {
				return token
			}
			return values[token]
		})
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.Trim(line, "·/|,-—：: "))
		if line != "" {
			result = append(result, line)
		}
	}
	caption := strings.Join(result, "\n")
	for strings.Contains(caption, "\n\n\n") {
		caption = strings.ReplaceAll(caption, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(replaceRandomTokens(caption))
}

func replaceRandomTokens(value string) string {
	value = strings.ReplaceAll(value, "{random}", randomString(6))
	for {
		start := strings.Index(value, "{random:")
		if start < 0 {
			return value
		}
		end := strings.Index(value[start:], "}")
		if end < 0 {
			return value
		}
		token := value[start : start+end+1]
		rawLen := strings.TrimSuffix(strings.TrimPrefix(token, "{random:"), "}")
		n, err := strconv.Atoi(rawLen)
		if err != nil || n <= 0 {
			n = 6
		}
		if n > 32 {
			n = 32
		}
		value = strings.Replace(value, token, randomString(n), 1)
	}
}

func randomString(n int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, n)
	if _, err := cryptorand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = byte(time.Now().UnixNano() >> (i % 8))
		}
	}
	var b strings.Builder
	for _, value := range buf {
		b.WriteByte(alphabet[int(value)%len(alphabet)])
	}
	return b.String()
}

func randomScore() string {
	seed := time.Now().UnixNano()
	major := 7 + seed%3
	minor := (seed / 10) % 10
	if major == 9 && minor > 8 {
		minor = 8
	}
	return fmt.Sprintf("%d.%d", major, minor)
}

func randomEmoji() string {
	emojis := []string{"🎬", "🔥", "✨", "⭐", "📺", "🍿", "🎞️", "💫"}
	return emojis[int(time.Now().UnixNano()%int64(len(emojis)))]
}

func contentTags(item content.ContentItem, routeCategory string) string {
	_, categoryLabel := videoCaptionLabels(routeCategory)
	raw := []string{categoryLabel, item.TypeName, item.Class, item.Area, item.Year}
	seen := make(map[string]bool)
	tags := make([]string, 0, len(raw))
	for _, group := range raw {
		for _, value := range strings.FieldsFunc(group, func(r rune) bool {
			return r == ',' || r == '，' || r == '/' || r == '、' || r == '|' || r == ' '
		}) {
			var normalized strings.Builder
			for _, r := range strings.TrimSpace(value) {
				if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
					normalized.WriteRune(r)
				}
			}
			tag := normalized.String()
			key := strings.ToLower(tag)
			if tag == "" || seen[key] {
				continue
			}
			seen[key] = true
			tags = append(tags, "#"+tag)
		}
	}
	return strings.Join(tags, " ")
}

func videoCaptionLabels(category string) (channelLabel, categoryLabel string) {
	switch strings.TrimSpace(strings.ToLower(category)) {
	case "adult", "成人", "av":
		return "AV精选", "AV"
	case "tv", "电视剧", "电视":
		return "电视剧", "电视剧"
	case "movie", "电影":
		return "电影", "电影"
	case "anime", "动漫", "动画":
		return "动漫", "动漫"
	case "variety", "综艺":
		return "综艺", "综艺"
	case "documentary", "纪录片":
		return "纪录片", "纪录片"
	case "default", "综合影视", "":
		return "影视大全", "综合影视"
	default:
		return category, category
	}
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
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
