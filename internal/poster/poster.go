package poster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
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

func (p *Poster) PostToChannel(text string, chatID int64) (int, error) {
	if chatID == 0 {
		return 0, errors.New("channel is not configured")
	}
	if strings.TrimSpace(text) == "" {
		return 0, errors.New("scheduled message is empty")
	}
	return p.sendTelethonRequest(text, chatID, true)
}

func (p *Poster) UpsertPinnedDirectory(text string, chatID int64, messageID int) (int, error) {
	if chatID == 0 || strings.TrimSpace(text) == "" {
		return 0, errors.New("directory channel or text is empty")
	}
	action := "create_directory"
	if messageID > 0 {
		action = "edit_directory"
	}
	result, err := p.telethonJSON(map[string]interface{}{
		"action": action, "chat_id": chatID, "text": text, "message_id": messageID,
	})
	if err == nil || messageID == 0 {
		return result, err
	}
	// A directory may be manually deleted while its message ID remains in
	// SQLite. Recreate it so future episode updates recover automatically.
	return p.telethonJSON(map[string]interface{}{
		"action": "create_directory", "chat_id": chatID, "text": text,
	})
}

func (p *Poster) RecreatePinnedDirectory(text string, chatID int64, messageID int) (int, error) {
	if chatID == 0 || strings.TrimSpace(text) == "" {
		return 0, errors.New("directory channel or text is empty")
	}
	return p.telethonJSON(map[string]interface{}{
		"action": "recreate_directory", "chat_id": chatID, "text": text, "message_id": messageID,
	})
}

func (p *Poster) telethonJSON(payload interface{}) (int, error) {
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
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(python, "internal/poster/telethon_bridge.py")
	cmd.Stdin = bytes.NewReader(data)
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

func (p *Poster) sendTelethon(text string, chatID int64) (int, error) {
	return p.sendTelethonRequest(text, chatID, false)
}

func (p *Poster) sendTelethonRequest(text string, chatID int64, plainText bool) (int, error) {
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
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"chat_id":%d,"text":%q,"plain_text":%t}`, chatID, text, plainText))
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

func (p *Poster) PostVideo(filePath, caption, category string, coverURL string, embedCover bool) (int, error) {
	if _, err := os.Stat(filePath); err != nil {
		return 0, fmt.Errorf("open video: %w", err)
	}
	chatID := p.pickChannel(category)
	if chatID == 0 {
		return 0, fmt.Errorf("no channel configured for category %q", category)
	}
	coverPath := ""
	if embedCover && coverURL != "" {
		var coverErr error
		coverPath, coverErr = prepareCover(coverURL, filePath)
		if coverErr != nil {
			log.Printf("[cover] 资源站封面处理失败，仅发送视频: url=%s error=%v", coverURL, coverErr)
		}
	}
	thumbPath, thumbErr := prepareVideoThumbnail(filePath)
	if thumbErr != nil {
		log.Printf("[cover] 视频预览截帧失败，继续上传视频: %v", thumbErr)
	}
	if coverPath != "" {
		defer os.Remove(coverPath)
	}
	if thumbPath != "" {
		defer os.Remove(thumbPath)
	}
	return p.sendTelethonVideo(filePath, caption, chatID, coverPath, thumbPath, embedCover)
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

func (p *Poster) sendTelethonVideo(filePath, caption string, chatID int64, coverPath, thumbPath string, embedCover bool) (int, error) {
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
	payload := fmt.Sprintf(`{"action":"upload_video","chat_id":%d,"file_path":%q,"caption":%q,"cover_path":%q,"thumb_path":%q,"embed_cover":%t}`, chatID, filePath, caption, coverPath, thumbPath, embedCover)
	timeout := 90 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("ZYZU_UPLOAD_TIMEOUT_MINUTES")); raw != "" {
		if minutes, parseErr := strconv.Atoi(raw); parseErr == nil && minutes >= 10 {
			timeout = time.Duration(minutes) * time.Minute
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "internal/poster/telethon_bridge.py")
	cmd.Stdin = strings.NewReader(payload)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	out := stdout.Bytes()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return 0, fmt.Errorf("telethon video upload timed out after %s", timeout)
	}
	if err != nil {
		return 0, fmt.Errorf("telethon video: %s", strings.TrimSpace(string(out)))
	}
	var result struct {
		MessageID     int    `json:"message_id"`
		CoverAttached bool   `json:"cover_attached"`
		Error         string `json:"error"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return 0, err
	}
	if result.Error != "" {
		return 0, errors.New(result.Error)
	}
	if result.CoverAttached {
		log.Printf("[cover] 资源站封面已嵌入可预览视频消息: video_message_id=%d", result.MessageID)
	} else if thumbPath != "" {
		log.Printf("[cover] 封面已随视频提交: %s", thumbPath)
	}
	return result.MessageID, nil
}

func prepareCover(rawURL, videoPath string) (string, error) {
	const maxCoverBytes = 10 << 20

	rawPath := videoPath + ".cover"
	thumbPath := videoPath + ".cover.jpg"
	defer os.Remove(rawPath)
	_ = os.Remove(thumbPath)

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ZYZU/1.0)")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("cover HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(rawPath)
	if err != nil {
		return "", err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxCoverBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n == 0 {
		return "", errors.New("cover response is empty")
	}
	if n > maxCoverBytes {
		return "", fmt.Errorf("cover is larger than %d MB", maxCoverBytes>>20)
	}

	// Resource sites often return WebP/PNG data under a .jpg URL. Normalize
	// it to a regular JPEG while preserving the poster's original aspect ratio.
	size, err := renderCoverPhoto(rawPath, thumbPath)
	if err != nil {
		return "", err
	}
	log.Printf("[cover] 资源站封面已就绪: %s (%d KB)", rawURL, (size+1023)/1024)
	return thumbPath, nil
}

func renderCoverPhoto(inputPath, coverPath string) (int64, error) {
	_ = os.Remove(coverPath)
	cmd := exec.Command(
		"ffmpeg", "-y", "-loglevel", "error",
		"-i", inputPath,
		"-vf", "scale=1280:1280:force_original_aspect_ratio=decrease",
		"-frames:v", "1", "-q:v", "3",
		coverPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("convert cover: %v: %s", err, strings.TrimSpace(string(output)))
	}
	info, err := os.Stat(coverPath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func prepareVideoThumbnail(videoPath string) (string, error) {
	thumbPath := videoPath + ".thumb.jpg"
	_ = os.Remove(thumbPath)
	size, err := renderThumbnail(videoPath, thumbPath, true)
	if err != nil {
		return "", err
	}
	log.Printf("[cover] 资源站未提供封面，已截取视频画面 (%d KB)", (size+1023)/1024)
	return thumbPath, nil
}

func renderThumbnail(inputPath, thumbPath string, seekVideo bool) (int64, error) {
	qualities := []string{"5", "10", "16"}
	for _, quality := range qualities {
		_ = os.Remove(thumbPath)
		args := []string{
			"ffmpeg", "-y", "-loglevel", "error",
		}
		if seekVideo {
			args = append(args, "-ss", "00:00:05")
		}
		args = append(args,
			"-i", inputPath,
			"-vf", "scale=320:180:force_original_aspect_ratio=decrease,pad=320:180:(ow-iw)/2:(oh-ih)/2:black",
			"-frames:v", "1", "-q:v", quality,
			thumbPath,
		)
		cmd := exec.Command(args[0], args[1:]...)
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			return 0, fmt.Errorf("convert cover: %v: %s", runErr, strings.TrimSpace(string(output)))
		}
		info, statErr := os.Stat(thumbPath)
		if statErr != nil {
			return 0, statErr
		}
		if info.Size() <= 200<<10 {
			return info.Size(), nil
		}
	}

	_ = os.Remove(thumbPath)
	return 0, errors.New("converted cover is larger than Telegram's 200 KB limit")
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
		_, err := p.PostVideo("", caption, cat, "", false) // placeholder — scheduler handles download
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
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
