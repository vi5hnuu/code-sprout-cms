package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// YouTube is a minimal YouTube Data API v3 client: only the read calls the sync needs, each
// costing one quota unit. Every call reports its cost to the caller's budget.
type YouTube struct {
	base string
	key  string
	http *http.Client
}

const youtubeAPI = "https://www.googleapis.com/youtube/v3"

// NewYouTube builds a client; base "" is the public API (tests pass an httptest URL).
func NewYouTube(apiKey, base string) *YouTube {
	if base == "" {
		base = youtubeAPI
	}
	return &YouTube{base: strings.TrimRight(base, "/"), key: apiKey, http: &http.Client{Timeout: 15 * time.Second}}
}

// ErrQuota means the API refused for quota; the run stops and resumes on the next tick.
var ErrQuota = errors.New("youtube quota exhausted")

type apiError struct {
	Error struct {
		Code   int `json:"code"`
		Errors []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
		Message string `json:"message"`
	} `json:"error"`
}

func (y *YouTube) get(ctx context.Context, path string, q url.Values, out any) error {
	q.Set("key", y.key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, y.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := y.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e apiError
		_ = json.Unmarshal(body, &e)
		for _, r := range e.Error.Errors {
			if r.Reason == "quotaExceeded" || r.Reason == "dailyLimitExceeded" || r.Reason == "rateLimitExceeded" {
				return ErrQuota
			}
		}
		return fmt.Errorf("youtube %s: %d %s", path, resp.StatusCode, e.Error.Message)
	}
	return json.Unmarshal(body, out)
}

// UploadsPlaylist returns a channel's "uploads" playlist: every public video it published.
func (y *YouTube) UploadsPlaylist(ctx context.Context, channelID string) (string, error) {
	var r struct {
		Items []struct {
			ContentDetails struct {
				RelatedPlaylists struct {
					Uploads string `json:"uploads"`
				} `json:"relatedPlaylists"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	if err := y.get(ctx, "/channels", url.Values{"part": {"contentDetails"}, "id": {channelID}}, &r); err != nil {
		return "", err
	}
	if len(r.Items) == 0 || r.Items[0].ContentDetails.RelatedPlaylists.Uploads == "" {
		return "", fmt.Errorf("channel %s not found", channelID)
	}
	return r.Items[0].ContentDetails.RelatedPlaylists.Uploads, nil
}

// PlaylistEntry is one video in a playlist, with who uploaded it.
type PlaylistEntry struct {
	VideoID      string
	OwnerChannel string
}

// PlaylistPage returns one page (≤ 50) of a playlist and the token for the next ("" at the end).
func (y *YouTube) PlaylistPage(ctx context.Context, playlistID, pageToken string) ([]PlaylistEntry, string, error) {
	var r struct {
		NextPageToken string `json:"nextPageToken"`
		Items         []struct {
			Snippet struct {
				VideoOwnerChannelID string `json:"videoOwnerChannelId"`
				ResourceID          struct {
					VideoID string `json:"videoId"`
				} `json:"resourceId"`
			} `json:"snippet"`
		} `json:"items"`
	}
	q := url.Values{"part": {"snippet"}, "playlistId": {playlistID}, "maxResults": {"50"}}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	if err := y.get(ctx, "/playlistItems", q, &r); err != nil {
		return nil, "", err
	}
	out := make([]PlaylistEntry, 0, len(r.Items))
	for _, it := range r.Items {
		if it.Snippet.ResourceID.VideoID != "" {
			out = append(out, PlaylistEntry{VideoID: it.Snippet.ResourceID.VideoID, OwnerChannel: it.Snippet.VideoOwnerChannelID})
		}
	}
	return out, r.NextPageToken, nil
}

// Upload is what the sync keeps about one YouTube video.
type Upload struct {
	ID          string
	ChannelID   string
	Title       string
	Description string
	PublishedAt time.Time
	Duration    int
	Thumbnail   Thumbnail
	Embeddable  bool
	Public      bool
	Live        bool // a live stream or premiere still running: no fixed duration yet
}

type Thumbnail struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Videos returns details for up to 50 ids in one call. Ids YouTube no longer serves (deleted,
// made private) are simply absent from the result.
func (y *YouTube) Videos(ctx context.Context, ids []string) ([]Upload, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 50 {
		return nil, errors.New("at most 50 ids per call")
	}
	var r struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				ChannelID            string               `json:"channelId"`
				Title                string               `json:"title"`
				Description          string               `json:"description"`
				PublishedAt          time.Time            `json:"publishedAt"`
				Thumbnails           map[string]Thumbnail `json:"thumbnails"`
				LiveBroadcastContent string               `json:"liveBroadcastContent"`
			} `json:"snippet"`
			ContentDetails struct {
				Duration string `json:"duration"`
			} `json:"contentDetails"`
			Status struct {
				PrivacyStatus string `json:"privacyStatus"`
				Embeddable    bool   `json:"embeddable"`
			} `json:"status"`
		} `json:"items"`
	}
	q := url.Values{"part": {"snippet,contentDetails,status"}, "id": {strings.Join(ids, ",")}, "maxResults": {"50"}}
	if err := y.get(ctx, "/videos", q, &r); err != nil {
		return nil, err
	}
	out := make([]Upload, 0, len(r.Items))
	for _, it := range r.Items {
		d := Upload{
			ID: it.ID, ChannelID: it.Snippet.ChannelID, Title: it.Snippet.Title, Description: it.Snippet.Description,
			PublishedAt: it.Snippet.PublishedAt, Embeddable: it.Status.Embeddable, Public: it.Status.PrivacyStatus == "public",
			Live:      it.Snippet.LiveBroadcastContent != "" && it.Snippet.LiveBroadcastContent != "none",
			Thumbnail: bestThumbnail(it.Snippet.Thumbnails),
		}
		if !d.Live {
			dur, err := ParseDuration(it.ContentDetails.Duration)
			if err != nil {
				return nil, fmt.Errorf("video %s: %w", it.ID, err)
			}
			d.Duration = dur
		}
		out = append(out, d)
	}
	return out, nil
}

// bestThumbnail prefers the largest 16:9 image YouTube generated (maxres is not always there).
func bestThumbnail(t map[string]Thumbnail) Thumbnail {
	for _, k := range []string{"maxres", "standard", "high", "medium", "default"} {
		if th, ok := t[k]; ok && th.URL != "" {
			return th
		}
	}
	return Thumbnail{}
}

// Playlist is one of a channel's public playlists.
type Playlist struct {
	ID          string
	Title       string
	Description string
}

// PlaylistsPage returns one page of a channel's public playlists.
func (y *YouTube) PlaylistsPage(ctx context.Context, channelID, pageToken string) ([]Playlist, string, error) {
	var r struct {
		NextPageToken string `json:"nextPageToken"`
		Items         []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"snippet"`
		} `json:"items"`
	}
	q := url.Values{"part": {"snippet"}, "channelId": {channelID}, "maxResults": {"50"}}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	if err := y.get(ctx, "/playlists", q, &r); err != nil {
		return nil, "", err
	}
	out := make([]Playlist, len(r.Items))
	for i, it := range r.Items {
		out[i] = Playlist{ID: it.ID, Title: it.Snippet.Title, Description: it.Snippet.Description}
	}
	return out, r.NextPageToken, nil
}
