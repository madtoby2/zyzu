package scheduler

import (
	"reflect"
	"testing"
	"time"

	"github.com/madtoby2/zyzu/internal/config"
	"github.com/madtoby2/zyzu/internal/content"
	"github.com/madtoby2/zyzu/internal/store"
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

func TestSelectRoutableItemsBalancesSourcesWithinCategory(t *testing.T) {
	cfg := &config.Config{ChannelMap: map[string][]int64{"电影": {1}}}
	items := []content.ContentItem{
		{Title: "source-a-1", Category: "movie", SourceURL: "https://a.test/api"},
		{Title: "source-a-2", Category: "movie", SourceURL: "https://a.test/api"},
		{Title: "source-b-1", Category: "movie", SourceURL: "https://b.test/api"},
	}

	got := selectRoutableItems(items, 3, cfg)
	want := []string{"source-a-1", "source-b-1", "source-a-2"}
	for i := range want {
		if got[i].Title != want[i] {
			t.Errorf("item %d = %q, want %q", i, got[i].Title, want[i])
		}
	}
}

func TestSelectContentSourcesIncludesConfiguredCategories(t *testing.T) {
	cfg := &config.Config{ChannelMap: map[string][]int64{
		"成人":  {1},
		"电影":  {2},
		"电视剧": {3},
	}}
	all := []store.Station{
		{Name: "fast-general", APIURL: "https://fast.test", Category: "电影资源站"},
		{Name: "adult", APIURL: "https://adult.test", Category: "成人"},
		{Name: "movie-a", APIURL: "https://movie-a.test", Category: "电影"},
		{Name: "movie-b", APIURL: "https://movie-b.test", Category: "电影"},
		{Name: "tv", APIURL: "https://tv.test", Category: "电视剧"},
	}

	got := selectContentSources(all, cfg, 1, 2)
	names := make(map[string]bool)
	for _, source := range got {
		names[source.Name] = true
	}
	for _, want := range []string{"fast-general", "adult", "movie-a", "movie-b", "tv"} {
		if !names[want] {
			t.Errorf("source %q was not selected", want)
		}
	}
}

func TestFormatVideoCaptionFollowsRouteChannel(t *testing.T) {
	item := content.ContentItem{
		Title:    "测试标题",
		Category: "default",
		Intro:    "测试简介",
	}
	format := "Madtoby的AV精选\n{title}\n分类：{category}\n原分类：{raw_category}"

	adult := formatVideoCaption(format, item, "成人")
	if adult != "Madtoby的AV精选\n测试标题\n分类：AV\n原分类：default" {
		t.Fatalf("adult caption = %q", adult)
	}

	tv := formatVideoCaption(format, item, "电视剧")
	if tv != "Madtoby的电视剧\n测试标题\n分类：电视剧\n原分类：default" {
		t.Fatalf("tv caption = %q", tv)
	}
}

func TestDueContentCategoriesUsesIndependentIntervals(t *testing.T) {
	cfg := &config.Config{
		ChannelMap: map[string][]int64{
			"成人":  {1},
			"电影":  {2},
			"电视剧": {3},
		},
		ChannelIntervals: map[string]int{
			"成人":  15,
			"电影":  60,
			"电视剧": 30,
		},
	}
	s := &Scheduler{
		Cfg:          cfg,
		categoryRuns: map[string]bool{},
		categoryLast: map[string]time.Time{
			"成人":  time.Now().Add(-16 * time.Minute),
			"电影":  time.Now().Add(-30 * time.Minute),
			"电视剧": time.Now().Add(-10 * time.Minute),
		},
	}

	if got, want := s.dueContentCategories(false), []string{"成人"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dueContentCategories() = %#v, want %#v", got, want)
	}
	if got, want := s.dueContentCategories(true), []string{"成人", "电影", "电视剧"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("forced dueContentCategories() = %#v, want %#v", got, want)
	}
}

func TestSelectCategoryCandidatesPrefersDifferentSources(t *testing.T) {
	items := []content.ContentItem{
		{Key: "a1", Title: "a1", Category: "movie", SourceURL: "https://a.test"},
		{Key: "a2", Title: "a2", Category: "movie", SourceURL: "https://a.test"},
		{Key: "b1", Title: "b1", Category: "movie", SourceURL: "https://b.test"},
		{Key: "tv1", Title: "tv1", Category: "tv", SourceURL: "https://tv.test"},
	}

	got := selectCategoryCandidates(items, "电影", 3, map[string]bool{})
	want := []string{"a1", "b1", "a2"}
	for i := range want {
		if got[i].Key != want[i] {
			t.Fatalf("candidate %d = %q, want %q", i, got[i].Key, want[i])
		}
	}
}

func TestEscapeHTMLDoesNotDoubleEscapeEntities(t *testing.T) {
	const input = "<p>A & B</p>"
	const want = "&lt;p&gt;A &amp; B&lt;/p&gt;"
	if got := escapeHTML(input); got != want {
		t.Fatalf("escapeHTML() = %q, want %q", got, want)
	}
}

func TestFormatVideoCaptionHidesEmptyIntroAndCoverURL(t *testing.T) {
	item := content.ContentItem{
		Title:    "测试标题",
		Category: "tv",
		CoverURL: "https://img.test/cover.webp",
	}
	format := "🎬 {cover}\n{title}\n简介：{intro}\n封面地址：{cover_url}"
	want := "🎬 \n测试标题\n封面地址：https://img.test/cover.webp"
	if got := formatVideoCaption(format, item, "电视剧"); got != want {
		t.Fatalf("formatVideoCaption() = %q, want %q", got, want)
	}
}
