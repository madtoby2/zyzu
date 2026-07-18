package poster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/madtoby2/zyzu/internal/content"
	"github.com/madtoby2/zyzu/internal/store"
)

const tgAPI = "https://api.telegram.org/bot"

type Poster struct {
	token     string
	channelID int64
	client    *http.Client
}

func New(token string, channelID int64) *Poster {
	return &Poster{
		token:     token,
		channelID: channelID,
		client:    &http.Client{Timeout: 25 * time.Second},
	}
}

func (p *Poster) PostStation(st *store.Station, format string, action string) (int, error) {
	return p.sendMessage(p.formatStation(st, format, action), "Markdown")
}

func (p *Poster) PostSimple(text string) (int, error) {
	return p.sendMessage(text, "")
}

// PostContent sends a single item: cover photo + title + play link.
func (p *Poster) PostContent(item content.ContentItem) (int, error) {
	caption := formatContent(item)
	if item.CoverURL != "" {
		id, err := p.sendPhoto(item.CoverURL, caption)
		if err == nil {
			time.Sleep(500 * time.Millisecond)
			return id, nil
		}
	}
	return p.sendMessage(caption, "HTML")
}

// PostContentDigest sends compact list with play links.
func (p *Poster) PostContentDigest(items []content.ContentItem, title string) (int, error) {
	var sb strings.Builder
	sb.WriteString("<b>")
	sb.WriteString(title)
	sb.WriteString("</b>\n\n")

	for i, item := range items {
		if i >= 15 {
			break
		}
		sb.WriteString(fmt.Sprintf("%d. <b>%s</b>", i+1, escapeHTML(item.Title)))
		if item.TypeName != "" {
			sb.WriteString(fmt.Sprintf(" [%s]", item.TypeName))
		}
		sb.WriteString("\n")

		if len(item.Episodes) > 0 {
			// Show first episode with playable link
			ep := item.Episodes[0]
			parts := strings.SplitN(ep, "$", 2)
			if len(parts) == 2 {
				sb.WriteString(fmt.Sprintf("   🎬 <a href=\"%s\">%s</a>\n", parts[1], parts[0]))
			}
			if len(item.Episodes) > 1 {
				sb.WriteString(fmt.Sprintf("   📺 共%d集 | %s\n", len(item.Episodes), item.Source))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("📊 共%d条 · ", len(items)))
	seen := map[string]bool{}
	sources := []string{}
	for _, item := range items {
		if !seen[item.Source] {
			sources = append(sources, item.Source)
			seen[item.Source] = true
		}
	}
	sb.WriteString(strings.Join(sources, " · "))

	// Send as plain text with HTML parse mode
	return p.sendMessage(sb.String(), "HTML")
}

// PostContentSplit posts items individually with photo + play link (for video playback mode).
func (p *Poster) PostContentSplit(items []content.ContentItem) (int, error) {
	count := 0
	limit := 10
	if len(items) < limit {
		limit = len(items)
	}
	for _, item := range items[:limit] {
		_, err := p.PostContent(item)
		if err != nil {
			continue
		}
		count++
		time.Sleep(1500 * time.Millisecond) // TG rate limit
	}
	return count, nil
}

func formatContent(item content.ContentItem) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>%s</b>", escapeHTML(item.Title)))
	if item.TypeName != "" {
		sb.WriteString(fmt.Sprintf(" | %s", item.TypeName))
	}
	sb.WriteString(fmt.Sprintf("\n📡 %s\n", item.Source))

	for _, ep := range item.Episodes {
		parts := strings.SplitN(ep, "$", 2)
		if len(parts) == 2 {
			sb.WriteString(fmt.Sprintf("🎬 <a href=\"%s\">%s</a>\n", parts[1], parts[0]))
		}
	}
	return sb.String()
}

func (p *Poster) formatStation(st *store.Station, format string, action string) string {
	msg := format
	msg = strings.ReplaceAll(msg, "{name}", escapeMD(st.Name))
	msg = strings.ReplaceAll(msg, "{category}", st.Category)
	msg = strings.ReplaceAll(msg, "{api_url}", st.APIURL)
	msg = strings.ReplaceAll(msg, "{availability}", st.Availability)
	msg = strings.ReplaceAll(msg, "{resource_count}", st.ResourceCount)
	msg = strings.ReplaceAll(msg, "{response_time}", st.ResponseTime)
	msg = strings.ReplaceAll(msg, "{interface_type}", st.InterfaceType)
	msg = strings.ReplaceAll(msg, "{description}", st.Description)

	var tags []string
	json.Unmarshal([]byte(st.Tags), &tags)
	msg = strings.ReplaceAll(msg, "{tags}", strings.Join(tags, " · "))

	switch action {
	case "new":
		msg = "🆕 新站上线\n" + msg
	case "update":
		msg = "🔄 站点更新\n" + msg
	}
	return msg
}

func (p *Poster) sendMessage(text string, parseMode string) (int, error) {
	body := map[string]interface{}{
		"chat_id": p.channelID,
		"text":    text,
	}
	if parseMode != "" {
		body["parse_mode"] = parseMode
		body["disable_web_page_preview"] = true
	}
	data, _ := json.Marshal(body)

	url := tgAPI + p.token + "/sendMessage"
	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("TG API %d: %s", resp.StatusCode, string(respData))
	}

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respData, &result); err != nil {
		return 0, err
	}
	if !result.OK {
		return 0, fmt.Errorf("TG API error: %s", string(respData))
	}
	return result.Result.MessageID, nil
}

func (p *Poster) sendPhoto(photoURL, caption string) (int, error) {
	body := map[string]interface{}{
		"chat_id":    p.channelID,
		"photo":      photoURL,
		"caption":    caption,
		"parse_mode": "HTML",
	}
	data, _ := json.Marshal(body)

	url := tgAPI + p.token + "/sendPhoto"
	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("sendPhoto HTTP %d: %s", resp.StatusCode, string(respData))
	}

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.Unmarshal(respData, &result)
	return result.Result.MessageID, nil
}

func formatContentCaption(item content.ContentItem) string {
	return formatContent(item)
}

func escapeMD(s string) string {
	return strings.NewReplacer(
		"_", "\\_", "*", "\\*", "[", "\\[", "]", "\\]",
		"(", "\\(", ")", "\\)", "~", "\\~", "`", "\\`",
		">", "\\>", "#", "\\#", "+", "\\+", "-", "\\-",
		"=", "\\=", "|", "\\|", "{", "\\{", "}", "\\}",
		".", "\\.", "!", "\\!",
	).Replace(s)
}

func escapeHTML(s string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;", "&", "&amp;").Replace(s)
}
