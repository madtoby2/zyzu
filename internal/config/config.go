package config

import (
	"encoding/json"
	"math/rand"
	"os"
	"strings"
)

type Config struct {
	BotToken         string                   `json:"bot_token"`
	ChannelIDs       []int64                  `json:"channel_ids"`                 // deprecated
	ChannelID        int64                    `json:"channel_id"`                  // deprecated
	ChannelMap       map[string][]int64       `json:"channel_map"`                 // {"adult":[-1001], "movie":[-1002], "default":[-1000]}
	ChannelIntervals map[string]int           `json:"channel_intervals,omitempty"` // category -> minutes
	ChannelPolicies  map[string]ChannelPolicy `json:"channel_policies,omitempty"`
	APIKey           string                   `json:"api_key"`
	ScrapeCron       string                   `json:"scrape_cron"`
	ContentCron      string                   `json:"content_cron"`
	ListenAddr       string                   `json:"listen_addr"`
	PostFormat       string                   `json:"post_format"`
	VideoFormat      string                   `json:"video_format"`
	VideoFormats     map[string]string        `json:"video_formats,omitempty"`
	ContentMode      string                   `json:"content_mode"`
	ContentLimit     int                      `json:"content_limit"`
	SeparateCover    bool                     `json:"separate_cover"`
}

type ChannelPolicy struct {
	Paused         bool `json:"paused"`
	PerRunLimit    int  `json:"per_run_limit"`
	PerSeriesLimit int  `json:"per_series_limit"`
	AllowSeries    bool `json:"allow_series"`
}

func DefaultChannelPolicy(category string) ChannelPolicy {
	switch strings.TrimSpace(strings.ToLower(category)) {
	case "adult", "成人", "av":
		return ChannelPolicy{PerRunLimit: 3, PerSeriesLimit: 1, AllowSeries: false}
	case "tv", "电视剧", "电视", "anime", "动漫", "variety", "综艺", "documentary", "纪录片":
		return ChannelPolicy{PerRunLimit: 2, PerSeriesLimit: 3, AllowSeries: true}
	case "movie", "电影":
		return ChannelPolicy{PerRunLimit: 1, PerSeriesLimit: 1, AllowSeries: false}
	default:
		return ChannelPolicy{PerRunLimit: 2, PerSeriesLimit: 2, AllowSeries: true}
	}
}

func (c *Config) ChannelPolicy(category string) ChannelPolicy {
	policy := DefaultChannelPolicy(category)
	for _, key := range channelKeys(category) {
		if configured, ok := c.ChannelPolicies[key]; ok {
			if configured.PerRunLimit > 0 {
				policy.PerRunLimit = configured.PerRunLimit
			}
			if configured.PerSeriesLimit > 0 {
				policy.PerSeriesLimit = configured.PerSeriesLimit
			}
			policy.Paused = configured.Paused
			policy.AllowSeries = configured.AllowSeries
			return policy
		}
	}
	return policy
}

// ChannelIntervalMinutes returns the configured automatic publishing interval.
// Defaults favor a frequent AV feed while keeping full-length TV and movie
// uploads from continuously filling the queue.
func (c *Config) ChannelIntervalMinutes(category string) int {
	if minutes := c.ChannelIntervals[category]; minutes > 0 {
		return minutes
	}
	switch strings.TrimSpace(strings.ToLower(category)) {
	case "adult", "成人", "av":
		return 15
	case "tv", "电视剧", "电视":
		return 30
	case "movie", "电影":
		return 60
	default:
		return 30
	}
}

func Default() *Config {
	return &Config{
		ScrapeCron:    "0 0 */6 * * *",
		ContentCron:   "0 8,20 * * *",
		ListenAddr:    ":8080",
		ContentMode:   "video",
		ContentLimit:  10,
		VideoFormats:  defaultVideoFormats(),
		SeparateCover: true,
		PostFormat:    "📡 *{name}*  |  {availability}  |  {resource_count}条  |  {response_time}\n🏷 {tags}\n🔗 `{api_url}`",
		VideoFormat:   "🎬 {code}\n{channel}\n{title}\n简介：{intro}\n分类：{category}\n更新时间：{updated_at}",
	}
}

func defaultVideoFormats() map[string]string {
	adultFormat := "🔞 <b>{title}</b>\n\n💋 {ai_copy}\n✨ 主演：{actor}\n🏷️ 标签：{class} / {tags}\n⏱️ 时长：{duration}\n⭐ 推荐：{rating}\n\n📝 {intro}\n\n#AV精选 #今夜必看"
	return map[string]string{
		"default":     "{title}\n\n简介：{intro}\n类型：{type} / {class}\n标签：{tags}\n地区：{area}\n年份：{year}\n状态：{remarks}\n更新：{updated_at}",
		"综合影视":        "{title}\n\n简介：{intro}\n类型：{type} / {class}\n标签：{tags}\n地区：{area}\n年份：{year}\n状态：{remarks}\n更新：{updated_at}",
		"成人":          adultFormat,
		"adult":       adultFormat,
		"电影":          "{title}\n\n简介：{intro}\n类型：{type} / {class}\n主演：{actor}\n地区：{area}\n年份：{year}\n状态：{remarks}\n更新：{updated_at}",
		"movie":       "{title}\n\n简介：{intro}\n类型：{type} / {class}\n主演：{actor}\n地区：{area}\n年份：{year}\n状态：{remarks}\n更新：{updated_at}",
		"电视剧":         "{title} · {episode}\n\n简介：{intro}\n类型：{type} / {class}\n主演：{actor}\n地区：{area}\n年份：{year}\n进度：第 {episode_index} / {episode_total} 集\n状态：{remarks}\n更新：{updated_at}",
		"tv":          "{title} · {episode}\n\n简介：{intro}\n类型：{type} / {class}\n主演：{actor}\n地区：{area}\n年份：{year}\n进度：第 {episode_index} / {episode_total} 集\n状态：{remarks}\n更新：{updated_at}",
		"动漫":          "{title} · {episode}\n\n简介：{intro}\n标签：{tags}\n地区：{area}\n年份：{year}\n进度：第 {episode_index} / {episode_total} 集\n状态：{remarks}\n更新：{updated_at}",
		"anime":       "{title} · {episode}\n\n简介：{intro}\n标签：{tags}\n地区：{area}\n年份：{year}\n进度：第 {episode_index} / {episode_total} 集\n状态：{remarks}\n更新：{updated_at}",
		"综艺":          "{title} · {episode}\n\n简介：{intro}\n嘉宾：{actor}\n标签：{tags}\n地区：{area}\n年份：{year}\n进度：第 {episode_index} / {episode_total} 期\n状态：{remarks}\n更新：{updated_at}",
		"variety":     "{title} · {episode}\n\n简介：{intro}\n嘉宾：{actor}\n标签：{tags}\n地区：{area}\n年份：{year}\n进度：第 {episode_index} / {episode_total} 期\n状态：{remarks}\n更新：{updated_at}",
		"纪录片":         "{title} · {episode}\n\n简介：{intro}\n类型：{type} / {class}\n地区：{area}\n年份：{year}\n进度：第 {episode_index} / {episode_total} 集\n时长：{duration}\n更新：{updated_at}",
		"documentary": "{title} · {episode}\n\n简介：{intro}\n类型：{type} / {class}\n地区：{area}\n年份：{year}\n进度：第 {episode_index} / {episode_total} 集\n时长：{duration}\n更新：{updated_at}",
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	cfg.VideoFormats = mergeVideoFormatDefaults(cfg.VideoFormats)
	if cfg.ChannelPolicies == nil {
		cfg.ChannelPolicies = map[string]ChannelPolicy{}
	}
	return cfg, nil
}

func mergeVideoFormatDefaults(formats map[string]string) map[string]string {
	defaults := defaultVideoFormats()
	if formats == nil {
		return defaults
	}
	legacyAdult := "{title}\n\n简介：{intro}\n标签：{class}\n演员：{actor}\n时长：{duration}\n更新：{updated_at}"
	previousAdult := "🔞 <b>{title}</b>\n\n💋 私密放送 · 今夜只为懂的人\n✨ 主演：{actor}\n🏷️ 标签：{class} / {tags}\n⏱️ 时长：{duration}\n⭐ 推荐：{rating}\n\n📝 {intro}\n\n#AV精选 #今夜必看"
	for _, category := range []string{"成人", "adult"} {
		if current := strings.TrimSpace(formats[category]); current == legacyAdult || current == previousAdult {
			formats[category] = defaults[category]
		}
	}
	for category, format := range defaults {
		if strings.TrimSpace(formats[category]) == "" {
			formats[category] = format
		}
	}
	return formats
}

func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (c *Config) VideoFormatFor(category string) string {
	for _, key := range channelKeys(category) {
		if format := strings.TrimSpace(c.VideoFormats[key]); format != "" {
			return format
		}
	}
	if format := strings.TrimSpace(c.VideoFormat); format != "" {
		return c.VideoFormat
	}
	return Default().VideoFormat
}

// PickChannel returns a random channel for the given category. Falls back to "default" or legacy fields.
func (c *Config) PickChannel(category string) int64 {
	ids := c.ChannelsFor(category)
	if len(ids) == 0 {
		return 0
	}
	return ids[rand.Intn(len(ids))]
}

// ChannelsFor returns all channel IDs for a given category.
func (c *Config) ChannelsFor(category string) []int64 {
	if len(c.ChannelMap) > 0 {
		for _, key := range channelKeys(category) {
			if ids, ok := c.ChannelMap[key]; ok && len(ids) > 0 {
				return ids
			}
		}
		if ids, ok := c.ChannelMap["default"]; ok && len(ids) > 0 {
			return ids
		}
		var only []int64
		for _, ids := range c.ChannelMap {
			if len(ids) != 1 {
				only = nil
				break
			}
			if len(only) > 0 {
				only = nil
				break
			}
			only = ids
		}
		if len(only) == 1 {
			return only
		}
	}
	if len(c.ChannelIDs) > 0 {
		return c.ChannelIDs
	}
	if c.ChannelID != 0 {
		return []int64{c.ChannelID}
	}
	return nil
}

func channelKeys(category string) []string {
	category = strings.TrimSpace(strings.ToLower(category))
	switch category {
	case "adult", "成人":
		return []string{"adult", "成人"}
	case "movie", "电影", "电影资源站":
		return []string{"movie", "电影", "电影资源站"}
	case "tv", "电视剧", "电视":
		return []string{"tv", "电视剧", "电视"}
	case "anime", "动漫", "动画":
		return []string{"anime", "动漫", "动画"}
	case "variety", "综艺":
		return []string{"variety", "综艺"}
	case "documentary", "纪录片":
		return []string{"documentary", "纪录片"}
	case "default", "综合影视":
		return []string{"default", "综合影视"}
	default:
		return []string{category}
	}
}

// HasAnyChannel returns true if any channel is configured.
func (c *Config) HasAnyChannel() bool {
	if len(c.ChannelMap) > 0 {
		return true
	}
	if len(c.ChannelIDs) > 0 || c.ChannelID != 0 {
		return true
	}
	return false
}
