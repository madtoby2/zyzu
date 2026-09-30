package scheduler

import (
	"github.com/madtoby2/zyzu/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMonitorUsesActualTransferBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(path+".part.mp4", []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{Cfg: &config.Config{ChannelMap: map[string][]int64{"tv": {1}}}, categoryRuns: map[string]bool{"tv": true}, channelJobs: map[string]ChannelJobStatus{"tv": {State: "downloading", File: path, UpdatedAt: time.Now()}}}
	job := s.Status()["channel_jobs"].(map[string]ChannelJobStatus)["tv"]
	if job.DoneBytes != 5 || job.ProgressKnown {
		t.Fatalf("unexpected download progress: %+v", job)
	}
	if err := os.WriteFile(path+".upload.json", []byte(`{"done":25,"total":100}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.channelJobs["tv"] = ChannelJobStatus{State: "uploading", File: path, UpdatedAt: time.Now()}
	job = s.Status()["channel_jobs"].(map[string]ChannelJobStatus)["tv"]
	if job.DoneBytes != 25 || job.TotalBytes != 100 || !job.ProgressKnown {
		t.Fatalf("unexpected upload progress: %+v", job)
	}
}

func TestMonitorIncludesFutureChannelsAndPausedReason(t *testing.T) {
	s := &Scheduler{Cfg: &config.Config{ChannelMap: map[string][]int64{"new": {1}, "paused": {2}, "empty": {}}, ChannelPolicies: map[string]config.ChannelPolicy{"paused": {Paused: true}}}, channelJobs: map[string]ChannelJobStatus{}}
	jobs := s.Status()["channel_jobs"].(map[string]ChannelJobStatus)
	if jobs["new"].Reason == "" || jobs["paused"].State != "paused" || jobs["empty"].State != "unbound" {
		t.Fatalf("missing channel states: %+v", jobs)
	}
}
