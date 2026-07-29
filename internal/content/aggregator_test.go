package content

import (
	"reflect"
	"testing"
)

func TestParseEpisodesSplitsPlayerGroups(t *testing.T) {
	raw := "第01集$https://one.test/1.m3u8#第02集$https://one.test/2.m3u8$$$正片$https://two.test/movie.m3u8"
	want := []string{
		"第01集$https://one.test/1.m3u8",
		"第02集$https://one.test/2.m3u8",
		"正片$https://two.test/movie.m3u8",
	}

	if got := parseEpisodes(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEpisodes() = %#v, want %#v", got, want)
	}
}

func TestCleanIntroRemovesCMSHTML(t *testing.T) {
	raw := `<p>皇家马德里&nbsp;的传奇征战历程</p><div>第二段<br>继续</div>`
	want := "皇家马德里 的传奇征战历程 第二段 继续"
	if got := cleanIntro(raw, "标题", 1); got != want {
		t.Fatalf("cleanIntro() = %q, want %q", got, want)
	}
}

func TestNormalizeMediaURL(t *testing.T) {
	tests := map[string]string{
		"https://img.test/poster.jpg": "https://img.test/poster.jpg",
		"//cdn.test/poster.jpg":       "https://cdn.test/poster.jpg",
		"/covers/poster.jpg":          "https://api.test/covers/poster.jpg",
	}
	for raw, want := range tests {
		if got := normalizeMediaURL("https://api.test/api.php/provide/vod/", raw); got != want {
			t.Errorf("normalizeMediaURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestScalarStringAcceptsCMSNumbersAndStrings(t *testing.T) {
	tests := map[any]string{
		nil:      "",
		" 2026 ": "2026",
		8.0:      "8",
		8.25:     "8.25",
	}
	for input, want := range tests {
		if got := scalarString(input); got != want {
			t.Errorf("scalarString(%v) = %q, want %q", input, got, want)
		}
	}
}
