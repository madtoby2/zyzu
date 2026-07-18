package config

import (
	"encoding/json"
	"math/rand"
	"os"
)

type Config struct {
	BotToken     string             `json:"bot_token"`
	ChannelIDs   []int64            `json:"channel_ids"`   // deprecated
	ChannelID    int64              `json:"channel_id"`    // deprecated
	ChannelMap   map[string][]int64 `json:"channel_map"`   // {"adult":[-1001], "movie":[-1002], "default":[-1000]}
	APIKey       string             `json:"api_key"`
	ScrapeCron   string             `json:"scrape_cron"`
	ContentCron  string             `json:"content_cron"`
	ListenAddr   string             `json:"listen_addr"`
	PostFormat   string             `json:"post_format"`
	ContentMode  string             `json:"content_mode"`
	ContentLimit int                `json:"content_limit"`
}

func Default() *Config {
	return &Config{
		ScrapeCron:   "0 0 */6 * * *",
		ContentCron:  "0 8,20 * * *",
		ListenAddr:   ":8080",
		ContentMode:  "video",
		ContentLimit: 10,
		PostFormat:   "📡 *{name}*  |  {availability}  |  {resource_count}条  |  {response_time}\n🏷 {tags}\n🔗 `{api_url}`",
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
	return cfg, nil
}

func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
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
		if ids, ok := c.ChannelMap[category]; ok && len(ids) > 0 {
			return ids
		}
		if ids, ok := c.ChannelMap["default"]; ok && len(ids) > 0 {
			return ids
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
