package video

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasComplete(t *testing.T) {
	dir := t.TempDir()
	downloader := New(dir)
	title := "已完成视频"
	path := filepath.Join(dir, sanitize(title)+".full.mp4")

	if downloader.HasComplete(title) {
		t.Fatal("empty cache unexpectedly reported as complete")
	}
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !downloader.HasComplete(title) {
		t.Fatal("finalized cache was not detected")
	}
}
