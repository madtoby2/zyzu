package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	BotToken    string `json:"bot_token"`
	ChannelID   int64  `json:"channel_id"`
	ScrapeCron  string `json:"scrape_cron"`
	ContentCron string `json:"content_cron"`
	ListenAddr  string `json:"listen_addr"`
	PostFormat  string `json:"post_format"`
	ContentMode string `json:"content_mode"` // "digest" or "split"
	ContentLimit int   `json:"content_limit"` // max items per run
}

func Default() *Config {
	return &Config{
		ScrapeCron:   "0 */6 * * *",
		ContentCron:  "0 8,20 * * *",
		ListenAddr:   ":8080",
		ContentMode:  "video",
		ContentLimit: 10,
		PostFormat: `📡 *{name}*  |  {availability}  |  {resource_count}条  |  {response_time}
🏷 {tags}
🔗 ` + "`{api_url}`",
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
