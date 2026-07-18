package poster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
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
	threadID  int // message_thread_id for forum topics (0 = no topic)
}

func New(token string, channelID int64) *Poster {
	return &Poster{
		token:     token,
		channelID: channelID,
		client:    &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *Poster) PostStation(st *store.Station, format string, action string) (int, error) {
	return p.sendMessage(p.formatStation(st, format, action), "Markdown")
}

func (p *Poster) PostSimple(text string) (int, error) {
	return p.sendMessage(text, "")
}

// PostHTML sends HTML-formatted text.
func (p *Poster) PostHTML(text string) (int, error) {
	return p.sendMessage(text, "HTML")
}

// PostVideo uploads a local video file with caption.
func (p *Poster) PostVideo(filePath, caption string) (int, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("open video: %w", err)
	}
	defer file.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	w.WriteField("chat_id", fmt.Sprintf("%d", p.channelID))
	w.WriteField("caption", caption)
	w.WriteField("parse_mode", "HTML")
	w.WriteField("supports_streaming", "true")
	if p.threadID > 0 {
		w.WriteField("message_thread_id", fmt.Sprintf("%d", p.threadID))
	}

	part, _ := w.CreateFormFile("video", filepath.Base(filePath))
	io.Copy(part, file)
	w.Close()

	url := tgAPI + p.token + "/sendVideo"
	req, _ := http.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("sendVideo HTTP %d: %s", resp.StatusCode, string(respData))
	}

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.Unmarshal(respData, &result)
	if !result.OK {
		return 0, fmt.Errorf("sendVideo failed: %s", string(respData))
	}
	return result.Result.MessageID, nil
}

// PostPhoto sends a photo with caption.
func (p *Poster) PostPhoto(photoURL, caption string) (int, error) {
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

// PostContentDigest sends a text list with play links.
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
			parts := strings.SplitN(item.Episodes[0], "$", 2)
			if len(parts) == 2 {
				sb.WriteString(fmt.Sprintf("   🎬 <a href=\"%s\">%s</a>\n", parts[1], parts[0]))
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
	return p.sendMessage(sb.String(), "HTML")
}

func formatVideoCaption(item content.ContentItem) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>%s</b>", escapeHTML(item.Title)))
	if item.TypeName != "" {
		sb.WriteString(fmt.Sprintf(" | %s", item.TypeName))
	}
	sb.WriteString(fmt.Sprintf("\n📡 %s", item.Source))
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
	if p.threadID > 0 {
		body["message_thread_id"] = p.threadID
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
