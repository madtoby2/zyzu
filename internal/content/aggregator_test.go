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
