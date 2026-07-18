package store

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type Station struct {
	ID            int64     `json:"id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	Category      string    `json:"category"`
	Tags          string    `json:"tags"` // JSON array
	APIURL        string    `json:"api_url"`
	InterfaceType string    `json:"interface_type"`
	ResourceCount string    `json:"resource_count"`
	Availability  string    `json:"availability"`
	ResponseTime  string    `json:"response_time"`
	Description   string    `json:"description"`
	Blacklisted   bool      `json:"blacklisted"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	LastPosted    time.Time `json:"last_posted"`
}

type PostLog struct {
	ID        int64     `json:"id"`
	StationID int64     `json:"station_id"`
	MessageID int       `json:"message_id"`
	Action    string    `json:"action"` // "new", "update", "manual"
	PostedAt  time.Time `json:"posted_at"`
	Content   string    `json:"content"`
}

type Store struct {
	db *sql.DB
}

func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
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
		last_posted DATETIME DEFAULT NULL
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
	`
	_, err := s.db.Exec(ddl)
	return err
}

// UpsertStation inserts or updates a station from scraper data.
// Returns true if the station is new (first_seen == last_seen).
func (s *Store) UpsertStation(st *Station) (bool, error) {
	now := time.Now()
	var existingID int64
	var existingBlacklisted bool
	err := s.db.QueryRow("SELECT id, blacklisted FROM stations WHERE slug = ?", st.Slug).
		Scan(&existingID, &existingBlacklisted)
	if err == sql.ErrNoRows {
		// New station
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

	// Update existing
	st.ID = existingID
	st.Blacklisted = existingBlacklisted
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

// GetStations returns all stations, optionally filtering blacklisted.
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

// SetBlacklist toggles the blacklist status of a station.
func (s *Store) SetBlacklist(slug string, blacklisted bool) error {
	_, err := s.db.Exec("UPDATE stations SET blacklisted=? WHERE slug=?", blacklisted, slug)
	return err
}

// HasBeenPosted checks if a station was posted recently (within duration).
func (s *Store) HasBeenPosted(slug string, within time.Duration) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM post_log pl JOIN stations s ON pl.station_id=s.id WHERE s.slug=? AND pl.posted_at > ?",
		slug, time.Now().Add(-within),
	).Scan(&count)
	return count > 0, err
}

// LogPost records a post action.
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

// GetPostHistory returns recent posting history.
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

// GetStationBySlug returns a single station.
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

// GetStats returns aggregate statistics.
func (s *Store) GetStats() (map[string]interface{}, error) {
	var total, blacklisted, posted int
	s.db.QueryRow("SELECT COUNT(*) FROM stations").Scan(&total)
	s.db.QueryRow("SELECT COUNT(*) FROM stations WHERE blacklisted=1").Scan(&blacklisted)
	s.db.QueryRow("SELECT COUNT(*) FROM post_log").Scan(&posted)
	return map[string]interface{}{
		"total":      total,
		"blacklisted": blacklisted,
		"posted":     posted,
	}, nil
}
