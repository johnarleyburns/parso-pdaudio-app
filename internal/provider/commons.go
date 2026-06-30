package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
)

const commonsAPI = "https://commons.wikimedia.org/w/api.php"

// CommonsProvider enumerates audio files matching a free-text band query.
type CommonsProvider struct {
	SourceKey string
	Query     string // e.g. `"United States Marine Band"` (filetype:audio is appended)
	PageSize  int
	Client    *Client
}

// Key implements Provider.
func (p *CommonsProvider) Key() string { return p.SourceKey }

type commonsSearchResp struct {
	BatchComplete any `json:"batchcomplete"`
	Continue      struct {
		SrOffset int    `json:"sroffset"`
		Continue string `json:"continue"`
	} `json:"continue"`
	Query struct {
		SearchInfo struct {
			TotalHits int `json:"totalhits"`
		} `json:"searchinfo"`
		Search []struct {
			Title string `json:"title"`
		} `json:"search"`
	} `json:"query"`
	Error *struct {
		Code string `json:"code"`
		Info string `json:"info"`
	} `json:"error"`
}

type commonsImageInfoResp struct {
	Query struct {
		Pages map[string]struct {
			Title     string `json:"title"`
			ImageInfo []struct {
				URL         string `json:"url"`
				Mime        string `json:"mime"`
				Size        int64  `json:"size"`
				ExtMetadata map[string]struct {
					Value flexString `json:"value"`
				} `json:"extmetadata"`
			} `json:"imageinfo"`
		} `json:"pages"`
	} `json:"query"`
	Error *struct {
		Code string `json:"code"`
		Info string `json:"info"`
	} `json:"error"`
}

// Discover returns one page of candidates and the next cursor (sroffset).
func (p *CommonsProvider) Discover(ctx context.Context, cursor string) ([]core.Candidate, string, bool, error) {
	pageSize := p.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	srsearch := p.Query + " filetype:audio"

	q := url.Values{}
	q.Set("action", "query")
	q.Set("list", "search")
	q.Set("srsearch", srsearch)
	q.Set("srnamespace", "6")
	q.Set("srlimit", strconv.Itoa(pageSize))
	if cursor != "" {
		q.Set("sroffset", cursor)
	}
	q.Set("format", "json")
	q.Set("maxlag", "5")

	body, err := p.Client.GetBytes(ctx, commonsAPI+"?"+q.Encode())
	if err != nil {
		return nil, "", true, fmt.Errorf("commons search %s: %w", p.SourceKey, err)
	}
	var sr commonsSearchResp
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, "", true, fmt.Errorf("commons search decode: %w", err)
	}
	if sr.Error != nil {
		return nil, "", true, fmt.Errorf("commons search error %s: %s", sr.Error.Code, sr.Error.Info)
	}

	var titles []string
	for _, s := range sr.Query.Search {
		titles = append(titles, s.Title)
	}

	done := sr.Continue.SrOffset == 0
	next := ""
	if !done {
		next = strconv.Itoa(sr.Continue.SrOffset)
	}

	if len(titles) == 0 {
		return nil, next, done, nil
	}

	cands, err := p.resolve(ctx, titles)
	if err != nil {
		return nil, next, done, err
	}
	return cands, next, done, nil
}

func (p *CommonsProvider) resolve(ctx context.Context, titles []string) ([]core.Candidate, error) {
	q := url.Values{}
	q.Set("action", "query")
	q.Set("titles", strings.Join(titles, "|"))
	q.Set("prop", "imageinfo")
	q.Set("iiprop", "url|mime|size|extmetadata")
	q.Set("format", "json")
	q.Set("maxlag", "5")

	body, err := p.Client.GetBytes(ctx, commonsAPI+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("commons imageinfo: %w", err)
	}
	var ir commonsImageInfoResp
	if err := json.Unmarshal(body, &ir); err != nil {
		return nil, fmt.Errorf("commons imageinfo decode: %w", err)
	}
	if ir.Error != nil {
		return nil, fmt.Errorf("commons imageinfo error %s: %s", ir.Error.Code, ir.Error.Info)
	}

	groups := map[string]*core.Candidate{}
	var order []string
	for _, page := range ir.Query.Pages {
		if len(page.ImageInfo) == 0 {
			continue
		}
		ii := page.ImageInfo[0]
		token := commonsFormatToken(ii.Mime, page.Title)
		if token == "" {
			continue
		}
		ext := func(k string) string { return strings.TrimSpace(string(ii.ExtMetadata[k].Value)) }
		title := cleanTitle(page.Title)
		workKey := workKeyFromTitle(page.Title)

		cf := core.CandidateFile{URL: ii.URL, Format: token, Bytes: ii.Size}
		cand, ok := groups[workKey]
		if !ok {
			dateRaw := stripHTML(ext("DateTimeOriginal"))
			composer, work, movement := parseClassicalTitle(title)
			cand = &core.Candidate{
				SourceItem: page.Title,
				WorkKey:    workKey,
				Meta: core.StructuredMeta{
					Title:        title,
					Work:         work,
					Movement:     movement,
					Composer:     composer,
					Performer:    stripHTML(ext("Artist")),
					DateRaw:      dateRaw,
					Year:         parseYear(dateRaw),
					LicenseShort: stripHTML(ext("LicenseShortName")),
					LicenseURL:   stripHTML(ext("LicenseUrl")),
				},
			}
			groups[workKey] = cand
			order = append(order, workKey)
		}
		cand.Files = append(cand.Files, cf)
	}

	out := make([]core.Candidate, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	return out, nil
}

func commonsFormatToken(mime, title string) string {
	ext := strings.ToLower(path.Ext(title))
	switch mime {
	case "audio/ogg":
		if ext == ".opus" {
			return "opus"
		}
		return "ogg" // .ogg/.oga; codec confirmed by ffprobe at convert time
	case "audio/opus":
		return "opus"
	case "audio/x-wav", "audio/wav", "audio/wave", "audio/vnd.wave":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "application/ogg", "application/x-ogg":
		// An Ogg container can carry audio or video; trust the audio extension
		// and let ffprobe confirm the codec at convert time (spec §6.2).
		switch ext {
		case ".opus":
			return "opus"
		case ".ogg", ".oga":
			return "ogg"
		}
		return ""
	default:
		if strings.HasPrefix(mime, "audio/") {
			return "other"
		}
		return ""
	}
}

var fmtWordRe = regexp.MustCompile(`(?i)[ _-]+(ogg|oga|opus|wav|wave|mp3|flac)$`)

// cleanTitle removes the File: prefix and extension for display.
func cleanTitle(title string) string {
	t := strings.TrimPrefix(title, "File:")
	t = strings.TrimSuffix(t, path.Ext(t))
	t = strings.ReplaceAll(t, "_", " ")
	return strings.TrimSpace(t)
}

// workKeyFromTitle builds a dedup key: lowercased, extension + trailing format
// word removed, whitespace collapsed.
func workKeyFromTitle(title string) string {
	t := cleanTitle(title)
	t = fmtWordRe.ReplaceAllString(t, "")
	t = strings.ToLower(t)
	t = strings.Join(strings.Fields(t), " ")
	return t
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&amp;", "&")
	return strings.TrimSpace(s)
}

// parseClassicalTitle is a light heuristic for `Composer, Work, Movement` and
// `Work - Performer` patterns. Returns empty strings when unsure.
func parseClassicalTitle(title string) (composer, work, movement string) {
	t := strings.TrimSpace(title)
	// Drop a trailing performer clause like " - United States Marine Band".
	if idx := strings.LastIndex(t, " - "); idx > 0 {
		tail := strings.ToLower(t[idx+3:])
		if strings.Contains(tail, "band") || strings.Contains(tail, "orchestra") ||
			strings.Contains(tail, "performed") || strings.Contains(tail, "ensemble") {
			t = strings.TrimSpace(t[:idx])
		}
	}
	parts := strings.Split(t, ",")
	if len(parts) >= 2 {
		first := strings.TrimSpace(parts[0])
		// Heuristic: a short leading token that looks like a surname is composer.
		if len(strings.Fields(first)) <= 2 && isLikelyName(first) {
			composer = first
			rest := strings.TrimSpace(strings.Join(parts[1:], ","))
			if mi := strings.LastIndex(rest, " - "); mi > 0 {
				work = strings.TrimSpace(rest[:mi])
				movement = strings.TrimSpace(rest[mi+3:])
			} else {
				work = rest
			}
			return composer, work, movement
		}
	}
	work = t
	return "", work, ""
}

func isLikelyName(s string) bool {
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '"' {
			return false
		}
	}
	return s != "" && strings.ToUpper(s[:1]) == s[:1]
}

func parseInt(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}
