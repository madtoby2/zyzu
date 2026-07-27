package poster

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/madtoby2/zyzu/internal/content"
	"github.com/madtoby2/zyzu/internal/store"
)

type Poster struct {
	pickChannel func(cat string) int64
	client      *http.Client
}

// Telethon uses SQLite-backed session files. Serialize bridge invocations so
// login requests cannot race with uploads/status checks on the same session.
var telethonMu sync.Mutex

func TelethonAction(action string, phone, code, password string) ([]byte, error) {
	telethonMu.Lock()
	defer telethonMu.Unlock()
	python := os.Getenv("PYTHON_BIN")
	if python == "" {
		if _, err := os.Stat("/opt/zyzu/.venv/bin/python"); err == nil {
			python = "/opt/zyzu/.venv/bin/python"
		} else {
			python = "python3"
		}
	}
	payload := fmt.Sprintf(`{"action":%q,"phone":%q,"code":%q,"password":%q}`, action, phone, code, password)
	cmd := exec.Command(python, "internal/poster/telethon_bridge.py")
	cmd.Stdin = strings.NewReader(payload)
	return cmd.CombinedOutput()
}

func New(pick func(string) int64) *Poster {
	return &Poster{
		pickChannel: pick,
		client:      &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *Poster) PostStation(st *store.Station, format string, action string) (int, error) {
	return p.sendTelethon(p.StationMessage(st, format, action), p.pickChannel("default"))
}

// StationMessage returns the exact text sent for a station announcement.
func (p *Poster) StationMessage(st *store.Station, format string, action string) string {
	return p.formatStation(st, format, action)
}

func (p *Poster) PostSimple(text string) (int, error) {
	return p.sendTelethon(text, p.pickChannel("default"))
}

func (p *Poster) PostHTML(text string) (int, error) {
	return p.sendTelethon(text, p.pickChannel("default"))
}

func (p *Poster) sendTelethon(text string, chatID int64) (int, error) {
	telethonMu.Lock()
	defer telethonMu.Unlock()
	python := os.Getenv("PYTHON_BIN")
	if python == "" {
		if _, err := os.Stat("/opt/zyzu/.venv/bin/python"); err == nil {
			python = "/opt/zyzu/.venv/bin/python"
		} else {
			python = "python3"
		}
	}
	cmd := exec.Command(python, "internal/poster/telethon_bridge.py")
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"chat_id":%d,"text":%q}`, chatID, text))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("telethon: %s", strings.TrimSpace(string(out)))
	}
	var result struct {
		MessageID int    `json:"message_id"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return 0, err
	}
	if result.Error != "" {
		return 0, errors.New(result.Error)
	}
	return result.MessageID, nil
}

func (p *Poster) PostVideo(filePath, caption, category string, coverURL string) (int, error) {
	if _, err := os.Stat(filePath); err != nil {
		return 0, fmt.Errorf("open video: %w", err)
	}
	thumbPath := ""
	if coverURL != "" {
		thumbPath = filePath + ".jpg"
		if err := downloadCover(coverURL, thumbPath); err != nil {
			thumbPath = ""
		}
		defer os.Remove(thumbPath)
	}
	return p.sendTelethonVideo(filePath, caption, p.pickChannel(category), thumbPath)
	/* req, _ := http.NewRequest("POST", tgAPI+p.token+"/sendVideo", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.Unmarshal(respData, &result)
	if !result.OK {
		return 0, fmt.Errorf("sendVideo failed")
	}
	return result.Result.MessageID, nil */
}

func (p *Poster) sendTelethonVideo(filePath, caption string, chatID int64, thumbPath string) (int, error) {
	telethonMu.Lock()
	defer telethonMu.Unlock()
	python := os.Getenv("PYTHON_BIN")
	if python == "" {
		if _, err := os.Stat("/opt/zyzu/.venv/bin/python"); err == nil {
			python = "/opt/zyzu/.venv/bin/python"
		} else {
			python = "python3"
		}
	}
	payload := fmt.Sprintf(`{"action":"upload_video","chat_id":%d,"file_path":%q,"caption":%q,"thumb_path":%q}`, chatID, filePath, caption, thumbPath)
	cmd := exec.Command(python, "internal/poster/telethon_bridge.py")
	cmd.Stdin = strings.NewReader(payload)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("telethon video: %s", strings.TrimSpace(string(out)))
	}
	var result struct {
		MessageID int    `json:"message_id"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return 0, err
	}
	if result.Error != "" {
		return 0, errors.New(result.Error)
	}
	return result.MessageID, nil
}

func downloadCover(rawURL, path string) error {
	resp, err := http.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cover HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.ReadFrom(resp.Body)
	return err
}

func (p *Poster) PostPhoto(photoURL, caption, category string) (int, error) {
	body := map[string]interface{}{
		"chat_id":    p.pickChannel(category),
		"photo":      photoURL,
		"caption":    caption,
		"parse_mode": "HTML",
	}
	_, _ = json.Marshal(body)
	return 0, errors.New("Telethon photo upload bridge not implemented")
	/* req, _ := http.NewRequest("POST", tgAPI+p.token+"/sendPhoto", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.Unmarshal(respData, &result)
	return result.Result.MessageID, nil */
}

func (p *Poster) PostContentDigest(items []content.ContentItem, title, category string) (int, error) {
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
	return p.sendMessage(sb.String(), "HTML", p.pickChannel(category))
}

func (p *Poster) PostContentSplit(items []content.ContentItem) int {
	posted := 0
	for _, item := range items {
		cat := item.Category
		if cat == "" {
			cat = "default"
		}
		caption := fmt.Sprintf("<b>%s</b>", escapeHTML(item.Title))
		if item.TypeName != "" {
			caption += fmt.Sprintf(" | %s", item.TypeName)
		}
		caption += fmt.Sprintf("\n📡 %s", item.Source)
		for _, ep := range item.Episodes {
			parts := strings.SplitN(ep, "$", 2)
			if len(parts) == 2 {
				caption += fmt.Sprintf("\n🎬 <a href=\"%s\">%s</a>", parts[1], parts[0])
			}
		}
		if item.CoverURL != "" {
			_, err := p.PostPhoto(item.CoverURL, caption, cat)
			if err == nil {
				posted++
				time.Sleep(1500 * time.Millisecond)
				continue
			}
		}
		p.sendMessage(caption, "HTML", p.pickChannel(cat))
		posted++
		time.Sleep(time.Second)
	}
	return posted
}

func (p *Poster) PostVideoSplit(items []content.ContentItem) int {
	posted := 0
	for _, item := range items {
		if len(item.Episodes) == 0 {
			continue
		}
		parts := strings.SplitN(item.Episodes[0], "$", 2)
		if len(parts) != 2 {
			continue
		}
		cat := item.Category
		if cat == "" {
			cat = "default"
		}
		caption := fmt.Sprintf("<b>%s</b>", escapeHTML(item.Title))
		if item.TypeName != "" {
			caption += fmt.Sprintf(" | %s", item.TypeName)
		}
		caption += fmt.Sprintf("\n📡 %s", item.Source)
		_, err := p.PostVideo("", caption, cat, "") // placeholder — scheduler handles download
		_ = err
		posted++
		time.Sleep(2 * time.Second)
	}
	return posted
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

func (p *Poster) sendMessage(text string, parseMode string, chatID int64) (int, error) {
	return p.sendTelethon(text, chatID)
	/*
	   	if chatID == 0 {
	   		return 0, nil
	   	}

	   	body := map[string]interface{}{
	   		"chat_id": chatID,
	   		"text":    text,
	   	}

	   	if parseMode != "" {
	   		body["parse_mode"] = parseMode
	   		body["disable_web_page_preview"] = true
	   	}

	   data, _ := json.Marshal(body)
	   req, _ := http.NewRequest("POST", tgAPI+p.token+"/sendMessage", bytes.NewReader(data))
	   req.Header.Set("Content-Type", "application/json")
	   resp, err := p.client.Do(req)

	   	if err != nil {
	   		return 0, err
	   	}

	   defer resp.Body.Close()
	   respData, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	   	var result struct {
	   		OK     bool `json:"ok"`
	   		Result struct {
	   			MessageID int `json:"message_id"`
	   		} `json:"result"`
	   	}

	   json.Unmarshal(respData, &result)

	   	if !result.OK {
	   		return 0, fmt.Errorf("TG API error")
	   	}

	   return result.Result.MessageID, nil
	*/
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
