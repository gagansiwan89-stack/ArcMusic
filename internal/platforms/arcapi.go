/*
 * ● ArcMusic
 * ○ A high-performance engine for streaming music in Telegram voicechats.
 *
 * Copyright (C) 2026 Team Arc
 */

package platforms

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Laky-64/gologging"
	"github.com/amarnathcjd/gogram/telegram"

	"main/internal/config"
	state "main/internal/core/models"
)

const PlatformArcApi state.PlatformName = "ArcApi"

type ArcApiPlatform struct {
	name state.PlatformName
}

func init() {
	Register(80, &ArcApiPlatform{
		name: PlatformArcApi,
	})
}

func (f *ArcApiPlatform) Name() state.PlatformName {
	return f.name
}

func (f *ArcApiPlatform) CanGetTracks(query string) bool {
	return false
}

func (f *ArcApiPlatform) GetTracks(_ string, _ bool) ([]*state.Track, error) {
	return nil, errors.New("arcapi is a download-only platform")
}

func (f *ArcApiPlatform) CanDownload(source state.PlatformName) bool {
	if config.ArcAPIURL == "" || config.ArcAPIKey == "" {
		return false
	}
	return source == PlatformYouTube
}

func (f *ArcApiPlatform) Download(
	ctx context.Context,
	track *state.Track,
	statusMsg *telegram.NewMessage,
) (string, error) {

	// Check local cache just in case the file was historically downloaded
	if f := findFile(track); f != "" {
		gologging.Debug("ArcApi: Download -> Local Cached File -> " + f)
		return f, nil
	}

	gologging.Debug("ArcApi: Fetching direct streaming URL from API V2")

	dlURL, err := f.v2Download(ctx, track)
	if err != nil {
		gologging.ErrorF("ArcApi: V2 URL fetch failed: %v", err)
		return "", err
	}

	gologging.Info(fmt.Sprintf("✅ V2-API Direct Stream | %s | Video: %t", track.ID, track.Video))
	return dlURL, nil
}

func (*ArcApiPlatform) CanSearch() bool { return false }

func (*ArcApiPlatform) Search(string, bool) ([]*state.Track, error) {
	return nil, nil
}

func (f *ArcApiPlatform) v2Download(ctx context.Context, track *state.Track) (string, error) {
	apiURL := strings.TrimRight(config.ArcAPIURL, "/")
	apiKey := config.ArcAPIKey

	query := track.ID
	if query == "" {
		query = track.URL
	}

	reqURL := fmt.Sprintf("%s/youtube/v2/download", apiURL)

	var respData map[string]any
	resp, err := rc.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"api_key": apiKey,
			"query":   query,
			"isVideo": strconv.FormatBool(track.Video),
		}).
		SetResult(&respData).
		Get(reqURL)

	if err != nil {
		return "", fmt.Errorf("failed to reach api: %w", err)
	}
	if resp.IsError() {
		return "", fmt.Errorf("api returned error status: %d", resp.StatusCode())
	}

	candidate := f.extractCandidate(respData)
	if candidate != "" && !strings.Contains(strings.ToLower(candidate), "processing") && !strings.Contains(strings.ToLower(candidate), "queued") {
		return f.normalizeURL(candidate, apiURL), nil
	}

	status, _ := respData["status"].(string)
	if status == "queued" || status == "processing" {
		jobID := f.extractJobID(respData)
		if jobID != "" {
			gologging.DebugF("ArcApi: Polling Job ID: %s", jobID)

			dlURL := f.pollJobStatus(ctx, jobID)
			if dlURL != "" {
				return f.normalizeURL(dlURL, apiURL), nil
			}
		}
	}

	return "", errors.New("failed to extract download url or job_id from api")
}

func (f *ArcApiPlatform) pollJobStatus(ctx context.Context, jobID string) string {
	apiURL := strings.TrimRight(config.ArcAPIURL, "/")
	apiKey := config.ArcAPIKey

	retries := 8
	sleepDuration := 7 * time.Second

	reqURL := fmt.Sprintf("%s/youtube/jobStatus", apiURL)

	for attempt := 0; attempt < retries; attempt++ {
		var respData map[string]any
		resp, err := rc.R().
			SetContext(ctx).
			SetQueryParams(map[string]string{
				"api_key": apiKey,
				"job_id":  jobID,
			}).
			SetResult(&respData).
			Get(reqURL)

		if err != nil || resp.IsError() {
			time.Sleep(sleepDuration)
			continue
		}

		status, _ := respData["status"].(string)
		if status != "success" {
			time.Sleep(sleepDuration)
			continue
		}

		job, ok := respData["job"].(map[string]any)
		if !ok {
			time.Sleep(sleepDuration)
			continue
		}

		jobStatus, _ := job["status"].(string)
		if jobStatus != "done" {
			time.Sleep(sleepDuration)
			continue
		}

		if result, ok := job["result"].(map[string]any); ok {
			if pubURL, ok := result["public_url"].(string); ok && pubURL != "" {
				return pubURL
			}
		}

		break
	}
	return ""
}

func (f *ArcApiPlatform) extractCandidate(data map[string]any) string {
	if job, ok := data["job"].(map[string]any); ok {
		if res, ok := job["result"].(map[string]any); ok {
			for _, k := range []string{"public_url"} {
				if v, ok := res[k].(string); ok && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
	}
	if res, ok := data["result"].(map[string]any); ok {
		for _, k := range []string{"public_url"} {
			if v, ok := res[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	for _, k := range []string{"public_url"} {
		if v, ok := data[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (f *ArcApiPlatform) extractJobID(data map[string]any) string {
	if job, ok := data["job"].(map[string]any); ok {
		if id, ok := job["id"].(string); ok {
			return id
		}
	}
	if id, ok := data["job_id"].(string); ok {
		return id
	}
	return ""
}

func (f *ArcApiPlatform) normalizeURL(candidate, apiURL string) string {
	if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
		return candidate
	}
	if strings.HasPrefix(candidate, "/") {
		return apiURL + candidate
	}
	return apiURL + "/" + candidate
}
