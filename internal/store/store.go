package store

import (
	"database/sql"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Station struct {
	ID            int64     `json:"id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	Category      string    `json:"category"`
	Tags          string    `json:"tags"`
	APIURL        string    `json:"api_url"`
	InterfaceType string    `json:"interface_type"`
	ResourceCount string    `json:"resource_count"`
	Availability  string    `json:"availability"`
	ResponseTime  string    `json:"response_time"`
	Description   string    `json:"description"`
	Blacklisted   bool      `json:"blacklisted"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	LastPosted    string    `json:"last_posted"`

	HealthScore           int        `json:"health_score"`
	HealthLabel           string     `json:"health_label"`
	DownloadCooldown      bool       `json:"download_cooldown"`
	DownloadFailCount     int        `json:"download_fail_count"`
	LastDownloadError     string     `json:"last_download_error,omitempty"`
	LastDownloadFailedAt  *time.Time `json:"last_download_failed_at,omitempty"`
	LastDownloadFailureID string     `json:"-"`
}

type PostLog struct {
	ID        int64     `json:"id"`
	StationID int64     `json:"station_id"`
	MessageID int       `json:"message_id"`
	Action    string    `json:"action"`
	PostedAt  time.Time `json:"posted_at"`
	Content   string    `json:"content"`
}

type EventLog struct {
	ID        int64     `json:"id"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type ScheduledMessage struct {
	ID              int64      `json:"id"`
	ChannelCategory string     `json:"channel_category"`
	ChannelID       int64      `json:"channel_id"`
	Content         string     `json:"content"`
	ScheduleType    string     `json:"schedule_type"`
	IntervalMinutes int        `json:"interval_minutes"`
	DailyTime       string     `json:"daily_time"`
	Enabled         bool       `json:"enabled"`
	LastSentAt      *time.Time `json:"last_sent_at,omitempty"`
	NextRunAt       time.Time  `json:"next_run_at"`
	LastStatus      string     `json:"last_status"`
	LastError       string     `json:"last_error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type SeriesDirectoryEntry struct {
	Category       string
	ChannelID      int64
	SeriesKey      string
	Title          string
	Year           string
	Episode        string
	VideoMessageID int
	DirectoryMsgID int
	UpdatedAt      time.Time
}

type Store struct {
	db *sql.DB
}

type SourceFailure struct {
	SourceKey  string    `json:"source_key"`
	SourceName string    `json:"source_name"`
	FailedAt   time.Time `json:"failed_at"`
	FailCount  int       `json:"fail_count"`
	LastError  string    `json:"last_error"`
}

func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=30000")
	if err != nil {
		return nil, err
	}
	// SQLite serializes writers; one pooled connection prevents scheduler/API lock races.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	ddl := `
	CREATE TABLE IF NOT EXISTS stations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		slug TEXT UNIQUE NOT NULL,
		name TEXT NOT NULL,
		category TEXT DEFAULT '',
		tags TEXT DEFAULT '[]',
		api_url TEXT DEFAULT '',
		interface_type TEXT DEFAULT '',
		resource_count TEXT DEFAULT '',
		availability TEXT DEFAULT '',
		response_time TEXT DEFAULT '',
		description TEXT DEFAULT '',
		blacklisted INTEGER DEFAULT 0,
		first_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_posted TEXT DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS post_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		station_id INTEGER NOT NULL,
		message_id INTEGER DEFAULT 0,
		action TEXT DEFAULT 'new',
		posted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		content TEXT DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_stations_slug ON stations(slug);
	CREATE INDEX IF NOT EXISTS idx_stations_blacklisted ON stations(blacklisted);
	CREATE INDEX IF NOT EXISTS idx_post_log_station_id ON post_log(station_id);
	CREATE TABLE IF NOT EXISTS event_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		level TEXT NOT NULL DEFAULT 'info',
		message TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_event_log_created_at ON event_log(created_at);
	CREATE TABLE IF NOT EXISTS content_posts (
		content_key TEXT PRIMARY KEY,
		posted_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_content_posts_posted_at ON content_posts(posted_at);
	CREATE TABLE IF NOT EXISTS translation_cache (
		source_hash TEXT PRIMARY KEY,
		source_text TEXT NOT NULL,
		translated_text TEXT NOT NULL,
		provider TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS series_directory (
		category TEXT NOT NULL,
		channel_id INTEGER NOT NULL,
		series_key TEXT NOT NULL,
		title TEXT NOT NULL,
		year TEXT DEFAULT '',
		episode TEXT DEFAULT '',
		video_message_id INTEGER NOT NULL,
		directory_message_id INTEGER DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (category, channel_id, series_key)
	);
	CREATE INDEX IF NOT EXISTS idx_series_directory_updated
		ON series_directory(category, channel_id, updated_at DESC);
	CREATE TABLE IF NOT EXISTS content_failures (
		content_key TEXT PRIMARY KEY,
		failed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		fail_count INTEGER DEFAULT 1
	);
	CREATE INDEX IF NOT EXISTS idx_content_failures_failed_at ON content_failures(failed_at);
	CREATE TABLE IF NOT EXISTS content_source_failures (
		source_key TEXT PRIMARY KEY,
		source_name TEXT DEFAULT '',
		failed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		fail_count INTEGER DEFAULT 1,
		last_error TEXT DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_content_source_failures_failed_at ON content_source_failures(failed_at);
	CREATE TABLE IF NOT EXISTS scheduled_messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_category TEXT NOT NULL,
		channel_id INTEGER NOT NULL,
		content TEXT NOT NULL,
		schedule_type TEXT NOT NULL DEFAULT 'interval',
		interval_minutes INTEGER NOT NULL DEFAULT 60,
		daily_time TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		last_sent_at DATETIME,
		next_run_at DATETIME NOT NULL,
		last_status TEXT NOT NULL DEFAULT 'waiting',
		last_error TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_scheduled_messages_due
		ON scheduled_messages(enabled, next_run_at);
	`
	_, err := s.db.Exec(ddl)
	if err != nil {
		return err
	}
	// Keep known migrated endpoints usable after an interface list refresh.
	_, err = s.db.Exec("UPDATE stations SET api_url=? WHERE api_url IN (?, ?)", "https://jyzyapi.com/provide/vod/", "https://api.jyzy.com/api.php/provide/vod", "https://api.jyzy.com/api.php/provide/vod/")
	return err
}

func (s *Store) TranslationCacheGet(hash string) (string, bool, error) {
	var translated string
	err := s.db.QueryRow("SELECT translated_text FROM translation_cache WHERE source_hash=?", hash).Scan(&translated)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return translated, err == nil, err
}

func (s *Store) TranslationCachePut(hash, source, translated, provider string) error {
	_, err := s.db.Exec(`INSERT INTO translation_cache(source_hash, source_text, translated_text, provider)
		VALUES (?, ?, ?, ?) ON CONFLICT(source_hash) DO UPDATE SET
		translated_text=excluded.translated_text, provider=excluded.provider, updated_at=CURRENT_TIMESTAMP`,
		hash, source, translated, provider)
	return err
}

func (s *Store) UpsertSeriesDirectory(entry SeriesDirectoryEntry) error {
	_, err := s.db.Exec(`INSERT INTO series_directory
		(category, channel_id, series_key, title, year, episode, video_message_id, directory_message_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(category, channel_id, series_key) DO UPDATE SET
		title=excluded.title, year=excluded.year, episode=excluded.episode,
		video_message_id=excluded.video_message_id,
		directory_message_id=CASE WHEN excluded.directory_message_id > 0 THEN excluded.directory_message_id ELSE series_directory.directory_message_id END,
		updated_at=CURRENT_TIMESTAMP`, entry.Category, entry.ChannelID, entry.SeriesKey,
		entry.Title, entry.Year, entry.Episode, entry.VideoMessageID, entry.DirectoryMsgID)
	return err
}

func (s *Store) SeriesDirectory(category string, channelID int64, limit int) ([]SeriesDirectoryEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 80
	}
	rows, err := s.db.Query(`SELECT category, channel_id, series_key, title, year, episode,
		video_message_id, directory_message_id, updated_at FROM series_directory
		WHERE category=? AND channel_id=? ORDER BY updated_at DESC LIMIT ?`, category, channelID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SeriesDirectoryEntry
	for rows.Next() {
		var entry SeriesDirectoryEntry
		if err := rows.Scan(&entry.Category, &entry.ChannelID, &entry.SeriesKey, &entry.Title,
			&entry.Year, &entry.Episode, &entry.VideoMessageID, &entry.DirectoryMsgID, &entry.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (s *Store) SetSeriesDirectoryMessage(category string, channelID int64, messageID int) error {
	_, err := s.db.Exec(`UPDATE series_directory SET directory_message_id=?
		WHERE category=? AND channel_id=?`, messageID, category, channelID)
	return err
}

func NextScheduledRun(scheduleType string, intervalMinutes int, dailyTime string, after time.Time) time.Time {
	if scheduleType == "daily" {
		parts := strings.Split(dailyTime, ":")
		if len(parts) == 2 {
			hour, hourErr := strconv.Atoi(parts[0])
			minute, minuteErr := strconv.Atoi(parts[1])
			if hourErr == nil && minuteErr == nil && hour >= 0 && hour < 24 && minute >= 0 && minute < 60 {
				location, err := time.LoadLocation("Asia/Shanghai")
				if err != nil {
					location = time.FixedZone("Asia/Shanghai", 8*60*60)
				}
				localAfter := after.In(location)
				next := time.Date(localAfter.Year(), localAfter.Month(), localAfter.Day(), hour, minute, 0, 0, location)
				if !next.After(localAfter) {
					next = next.AddDate(0, 0, 1)
				}
				return next.UTC()
			}
		}
	}
	if intervalMinutes < 1 {
		intervalMinutes = 60
	}
	return after.Add(time.Duration(intervalMinutes) * time.Minute).UTC()
}

func (s *Store) CreateScheduledMessage(message *ScheduledMessage) error {
	now := time.Now().UTC()
	message.NextRunAt = NextScheduledRun(message.ScheduleType, message.IntervalMinutes, message.DailyTime, now)
	result, err := s.db.Exec(`INSERT INTO scheduled_messages
		(channel_category, channel_id, content, schedule_type, interval_minutes,
		 daily_time, enabled, next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.ChannelCategory, message.ChannelID, message.Content, message.ScheduleType,
		message.IntervalMinutes, message.DailyTime, message.Enabled, message.NextRunAt, now, now)
	if err != nil {
		return err
	}
	message.ID, _ = result.LastInsertId()
	message.CreatedAt = now
	message.UpdatedAt = now
	message.LastStatus = "waiting"
	return nil
}

func (s *Store) UpdateScheduledMessage(message *ScheduledMessage) error {
	now := time.Now().UTC()
	message.NextRunAt = NextScheduledRun(message.ScheduleType, message.IntervalMinutes, message.DailyTime, now)
	result, err := s.db.Exec(`UPDATE scheduled_messages SET
		channel_category=?, channel_id=?, content=?, schedule_type=?,
		interval_minutes=?, daily_time=?, enabled=?, next_run_at=?,
		last_status='waiting', last_error='', updated_at=?
		WHERE id=?`,
		message.ChannelCategory, message.ChannelID, message.Content, message.ScheduleType,
		message.IntervalMinutes, message.DailyTime, message.Enabled, message.NextRunAt, now, message.ID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	message.UpdatedAt = now
	return nil
}

func (s *Store) DeleteScheduledMessage(id int64) error {
	result, err := s.db.Exec("DELETE FROM scheduled_messages WHERE id=?", id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DisableScheduledMessagesForChannel(channelID int64) error {
	_, err := s.db.Exec(`UPDATE scheduled_messages SET enabled=0,
		last_status='channel_deleted', last_error='频道已删除',
		updated_at=CURRENT_TIMESTAMP WHERE channel_id=?`, channelID)
	return err
}

func (s *Store) GetScheduledMessage(id int64) (*ScheduledMessage, error) {
	row := s.db.QueryRow(`SELECT id, channel_category, channel_id, content,
		schedule_type, interval_minutes, daily_time, enabled, last_sent_at,
		next_run_at, last_status, last_error, created_at, updated_at
		FROM scheduled_messages WHERE id=?`, id)
	return scanScheduledMessage(row)
}

func (s *Store) ListScheduledMessages() ([]ScheduledMessage, error) {
	rows, err := s.db.Query(`SELECT id, channel_category, channel_id, content,
		schedule_type, interval_minutes, daily_time, enabled, last_sent_at,
		next_run_at, last_status, last_error, created_at, updated_at
		FROM scheduled_messages ORDER BY channel_category, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]ScheduledMessage, 0)
	for rows.Next() {
		message, err := scanScheduledMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, *message)
	}
	return messages, rows.Err()
}

func (s *Store) GetDueScheduledMessages(now time.Time) ([]ScheduledMessage, error) {
	rows, err := s.db.Query(`SELECT id, channel_category, channel_id, content,
		schedule_type, interval_minutes, daily_time, enabled, last_sent_at,
		next_run_at, last_status, last_error, created_at, updated_at
		FROM scheduled_messages
		WHERE enabled=1
		ORDER BY next_run_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]ScheduledMessage, 0)
	for rows.Next() {
		message, err := scanScheduledMessage(rows)
		if err != nil {
			return nil, err
		}
		if !message.NextRunAt.After(now.UTC()) {
			messages = append(messages, *message)
		}
	}
	return messages, rows.Err()
}

func (s *Store) MarkScheduledMessageResult(id int64, sent bool, nextRun time.Time, status, lastError string) error {
	if sent {
		_, err := s.db.Exec(`UPDATE scheduled_messages SET
			last_sent_at=CURRENT_TIMESTAMP, next_run_at=?, last_status=?,
			last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			nextRun.UTC(), status, lastError, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE scheduled_messages SET
		next_run_at=?, last_status=?, last_error=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`, nextRun.UTC(), status, lastError, id)
	return err
}

func (s *Store) MarkScheduledMessageSending(id int64, nextRun time.Time) error {
	_, err := s.db.Exec(`UPDATE scheduled_messages SET next_run_at=?,
		last_status='sending', last_error='', updated_at=CURRENT_TIMESTAMP
		WHERE id=?`, nextRun.UTC(), id)
	return err
}

type scheduledScanner interface {
	Scan(dest ...interface{}) error
}

func scanScheduledMessage(scanner scheduledScanner) (*ScheduledMessage, error) {
	var message ScheduledMessage
	var lastSent sql.NullTime
	err := scanner.Scan(
		&message.ID, &message.ChannelCategory, &message.ChannelID, &message.Content,
		&message.ScheduleType, &message.IntervalMinutes, &message.DailyTime,
		&message.Enabled, &lastSent, &message.NextRunAt, &message.LastStatus,
		&message.LastError, &message.CreatedAt, &message.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if lastSent.Valid {
		last := lastSent.Time
		message.LastSentAt = &last
	}
	return &message, nil
}

// HasContentPosted checks whether a source item was already uploaded.
func (s *Store) HasContentPosted(key string) (bool, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM content_posts WHERE content_key=?", key).Scan(&n)
	return n > 0, err
}

func (s *Store) LogContentPost(key string) error {
	_, err := s.db.Exec("INSERT OR IGNORE INTO content_posts (content_key) VALUES (?)", key)
	if err == nil {
		_, _ = s.db.Exec("DELETE FROM content_failures WHERE content_key=?", key)
	}
	return err
}

func (s *Store) ContentInFailureCooldown(key string, within time.Duration) (bool, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM content_failures WHERE content_key=? AND failed_at > ?", key, time.Now().Add(-within)).Scan(&n)
	return n > 0, err
}

func (s *Store) LogContentFailure(key string) error {
	_, err := s.db.Exec(`INSERT INTO content_failures (content_key, failed_at, fail_count)
		VALUES (?, CURRENT_TIMESTAMP, 1)
		ON CONFLICT(content_key) DO UPDATE SET failed_at=CURRENT_TIMESTAMP, fail_count=fail_count+1`, key)
	return err
}

func (s *Store) SourceInFailureCooldown(sourceKey string, within time.Duration) (bool, error) {
	sourceKey = strings.TrimSpace(sourceKey)
	if sourceKey == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM content_source_failures WHERE source_key=? AND failed_at > ?", sourceKey, time.Now().Add(-within)).Scan(&n)
	return n > 0, err
}

func (s *Store) LogSourceFailure(sourceKey, sourceName, reason string) error {
	sourceKey = strings.TrimSpace(sourceKey)
	if sourceKey == "" {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO content_source_failures (source_key, source_name, failed_at, fail_count, last_error)
		VALUES (?, ?, CURRENT_TIMESTAMP, 1, ?)
		ON CONFLICT(source_key) DO UPDATE SET source_name=excluded.source_name, failed_at=CURRENT_TIMESTAMP, fail_count=fail_count+1, last_error=excluded.last_error`,
		sourceKey, sourceName, reason)
	return err
}

func (s *Store) ClearSourceFailure(sourceKey string) error {
	sourceKey = strings.TrimSpace(sourceKey)
	if sourceKey == "" {
		return nil
	}
	_, err := s.db.Exec("DELETE FROM content_source_failures WHERE source_key=?", sourceKey)
	return err
}

func (s *Store) GetSourceFailures() (map[string]SourceFailure, error) {
	rows, err := s.db.Query(`SELECT source_key, source_name, failed_at, fail_count, last_error
		FROM content_source_failures`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	failures := make(map[string]SourceFailure)
	for rows.Next() {
		var failure SourceFailure
		if err := rows.Scan(&failure.SourceKey, &failure.SourceName, &failure.FailedAt, &failure.FailCount, &failure.LastError); err != nil {
			return nil, err
		}
		failures[strings.TrimRight(strings.ToLower(strings.TrimSpace(failure.SourceKey)), "/")] = failure
	}
	return failures, rows.Err()
}

func (s *Store) LogEvent(level, message string) error {
	_, err := s.db.Exec("INSERT INTO event_log (level, message) VALUES (?, ?)", level, message)
	return err
}

func (s *Store) GetEventHistory(limit int) ([]EventLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query("SELECT id, level, message, created_at FROM event_log ORDER BY created_at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var logs []EventLog
	for rows.Next() {
		var log EventLog
		if err := rows.Scan(&log.ID, &log.Level, &log.Message, &log.CreatedAt); err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	// The UI renders oldest-to-newest so the list reads naturally.
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}
	return logs, rows.Err()
}

func (s *Store) ClearEventHistory() error {
	_, err := s.db.Exec("DELETE FROM event_log")
	return err
}

func (s *Store) UpsertStation(st *Station) (bool, error) {
	now := time.Now()
	var existingID int64
	var existingCategory string
	var existingBlacklisted bool
	err := s.db.QueryRow("SELECT id, category, blacklisted FROM stations WHERE slug = ?", st.Slug).
		Scan(&existingID, &existingCategory, &existingBlacklisted)
	if err == sql.ErrNoRows {
		res, err := s.db.Exec(`
			INSERT INTO stations (slug, name, category, tags, api_url, interface_type,
				resource_count, availability, response_time, description, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			st.Slug, st.Name, st.Category, st.Tags, st.APIURL, st.InterfaceType,
			st.ResourceCount, st.Availability, st.ResponseTime, st.Description, now, now)
		if err != nil {
			return false, err
		}
		id, _ := res.LastInsertId()
		st.ID = id
		st.FirstSeen = now
		st.LastSeen = now
		return true, nil
	}
	if err != nil {
		return false, err
	}
	st.ID = existingID
	st.Blacklisted = existingBlacklisted
	// A category changed in the dashboard is user-owned. Do not let a later
	// scrape overwrite it with the source site's generic category.
	if strings.TrimSpace(existingCategory) != "" {
		st.Category = existingCategory
	}
	_, err = s.db.Exec(`
		UPDATE stations SET name=?, category=?, tags=?, api_url=?, interface_type=?,
			resource_count=?, availability=?, response_time=?, description=?, last_seen=?
		WHERE slug=?`,
		st.Name, st.Category, st.Tags, st.APIURL, st.InterfaceType,
		st.ResourceCount, st.Availability, st.ResponseTime, st.Description, now, st.Slug)
	if err != nil {
		return false, err
	}
	st.LastSeen = now
	return false, nil
}

func (s *Store) GetStations(includeBlacklisted bool) ([]Station, error) {
	query := "SELECT id, slug, name, category, tags, api_url, interface_type, resource_count, availability, response_time, description, blacklisted, first_seen, last_seen, last_posted FROM stations"
	if !includeBlacklisted {
		query += " WHERE blacklisted = 0"
	}
	query += " ORDER BY last_seen DESC"

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stations []Station
	for rows.Next() {
		var st Station
		if err := rows.Scan(&st.ID, &st.Slug, &st.Name, &st.Category, &st.Tags,
			&st.APIURL, &st.InterfaceType, &st.ResourceCount, &st.Availability,
			&st.ResponseTime, &st.Description, &st.Blacklisted,
			&st.FirstSeen, &st.LastSeen, &st.LastPosted); err != nil {
			return nil, err
		}
		stations = append(stations, st)
	}
	return stations, nil
}

func (s *Store) SetBlacklist(slug string, blacklisted bool) error {
	_, err := s.db.Exec("UPDATE stations SET blacklisted=? WHERE slug=?", blacklisted, slug)
	return err
}

func (s *Store) SetCategory(slug, category string) error {
	_, err := s.db.Exec("UPDATE stations SET category=? WHERE slug=?", category, slug)
	return err
}

func (s *Store) UpdateHealth(slug, availability, responseTime string) error {
	_, err := s.db.Exec("UPDATE stations SET availability=?, response_time=? WHERE slug=?", availability, responseTime, slug)
	return err
}

func (s *Store) UpdateResourceCount(slug, count string) error {
	_, err := s.db.Exec("UPDATE stations SET resource_count=? WHERE slug=?", count, slug)
	return err
}

func (s *Store) UpdateAPIURL(slug, apiURL string) error {
	_, err := s.db.Exec("UPDATE stations SET api_url=? WHERE slug=?", apiURL, slug)
	return err
}

func (s *Store) HasBeenPosted(slug string, within time.Duration) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM post_log pl JOIN stations s ON pl.station_id=s.id WHERE s.slug=? AND pl.posted_at > ?",
		slug, time.Now().Add(-within),
	).Scan(&count)
	return count > 0, err
}

func (s *Store) LogPost(stationID int64, action string, messageID int, content string) error {
	_, err := s.db.Exec(
		"INSERT INTO post_log (station_id, action, message_id, content) VALUES (?, ?, ?, ?)",
		stationID, action, messageID, content)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE stations SET last_posted=CURRENT_TIMESTAMP WHERE id=?", stationID)
	return err
}

func (s *Store) GetPostHistory(limit int) ([]PostLog, error) {
	rows, err := s.db.Query(
		"SELECT id, station_id, message_id, action, posted_at, content FROM post_log ORDER BY posted_at DESC LIMIT ?",
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []PostLog
	for rows.Next() {
		var pl PostLog
		if err := rows.Scan(&pl.ID, &pl.StationID, &pl.MessageID, &pl.Action, &pl.PostedAt, &pl.Content); err != nil {
			return nil, err
		}
		logs = append(logs, pl)
	}
	return logs, nil
}

func (s *Store) GetStationBySlug(slug string) (*Station, error) {
	var st Station
	err := s.db.QueryRow(
		"SELECT id, slug, name, category, tags, api_url, interface_type, resource_count, availability, response_time, description, blacklisted, first_seen, last_seen, last_posted FROM stations WHERE slug=?",
		slug,
	).Scan(&st.ID, &st.Slug, &st.Name, &st.Category, &st.Tags,
		&st.APIURL, &st.InterfaceType, &st.ResourceCount, &st.Availability,
		&st.ResponseTime, &st.Description, &st.Blacklisted,
		&st.FirstSeen, &st.LastSeen, &st.LastPosted)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) GetStats() (map[string]interface{}, error) {
	var total, blacklisted, posted int
	s.db.QueryRow("SELECT COUNT(*) FROM stations").Scan(&total)
	s.db.QueryRow("SELECT COUNT(*) FROM stations WHERE blacklisted=1").Scan(&blacklisted)
	s.db.QueryRow("SELECT COUNT(*) FROM post_log").Scan(&posted)
	return map[string]interface{}{
		"total":       total,
		"blacklisted": blacklisted,
		"posted":      posted,
	}, nil
}
