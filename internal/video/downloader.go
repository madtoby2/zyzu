package video

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Downloader converts m3u8 to mp4 via ffmpeg.
type Downloader struct {
	WorkDir        string
	Timeout        int           // optional media seconds per download; zero means full duration
	RuntimeTimeout time.Duration // wall-clock cap for the ffmpeg process
	StallTimeout   time.Duration // abort when the media timestamp stops advancing
}

func New(workDir string) *Downloader {
	os.MkdirAll(workDir, 0755)
	return &Downloader{WorkDir: workDir, Timeout: 0, RuntimeTimeout: 6 * time.Hour, StallTimeout: 3 * time.Minute}
}

// HasComplete reports whether a fully finalized file can be resumed for upload.
func (d *Downloader) HasComplete(filename string) bool {
	path := filepath.Join(d.WorkDir, sanitize(filename)+".full.mp4")
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// Download converts an m3u8 URL to an mp4 file. Returns the local file path.
func (d *Downloader) Download(m3u8URL, filename string) (string, error) {
	resolvedURL, resolveErr := resolvePlayableURL(m3u8URL)
	if resolveErr != nil {
		return "", resolveErr
	}
	m3u8URL = resolvedURL
	// Sanitize filename
	filename = sanitize(filename)
	// Use a new cache namespace so files created by the former 120s cap are
	// never mistaken for complete downloads.
	outPath := filepath.Join(d.WorkDir, filename+".full.mp4")
	tmpPath := outPath + ".part.mp4"

	// Skip if already exists
	if _, err := os.Stat(outPath); err == nil {
		return outPath, nil
	}
	_ = os.Remove(tmpPath)

	// ffmpeg: download and convert
	args := []string{
		"-y",                 // overwrite
		"-loglevel", "error", // quiet
		"-nostats",
		"-progress", "pipe:1",
		"-timeout", "30000000", // 30s socket timeout (microseconds)
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "5",
		"-allowed_extensions", "ALL", // some HLS providers disguise media as images
		"-allowed_segment_extensions", "ALL",
		"-extension_picky", "0",
		"-user_agent", "Mozilla/5.0",
		"-headers", fmt.Sprintf("Referer: %s\r\n", mediaReferer(m3u8URL)),
		"-i", m3u8URL,
		"-c", "copy", // stream copy (fast, no re-encode)
		"-bsf:a", "aac_adtstoasc",
		"-movflags", "+faststart",
		tmpPath,
	}
	if d.Timeout > 0 {
		args = append(args[:len(args)-1], "-t", fmt.Sprintf("%d", d.Timeout), args[len(args)-1])
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if d.RuntimeTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, d.RuntimeTimeout)
		defer cancel()
	}
	processCtx, stopProcess := context.WithCancel(ctx)
	defer stopProcess()
	cmd := exec.CommandContext(processCtx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("ffmpeg progress pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start ffmpeg: %w", err)
	}

	advanced := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		lastTimestamp := ""
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "out_time_us=") && !strings.HasPrefix(line, "out_time_ms=") {
				continue
			}
			if line == lastTimestamp {
				continue
			}
			lastTimestamp = line
			select {
			case advanced <- struct{}{}:
			default:
			}
		}
	}()
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	var stallTimer *time.Timer
	var stall <-chan time.Time
	if d.StallTimeout > 0 {
		stallTimer = time.NewTimer(d.StallTimeout)
		stall = stallTimer.C
		defer stallTimer.Stop()
	}
	for {
		select {
		case err = <-wait:
			goto finished
		case <-advanced:
			if stallTimer != nil {
				if !stallTimer.Stop() {
					select {
					case <-stallTimer.C:
					default:
					}
				}
				stallTimer.Reset(d.StallTimeout)
			}
		case <-stall:
			stopProcess()
			<-wait
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("ffmpeg stalled with no media progress for %s: %s", d.StallTimeout, strings.TrimSpace(stderr.String()))
		case <-ctx.Done():
			stopProcess()
			<-wait
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("ffmpeg timed out after %s", d.RuntimeTimeout)
		}
	}

finished:
	if err != nil {
		// Clean up partial file
		os.Remove(tmpPath)
		if ctx.Err() != nil {
			return "", fmt.Errorf("ffmpeg timed out after %s", d.RuntimeTimeout)
		}
		return "", fmt.Errorf("ffmpeg: %v: %s", err, stderr.String())
	}

	info, err := os.Stat(tmpPath)
	if err != nil || info.Size() == 0 {
		os.Remove(tmpPath)
		return "", fmt.Errorf("output file empty or missing")
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("finalize output: %w", err)
	}

	logURL := m3u8URL
	if len(logURL) > 60 {
		logURL = logURL[:60]
	}
	log.Printf("[video] downloaded %s -> %s (%.1fMB)", logURL, filename, float64(info.Size())/1024/1024)
	return outPath, nil
}

func mediaReferer(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	return parsed.Scheme + "://" + parsed.Host + "/"
}

func resolvePlayableURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty media url")
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, ".m3u8") || strings.Contains(lower, ".mp4") {
		return raw, nil
	}
	req, err := http.NewRequest("GET", raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve playable url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("resolve playable url: HTTP %d", resp.StatusCode)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "mpegurl") || strings.Contains(ct, "mp4") {
		return resp.Request.URL.String(), nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return "", err
	}
	text := string(body)
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)"url"\s*:\s*"([^"]+\.(?:m3u8|mp4)(?:\?[^"]*)?)"`),
		regexp.MustCompile(`(?i)var\s+main\s*=\s*"([^"]+\.(?:m3u8|mp4)(?:\?[^"]*)?)"`),
		regexp.MustCompile(`(?i)var\s+mp4\s*=\s*"([^"]+\.mp4(?:\?[^"]*)?)"`),
		regexp.MustCompile(`(?i)(https?://[^'"<>\\\s]+\.(?:m3u8|mp4)(?:\?[^'"<>\\\s]*)?)`),
		regexp.MustCompile(`(?i)(/[^'"<>\\\s]+\.(?:m3u8|mp4)(?:\?[^'"<>\\\s]*)?)`),
	} {
		if match := re.FindStringSubmatch(text); len(match) > 1 {
			resolved, err := resp.Request.URL.Parse(strings.ReplaceAll(match[1], `\/`, `/`))
			if err == nil {
				return resolved.String(), nil
			}
		}
	}
	return "", fmt.Errorf("resolved page is not a direct playable media url: %s", raw)
}

// Cleanup removes files older than maxAge.
func (d *Downloader) Cleanup(keep int) {
	entries, _ := os.ReadDir(d.WorkDir)
	if len(entries) <= keep {
		return
	}
	// Keep the newest `keep` files
	for i := 0; i < len(entries)-keep; i++ {
		os.Remove(filepath.Join(d.WorkDir, entries[i].Name()))
	}
}

func sanitize(s string) string {
	original := s
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
	if s == "" {
		s = "video"
	}
	sum := sha256.Sum256([]byte(original))
	suffix := fmt.Sprintf("-%x", sum[:4])
	maxBase := 80 - len(suffix)
	if len(s) > maxBase {
		s = s[:maxBase]
	}
	return s + suffix
}
