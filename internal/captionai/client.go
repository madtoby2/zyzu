package captionai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/madtoby2/zyzu/internal/content"
)

type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

func New(baseURL, apiKey, model string) *Client {
	return &Client{BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), APIKey: strings.TrimSpace(apiKey), Model: strings.TrimSpace(model), HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) Enabled() bool {
	return c != nil && c.BaseURL != "" && c.APIKey != "" && c.Model != ""
}

func (c *Client) Generate(ctx context.Context, item content.ContentItem, category string) (string, error) {
	if !c.Enabled() {
		return "", nil
	}
	prompt := fmt.Sprintf("频道：%s\n标题：%s\n演员：%s\n分类：%s\n标签：%s\n简介：%s", category, item.Title, item.Actor, item.Class, item.TypeName, item.Intro)
	payload := map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "你是成人影视频道的中文文案编辑。内容只涉及成年人。根据资料写一句20到45字、暧昧诱人但不低俗的推荐语。不要复述标题，不要解释，不要加引号、标签或换行。"},
			{"role": "user", "content": prompt},
		},
		"temperature": 0.85,
		"max_tokens":  800,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("caption AI HTTP %d", resp.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("caption AI returned no choices")
	}
	text := strings.Trim(strings.TrimSpace(result.Choices[0].Message.Content), "\"'“”‘’ ")
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "", fmt.Errorf("caption AI returned empty content")
	}
	runes := []rune(text)
	if len(runes) > 120 {
		text = string(runes[:120])
	}
	return text, nil
}
