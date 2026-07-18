package content

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/madtoby2/zyzu/internal/store"
)

// APIItem represents a video item from CMS JSON API.
type APIItem struct {
	VodID      int    `json:"vod_id"`
	VodName    string `json:"vod_name"`
	TypeName   string `json:"type_name"`
	VodTime    string `json:"vod_time"`
	VodRemarks string `json:"vod_remarks"`
	VodPic     string `json:"vod_pic"`
	VodPlayURL string `json:"vod_play_url"`
}

// APIListResp is the list endpoint response.
type APIListResp struct {
	Code      int       `json:"code"`
	Msg       string    `json:"msg"`
	Page      int       `json:"page"`
	PageCount int       `json:"pagecount"`
	Total     int       `json:"total"`
	List      []APIItem `json:"list"`
}

// APIDetailResp is the detail endpoint response.
type APIDetailResp struct {
	Code int       `json:"code"`
	List []APIItem `json:"list"`
}

// ContentItem is our unified content format.
type ContentItem struct {
	Title    string   `json:"title"`
	TypeName string   `json:"type_name"`
	Episodes []string `json:"episodes"` // ["第1集$url", ...]
	CoverURL string   `json:"cover_url"`
	Source   string   `json:"source"`
	VodID    int      `json:"vod_id"`
	VodTime  string   `json:"vod_time"`
}

// Aggregator fetches content from CMS APIs.
type Aggregator struct {
	client  *http.Client
	sources []store.Station
	pages   int // how many pages to fetch per source
}

func New(sources []store.Station) *Aggregator {
	return &Aggregator{
		client:  &http.Client{Timeout: 15 * time.Second},
		sources: sources,
		pages:   3, // fetch first 3 pages (60 items) per source
	}
}

// FetchLatest gets the newest content across all enabled sources.
func (a *Aggregator) FetchLatest() ([]ContentItem, error) {
	var all []ContentItem
	seen := map[int]bool{}

	for _, src := range a.sources {
		if src.APIURL == "" || src.Blacklisted {
			continue
		}

		for page := 1; page <= a.pages; page++ {
			items, err := a.fetchList(src, page)
			if err != nil {
				continue
			}

			for _, item := range items {
				if seen[item.VodID] {
					continue
				}
				seen[item.VodID] = true

				ci := ContentItem{
					Title:    item.VodName,
					TypeName: item.TypeName,
					CoverURL: item.VodPic,
					Source:   src.Name,
					VodID:    item.VodID,
					VodTime:  item.VodTime,
				}

				// Fetch detail for play URLs (best-effort)
				detail, err := a.fetchDetail(src, item.VodID)
				if err == nil && len(detail) > 0 && detail[0].VodPlayURL != "" {
					ci.Episodes = parseEpisodes(detail[0].VodPlayURL)
				}

				all = append(all, ci)
			}
		}
	}

	// Sort newest first
	sort.Slice(all, func(i, j int) bool {
		return all[i].VodTime > all[j].VodTime
	})

	return all, nil
}

func (a *Aggregator) fetchList(src store.Station, page int) ([]APIItem, error) {
	u := buildURL(src.APIURL, map[string]string{
		"ac":   "list",
		"pg":   fmt.Sprintf("%d", page),
		"h":    "24", // last 24 hours
	})

	resp, err := a.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r APIListResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Code != 1 {
		return nil, fmt.Errorf("API code=%d", r.Code)
	}
	return r.List, nil
}

func (a *Aggregator) fetchDetail(src store.Station, vodID int) ([]APIItem, error) {
	u := buildURL(src.APIURL, map[string]string{
		"ac":  "detail",
		"ids": fmt.Sprintf("%d", vodID),
	})

	resp, err := a.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r APIDetailResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Code != 1 {
		return nil, fmt.Errorf("API code=%d", r.Code)
	}
	return r.List, nil
}

// buildURL appends query params to the API base URL.
func buildURL(base string, params map[string]string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// parseEpisodes splits vod_play_url into episode list.
// Format: "第1集$url1#第2集$url2#..."
func parseEpisodes(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, "#")
	var eps []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			eps = append(eps, p)
		}
	}
	return eps
}

// GetActiveSources returns non-blacklisted sources with APIs, sorted by response time.
func GetActiveSources(st *store.Store, limit int) ([]store.Station, error) {
	all, err := st.GetStations(false)
	if err != nil {
		return nil, err
	}

	var active []store.Station
	for _, s := range all {
		if s.APIURL == "" || s.Blacklisted {
			continue
		}
		active = append(active, s)
	}

	// Sort by response time (fastest first)
	sort.Slice(active, func(i, j int) bool {
		ti, _ := time.ParseDuration(strings.ReplaceAll(active[i].ResponseTime, "ms", "ms"))
		tj, _ := time.ParseDuration(strings.ReplaceAll(active[j].ResponseTime, "ms", "ms"))
		return ti < tj
	})

	if limit > 0 && len(active) > limit {
		active = active[:limit]
	}
	return active, nil
}
