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
	Timeout int // seconds per download
}

func New(workDir string) *Downloader {
	os.MkdirAll(workDir, 0755)
	return &Downloader{WorkDir: workDir, Timeout: 120}
}

// Download converts an m3u8 URL to an mp4 file. Returns the local file path.
func (d *Downloader) Download(m3u8URL, filename string) (string, error) {
	// Sanitize filename
	filename = sanitize(filename)
	outPath := filepath.Join(d.WorkDir, filename+".mp4")

	// Skip if already exists
	if _, err := os.Stat(outPath); err == nil {
		return outPath, nil
	}

	// ffmpeg: download and convert
	args := []string{
		"-y",                    // overwrite
		"-loglevel", "error",    // quiet
		"-timeout", "30000000",  // 30s socket timeout (microseconds)
		"-i", m3u8URL,
		"-c", "copy",            // stream copy (fast, no re-encode)
		"-bsf:a", "aac_adtstoasc",
		"-movflags", "+faststart",
		"-t", fmt.Sprintf("%d", d.Timeout), // max duration
		outPath,
	}

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = nil
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Clean up partial file
		os.Remove(outPath)
		return "", fmt.Errorf("ffmpeg: %v: %s", err, string(output))
	}

	info, err := os.Stat(outPath)
	if err != nil || info.Size() == 0 {
		return "", fmt.Errorf("output file empty or missing")
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
