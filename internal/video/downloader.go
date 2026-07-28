package video

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Downloader converts m3u8 to mp4 via ffmpeg.
type Downloader struct {
	WorkDir string
	Timeout int // optional seconds per download; zero means full duration
}

func New(workDir string) *Downloader {
	os.MkdirAll(workDir, 0755)
	return &Downloader{WorkDir: workDir, Timeout: 0}
}

// Download converts an m3u8 URL to an mp4 file. Returns the local file path.
func (d *Downloader) Download(m3u8URL, filename string) (string, error) {
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
		"-timeout", "30000000", // 30s socket timeout (microseconds)
		"-allowed_extensions", "ALL", // some HLS providers disguise media as images
		"-allowed_segment_extensions", "ALL",
		"-extension_picky", "0",
		"-i", m3u8URL,
		"-c", "copy", // stream copy (fast, no re-encode)
		"-bsf:a", "aac_adtstoasc",
		"-movflags", "+faststart",
		tmpPath,
	}
	if d.Timeout > 0 {
		args = append(args[:len(args)-1], "-t", fmt.Sprintf("%d", d.Timeout), args[len(args)-1])
	}

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = nil
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Clean up partial file
		os.Remove(tmpPath)
		return "", fmt.Errorf("ffmpeg: %v: %s", err, string(output))
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

	log.Printf("[video] downloaded %s -> %s (%.1fMB)", m3u8URL[:60], filename, float64(info.Size())/1024/1024)
	return outPath, nil
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
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
