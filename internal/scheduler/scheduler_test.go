package scheduler

import (
	"testing"

	"github.com/madtoby2/zyzu/internal/config"
	"github.com/madtoby2/zyzu/internal/content"
)

func TestSelectRoutableItemsBalancesConfiguredChannels(t *testing.T) {
	cfg := &config.Config{ChannelMap: map[string][]int64{
		"成人":  {1},
		"电影":  {2},
		"电视剧": {3},
	}}
	items := []content.ContentItem{
		{Title: "adult-1", Category: "adult"},
		{Title: "adult-2", Category: "adult"},
		{Title: "adult-3", Category: "adult"},
		{Title: "tv-1", Category: "tv"},
		{Title: "movie-1", Category: "movie"},
		{Title: "anime-1", Category: "anime"},
	}

	got := selectRoutableItems(items, 3, cfg)
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
	want := []string{"adult-1", "tv-1", "movie-1"}
	for i := range want {
		if got[i].Title != want[i] {
			t.Errorf("item %d = %q, want %q", i, got[i].Title, want[i])
		}
	}
}
