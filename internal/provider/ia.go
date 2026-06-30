package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
)

// flexString decodes a JSON value that may be a string or array of strings.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	switch b[0] {
	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
	case '[':
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*f = flexString(strings.Join(arr, "; "))
	default:
		*f = flexString(strings.Trim(string(b), `"`))
	}
	return nil
}

type iaResponse struct {
	Files    []iaFile `json:"files"`
	Metadata struct {
		Creator    flexString `json:"creator"`
		Date       flexString `json:"date"`
		Title      flexString `json:"title"`
		LicenseURL flexString `json:"licenseurl"`
	} `json:"metadata"`
}

type iaFile struct {
	Name   string     `json:"name"`
	Format string     `json:"format"`
	Size   string     `json:"size"`
	Length string     `json:"length"`
	Track  flexString `json:"track"`
	Title  flexString `json:"title"`
	Album  flexString `json:"album"`
	Artist flexString `json:"artist"`
}

// IAProvider enumerates a single Internet Archive item.
type IAProvider struct {
	SourceKey      string
	Identifier     string
	ComposerForAll string // override composer for the whole item (e.g. Chopin)
	Client         *Client
}

// Key implements Provider.
func (p *IAProvider) Key() string { return p.SourceKey }

func iaFormatToken(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "ogg vorbis":
		return "ogg"
	case "vbr mp3", "mp3", "128kbps mp3", "64kbps mp3":
		return "mp3"
	case "opus", "ogg opus":
		return "opus"
	case "wave", "wav":
		return "wav"
	case "apple lossless audio":
		return "other"
	case "flac":
		return "flac"
	default:
		return ""
	}
}

// Discover fetches the item metadata and returns one candidate per recording.
func (p *IAProvider) Discover(ctx context.Context, _ string) ([]core.Candidate, string, bool, error) {
	u := "https://archive.org/metadata/" + url.PathEscape(p.Identifier)
	body, err := p.Client.GetBytes(ctx, u)
	if err != nil {
		return nil, "", true, fmt.Errorf("ia %s: %w", p.Identifier, err)
	}
	var resp iaResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, "", true, fmt.Errorf("ia %s decode: %w", p.Identifier, err)
	}
	// Existence check: non-empty files array (spec §0.1).
	if len(resp.Files) == 0 {
		return nil, "", true, nil
	}

	itemLicense := string(resp.Metadata.LicenseURL)
	licenseShort := licenseShortFromURL(itemLicense)
	itemDate := string(resp.Metadata.Date)

	groups := map[string]*core.Candidate{}
	var order []string
	for _, f := range resp.Files {
		token := iaFormatToken(f.Format)
		if token == "" {
			continue
		}
		base := strings.TrimSuffix(f.Name, path.Ext(f.Name))
		key := strings.ToLower(strings.TrimSpace(base))
		if key == "" {
			continue
		}
		dl := "https://archive.org/download/" + url.PathEscape(p.Identifier) + "/" + pathEscapeName(f.Name)

		cf := core.CandidateFile{URL: dl, Format: token, Bytes: parseInt(f.Size)}
		cand, ok := groups[key]
		if !ok {
			title := strings.TrimSpace(string(f.Title))
			if title == "" {
				title = base
			}
			cand = &core.Candidate{
				SourceItem: p.Identifier,
				WorkKey:    key,
				Meta: core.StructuredMeta{
					Title:        title,
					Composer:     p.ComposerForAll,
					Performer:    strings.TrimSpace(string(f.Artist)),
					Album:        strings.TrimSpace(string(f.Album)),
					DateRaw:      itemDate,
					Year:         parseYear(itemDate),
					LicenseShort: licenseShort,
					LicenseURL:   itemLicense,
				},
			}
			groups[key] = cand
			order = append(order, key)
		}
		cand.Files = append(cand.Files, cf)
	}

	out := make([]core.Candidate, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	return out, "", true, nil
}

func pathEscapeName(name string) string {
	parts := strings.Split(name, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func licenseShortFromURL(u string) string {
	lu := strings.ToLower(u)
	switch {
	case u == "":
		return ""
	case strings.Contains(lu, "publicdomain/zero"):
		return "CC0"
	case strings.Contains(lu, "publicdomain/mark"):
		return "Public domain"
	case strings.Contains(lu, "/by-sa/"):
		return "CC BY-SA"
	case strings.Contains(lu, "/by/"):
		return "CC BY"
	default:
		return "see license url"
	}
}
