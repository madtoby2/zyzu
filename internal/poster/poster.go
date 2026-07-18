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
		client:    &http.Client{Timeout: 20 * time.Second},
	}
}

// PostStation formats and sends a station to the channel.
func (p *Poster) PostStation(st *store.Station, format string, action string) (int, error) {
	return p.sendMessage(p.formatStation(st, format, action), "Markdown")
}

// PostSimple sends a plain text message.
func (p *Poster) PostSimple(text string) (int, error) {
	return p.sendMessage(text, "")
}

// PostContent sends a single content item with cover photo.
func (p *Poster) PostContent(item content.ContentItem) (int, error) {
	caption := formatContentCaption(item)
	if item.CoverURL != "" {
		id, err := p.sendPhoto(item.CoverURL, caption)
		if err == nil {
			return id, nil
		}
		// Fallback to text-only
	}
	return p.sendMessage(caption, "HTML")
}

// PostContentDigest sends a batch as a text digest.
func (p *Poster) PostContentDigest(items []content.ContentItem, title string) (int, error) {
	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString("\n\n")

	for i, item := range items {
		if i >= 20 {
			break
		}
		sb.WriteString(fmt.Sprintf("%d. <b>%s</b>", i+1, escapeHTML(item.Title)))
		if item.TypeName != "" {
			sb.WriteString(fmt.Sprintf(" [%s]", item.TypeName))
		}
		sb.WriteString("\n")
		if len(item.Episodes) > 0 {
			ep := item.Episodes[0]
			if idx := strings.Index(ep, "$"); idx > 0 {
				sb.WriteString(fmt.Sprintf("   🎬 %s\n", ep[:idx]))
			}
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("\n📊 共 %d 条，来源: ", len(items)))
	seen := map[string]bool{}
	for _, item := range items {
		if !seen[item.Source] {
			sb.WriteString(fmt.Sprintf("%s ", item.Source))
			seen[item.Source] = true
		}
	}

	return p.sendMessage(sb.String(), "HTML")
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
		"chat_id":   p.channelID,
		"photo":     photoURL,
		"caption":   caption,
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
		return 0, err
	}

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.Unmarshal(respData, &result)
	if !result.OK {
		return 0, fmt.Errorf("TG sendPhoto failed")
	}
	return result.Result.MessageID, nil
}

func formatContentCaption(item content.ContentItem) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>%s</b>", escapeHTML(item.Title)))
	if item.TypeName != "" {
		sb.WriteString(fmt.Sprintf(" | %s", item.TypeName))
	}
	sb.WriteString(fmt.Sprintf("\n📡 %s", item.Source))
	if len(item.Episodes) > 0 {
		sb.WriteString(fmt.Sprintf("\n📺 %d集", len(item.Episodes)))
	}
	return sb.String()
}

func escapeMD(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_", "*", "\\*", "[", "\\[", "]", "\\]",
		"(", "\\(", ")", "\\)", "~", "\\~", "`", "\\`",
		">", "\\>", "#", "\\#", "+", "\\+", "-", "\\-",
		"=", "\\=", "|", "\\|", "{", "\\{", "}", "\\}",
		".", "\\.", "!", "\\!",
	)
	return replacer.Replace(s)
}

func escapeHTML(s string) string {
	replacer := strings.NewReplacer("<", "&lt;", ">", "&gt;", "&", "&amp;")
	return replacer.Replace(s)
}
