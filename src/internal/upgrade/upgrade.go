// Package upgrade implements GitHub-facing kernel/panel upgrades: query the latest,
// download (local proxy preferred), trial-validate, atomic swap, backup rotation.
// Orchestration (restart + health + rollback) lives in the command layer.
package upgrade

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/deadship2003/panoxy/internal/httpx"
	"github.com/deadship2003/panoxy/internal/logx"
)

// Latest queries a repo's (e.g. MetaCubeX/mihomo) latest stable tag; local proxy
// preferred, direct as the fallback.
func Latest(repo, proxy string) (string, error) {
	api := "https://api.github.com/repos/" + repo + "/releases/latest"
	fetch := func(p string) (string, error) {
		hc := httpx.Client(p, 15*time.Second)
		resp, err := hc.Get(api)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		var rel struct {
			TagName string `json:"tag_name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil || rel.TagName == "" {
			return "", fmt.Errorf("unexpected release response")
		}
		return rel.TagName, nil
	}
	if v, err := fetch(proxy); err == nil {
		return v, nil
	}
	return fetch("")
}

// Download fetches url to dst (proxy preferred, direct fallback; >=300 is a failure).
func Download(urlStr, proxy, dst string) error {
	try := func(p string) error {
		hc := httpx.Client(p, 300*time.Second)
		resp, err := hc.Get(urlStr)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		f, err := os.Create(dst)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, resp.Body)
		return err
	}
	if err := try(proxy); err == nil {
		return nil
	}
	logx.Step("download via proxy failed, retrying direct: %s", urlStr)
	return try("")
}

// DownloadProgress downloads with a progress bar: a uniform 10-minute timeout (percent
// rendered when Content-Length is known). The connectivity probe (15s hard cap) has
// already been done by the caller's directAssetReachable, so it is not repeated here;
// a large-file download normally needs 10-30s, and a 15s hard cap would kill it
// (lesson from practice).
func DownloadProgress(urlStr, proxy, dst, label string) error {
	return downloadOnce(urlStr, proxy, dst, label, 600*time.Second)
}

func downloadOnce(urlStr, proxy, dst, label string, timeout time.Duration) error {
	hc := httpx.Client(proxy, timeout)
	req, _ := http.NewRequest("GET", urlStr, nil)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	pg := logx.NewProgress(label, resp.ContentLength)
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	var n int64
	buf := make([]byte, 64<<10)
loop:
	for {
		m, rerr := resp.Body.Read(buf)
		if m > 0 {
			if _, werr := f.Write(buf[:m]); werr != nil {
				f.Close()
				pg.Done(werr)
				return werr
			}
			n += int64(m)
			pg.Update(n)
		}
		if rerr != nil {
			if rerr == io.EOF {
				break loop
			}
			f.Close()
			pg.Done(rerr)
			return rerr
		}
	}
	err = f.Close()
	pg.Done(err)
	return err
}
