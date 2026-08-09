package video

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestResolvePlayableURLExtractsSharePageM3U8(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<script>var playlist = '[{"url":"/20260809/demo/2332kb/hls/index.m3u8"}]'; var main = "/20260809/demo/index.m3u8";</script>`)
	}))
	defer server.Close()
	serverURL = server.URL

	got, err := resolvePlayableURL(serverURL + "/share/demo")
	if err != nil {
		t.Fatal(err)
	}
	want := serverURL + "/20260809/demo/2332kb/hls/index.m3u8"
	if got != want {
		t.Fatalf("resolvePlayableURL() = %q, want %q", got, want)
	}
}
