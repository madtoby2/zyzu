package handler

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os/exec"
	"path/filepath"
	"time"
)

// Fixed commands and paths only: never accept a deletion path from the browser.
func (h *Handler) storageStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	preview, err := exec.CommandContext(ctx, "python3", "/usr/local/lib/zyzu-cache-janitor.py").CombinedOutput()
	sizes := map[string]int64{}
	var scanErrors []string
	for label, root := range map[string]string{"video": h.sched.Video.WorkDir, "scripts": "/opt/deploy/data/tmp"} {
		scanErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.Type().IsRegular() {
				info, e := entry.Info()
				if e != nil {
					return e
				}
				sizes[label] += info.Size()
			}
			return nil
		})
		if scanErr != nil {
			scanErrors = append(scanErrors, label+": "+scanErr.Error())
		}
	}
	history, _ := exec.CommandContext(ctx, "journalctl", "-u", "zyzu-cache-janitor.service", "-n", "15", "--no-pager", "-o", "short-iso").Output()
	timer, _ := exec.CommandContext(ctx, "systemctl", "is-active", "zyzu-cache-janitor.timer").Output()
	message := ""
	if err != nil {
		message = fmt.Sprintf("清理预览不可用：%v", err)
	}
	jsonOK(w, map[string]interface{}{"disk": diskStatus(h.sched.Video.WorkDir), "sizes": sizes, "scan_errors": scanErrors, "preview": string(preview), "history": string(history), "timer": string(timer), "cleanup_available": err == nil, "error": message})
}

func (h *Handler) cleanupStorage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "python3", "/usr/local/lib/zyzu-cache-janitor.py", "--apply").CombinedOutput()
	if err != nil {
		jsonError(w, "清理失败或已有清理任务运行："+string(out), http.StatusConflict)
		return
	}
	_ = h.store.LogEvent("ok", "手动清理过期缓存："+string(out))
	jsonOK(w, map[string]string{"result": string(out)})
}
