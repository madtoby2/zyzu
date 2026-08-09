package content

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/madtoby2/zyzu/internal/store"
)

// APIItem represents a video item from CMS JSON API.
type APIItem struct {
	VodID       int    `json:"vod_id"`
	VodName     string `json:"vod_name"`
	TypeName    string `json:"type_name"`
	VodClass    string `json:"vod_class"`
	VodActor    string `json:"vod_actor"`
	VodDirector string `json:"vod_director"`
	VodArea     string `json:"vod_area"`
	VodLang     string `json:"vod_lang"`
	VodYear     any    `json:"vod_year"`
	VodScore    any    `json:"vod_score"`
	VodDuration any    `json:"vod_duration"`
	VodTime     string `json:"vod_time"`
	VodRemarks  string `json:"vod_remarks"`
	VodContent  string `json:"vod_content"`
	VodPic      string `json:"vod_pic"`
	VodPlayURL  string `json:"vod_play_url"`
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
	Key          string   `json:"-"`
	Title        string   `json:"title"`
	TypeName     string   `json:"type_name"`
	Class        string   `json:"class"`
	Actor        string   `json:"actor"`
	Director     string   `json:"director"`
	Area         string   `json:"area"`
	Language     string   `json:"language"`
	Year         string   `json:"year"`
	Score        string   `json:"score"`
	Duration     string   `json:"duration"`
	Remarks      string   `json:"remarks"`
	Category     string   `json:"category"`
	Episodes     []string `json:"episodes"`
	EpisodeName  string   `json:"episode_name,omitempty"`
	EpisodeIndex int      `json:"episode_index,omitempty"`
	EpisodeTotal int      `json:"episode_total,omitempty"`
	CoverURL     string   `json:"cover_url"`
	Source       string   `json:"source"`
	SourceURL    string   `json:"source_url"`
	VodID        int      `json:"vod_id"`
	VodTime      string   `json:"vod_time"`
	Intro        string   `json:"intro"`
}

// DedupKey identifies one source item across scheduler runs. The source is
// part of the key because different资源站 may legitimately reuse vod_id.
func DedupKey(item ContentItem) string {
	if item.Key != "" {
		return item.Key
	}
	raw := strings.TrimSpace(item.SourceURL) + "\x00"
	if item.VodID > 0 {
		raw += fmt.Sprintf("id:%d", item.VodID)
	} else {
		raw += "title:" + strings.ToLower(strings.Join(strings.Fields(item.Title), " ")) + "\x00time:" + strings.TrimSpace(item.VodTime)
	}
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum[:])
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
	seen := map[string]bool{}

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
				key := itemKey(src.APIURL, item)
				if seen[key] {
					continue
				}
				seen[key] = true

				ci := ContentItem{
					Key:       key,
					Title:     normalizeTitle(item.VodName),
					TypeName:  item.TypeName,
					Class:     item.VodClass,
					Actor:     item.VodActor,
					Director:  item.VodDirector,
					Area:      item.VodArea,
					Language:  item.VodLang,
					Year:      scalarString(item.VodYear),
					Score:     scalarString(item.VodScore),
					Duration:  scalarString(item.VodDuration),
					Remarks:   item.VodRemarks,
					Category:  Classify(item.TypeName+" "+item.VodName+" "+item.VodRemarks, nil),
					CoverURL:  normalizeMediaURL(src.APIURL, item.VodPic),
					Source:    src.Name,
					SourceURL: src.APIURL,
					VodID:     item.VodID,
					VodTime:   item.VodTime,
					Intro:     cleanIntro(item.VodContent, item.VodName, item.VodID),
				}

				// Fetch detail for play URLs (best-effort)
				detail, err := a.fetchDetail(src, item.VodID)
				if err == nil && len(detail) > 0 {
					ci.Episodes = parseEpisodes(detail[0].VodPlayURL)
					if coverURL := normalizeMediaURL(src.APIURL, detail[0].VodPic); coverURL != "" {
						ci.CoverURL = coverURL
					}
					if intro := cleanIntro(detail[0].VodContent, item.VodName, item.VodID); intro != "" {
						ci.Intro = intro
					}
					if detail[0].VodRemarks != "" {
						ci.Remarks = detail[0].VodRemarks
					}
					if detail[0].VodClass != "" {
						ci.Class = detail[0].VodClass
					}
					if detail[0].VodActor != "" {
						ci.Actor = detail[0].VodActor
					}
					if detail[0].VodDirector != "" {
						ci.Director = detail[0].VodDirector
					}
					if detail[0].VodArea != "" {
						ci.Area = detail[0].VodArea
					}
					if detail[0].VodLang != "" {
						ci.Language = detail[0].VodLang
					}
					if value := scalarString(detail[0].VodYear); value != "" {
						ci.Year = value
					}
					if value := scalarString(detail[0].VodScore); value != "" {
						ci.Score = value
					}
					if value := scalarString(detail[0].VodDuration); value != "" {
						ci.Duration = value
					}
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

func scalarString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", typed), "0"), ".")
	case json.Number:
		return typed.String()
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func normalizeMediaURL(baseURL, rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if strings.HasPrefix(rawURL, "//") {
		base, err := url.Parse(baseURL)
		if err == nil && base.Scheme != "" {
			return base.Scheme + ":" + rawURL
		}
		return "https:" + rawURL
	}
	ref, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if ref.IsAbs() {
		return ref.String()
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return rawURL
	}
	return base.ResolveReference(ref).String()
}

// normalizeTitle removes the common API corruption where the exact same title
// is concatenated twice (for example "A A"). Keep other repeated words intact.
func normalizeTitle(title string) string {
	title = strings.TrimSpace(title)
	runes := []rune(title)
	// Check both the complete title and a prefixed title such as
	// "code name name", which some APIs return when joining fields.
	for prefix := 0; prefix < len(runes); prefix++ {
		suffix := runes[prefix:]
		if len(suffix) < 2 || len(suffix)%2 != 0 {
			continue
		}
		half := len(suffix) / 2
		if string(suffix[:half]) == string(suffix[half:]) {
			clean := strings.TrimSpace(string(runes[:prefix]) + string(suffix[:half]))
			if clean != "" {
				return clean
			}
		}
	}
	return title
}

func itemKey(sourceURL string, item APIItem) string {
	ci := ContentItem{SourceURL: sourceURL, VodID: item.VodID, Title: item.VodName, VodTime: item.VodTime}
	return DedupKey(ci)
}

func cleanIntro(intro, title string, id int) string {
	intro = html.UnescapeString(intro)
	intro = introBreakTags.ReplaceAllString(intro, "\n")
	intro = introTags.ReplaceAllString(intro, "")
	intro = strings.Join(strings.Fields(intro), " ")
	if intro == "" || intro == title || (id > 0 && intro == fmt.Sprintf("%d", id)) {
		return ""
	}
	return intro
}

var (
	introBreakTags = regexp.MustCompile(`(?i)</?(?:p|div|br|li|h[1-6])\b[^>]*>`)
	introTags      = regexp.MustCompile(`(?s)<[^>]*>`)
)

func (a *Aggregator) fetchList(src store.Station, page int) ([]APIItem, error) {
	u := buildURL(src.APIURL, map[string]string{
		"ac": "list",
		"pg": fmt.Sprintf("%d", page),
		"h":  "24", // last 24 hours
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
// AppleCMS separates episodes with "#" and independent player/source groups
// with "$$$": "第1集$url1#第2集$url2$$$正片$url3".
func parseEpisodes(raw string) []string {
	if raw == "" {
		return nil
	}
	var eps []string
	for _, group := range strings.Split(raw, "$$$") {
		for _, episode := range strings.Split(group, "#") {
			episode = strings.TrimSpace(episode)
			if episode != "" {
				eps = append(eps, episode)
			}
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
	seenAPI := make(map[string]bool)
	for _, s := range all {
		if s.APIURL == "" || s.Blacklisted {
			continue
		}
		apiKey := strings.TrimRight(strings.ToLower(strings.TrimSpace(s.APIURL)), "/")
		if seenAPI[apiKey] {
			continue
		}
		seenAPI[apiKey] = true
		active = append(active, s)
	}

	// Sort measured, healthy sources first. Empty/failed probes used to parse as
	// zero milliseconds and starve known-good sources from the content batch.
	sort.Slice(active, func(i, j int) bool {
		ti := sourceResponseTime(active[i])
		tj := sourceResponseTime(active[j])
		return ti < tj
	})

	if limit > 0 && len(active) > limit {
		active = active[:limit]
	}
	return active, nil
}

func sourceResponseTime(s store.Station) time.Duration {
	v := strings.TrimSpace(s.ResponseTime)
	if v == "" || s.Availability == "0%" {
		return time.Duration(1<<63 - 1)
	}
	d, err := time.ParseDuration(strings.ReplaceAll(v, "ms", "ms"))
	if err != nil || d <= 0 {
		return time.Duration(1<<63 - 1)
	}
	return d
}
