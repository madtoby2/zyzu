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

func TestSanitizeKeepsUnicodeTitlesUnique(t *testing.T) {
	first := sanitize("不喜欢色色的我吗？")
	second := sanitize("教我如何不想“她”")
	if first == second {
		t.Fatalf("different titles produced the same cache name %q", first)
	}
}
