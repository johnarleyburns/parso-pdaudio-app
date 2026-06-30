package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"golang.org/x/text/unicode/norm"
)

// ClassicalCategoriesProvider enumerates classical audio files by traversing
// Commons category trees. It reads the composer worklist from a root category
// (B.1), recursively walks each composer's subcategories (B.2), batch-resolves
// imageinfo (B.4), and applies filtering gates (B.3).
type ClassicalCategoriesProvider struct {
	SourceKey          string
	RootCategory       string
	MaxDepth           int
	SkipSubcatPatterns []string
	MinBytes           int64
	MinDurationSec     float64
	LicensePolicy      string // "strict" or "attribution"
	ComposerAllowlist  []string
	PageSize           int // candidates per Discover call (0 = unlimited)
	Client             *Client

	// state populated across Discover calls
	composerList    []composerInfo
	initialized     bool
	pendingFiles    []fileWithCtx // files already collected but not yet resolved
	pendingComposer string        // composer name for pending files
	pendingIndex    int           // index of the composer whose files are pending
}

type composerInfo struct {
	CategoryTitle string
	Name          string
}

// Key implements Provider.
func (p *ClassicalCategoriesProvider) Key() string { return p.SourceKey }

// --- API response types ---

type cmResp struct {
	Continue struct {
		CmContinue string `json:"cmcontinue"`
		Continue   string `json:"continue"`
	} `json:"continue"`
	Query struct {
		CategoryMembers []struct {
			Title string `json:"title"`
			Ns    int    `json:"ns"`
		} `json:"categorymembers"`
	} `json:"query"`
	Error *struct {
		Code string `json:"code"`
		Info string `json:"info"`
	} `json:"error"`
}

// --- Cursor ---

type ccCursor struct {
	ComposerIndex int `json:"i"`
}

func (c ccCursor) encode() string {
	b, _ := json.Marshal(c)
	return string(b)
}

func decodeCCCursor(s string) ccCursor {
	var c ccCursor
	if s == "" {
		return c
	}
	_ = json.Unmarshal([]byte(s), &c)
	return c
}

// --- Discover ---

// Discover returns candidates in pages. Each call processes up to PageSize
// files from the current composer, traversing+resolving on demand so the
// caller sees results and progress instead of a single long synchronous wait.
func (p *ClassicalCategoriesProvider) Discover(ctx context.Context, cursor string) ([]core.Candidate, string, bool, error) {
	cur := decodeCCCursor(cursor)
	pageSize := p.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}

	// Initialize composer list on first call
	if !p.initialized {
		composers, err := p.fetchComposerList(ctx)
		if err != nil {
			return nil, "", true, fmt.Errorf("fetch composer list: %w", err)
		}
		p.composerList = composers
		p.initialized = true
	}

	// Accumulate candidates until we have enough or run out of composers.
	var candidates []core.Candidate
	startIndex := cur.ComposerIndex

	for {
		// Reached end of composer list
		if cur.ComposerIndex >= len(p.composerList) {
			done := len(candidates) == 0
			return candidates, cur.encode(), done, nil
		}

		ci := p.composerList[cur.ComposerIndex]

		// Need to collect more files for this composer?
		if len(p.pendingFiles) == 0 || p.pendingIndex != cur.ComposerIndex {
			p.pendingComposer = ci.Name
			p.pendingIndex = cur.ComposerIndex
			var err error
			p.pendingFiles, err = p.traverse(ctx, ci.CategoryTitle)
			if err != nil || len(p.pendingFiles) == 0 {
				p.pendingFiles = nil
				cur.ComposerIndex++
				// If we've already found some candidates, return them
				if len(candidates) > 0 {
					return candidates, cur.encode(), false, nil
				}
				continue
			}
		}

		// Take a page of files from pending
		batch := p.pendingFiles
		if len(batch) > pageSize {
			batch = batch[:pageSize]
		}
		p.pendingFiles = p.pendingFiles[len(batch):]
		if len(p.pendingFiles) == 0 {
			p.pendingFiles = nil
		}

		// Resolve this batch
		resolved, err := p.resolveFiles(ctx, ci.Name, batch)
		if err != nil {
			p.pendingFiles = nil
			cur.ComposerIndex++
			if len(candidates) > 0 {
				return candidates, cur.encode(), false, nil
			}
			continue
		}

		candidates = append(candidates, resolved...)

		// If we've collected enough candidates or this composer is drained,
		// return what we have.
		if len(candidates) >= pageSize || len(p.pendingFiles) == 0 {
			if len(p.pendingFiles) == 0 {
				cur.ComposerIndex++
			}
			return candidates, cur.encode(), false, nil
		}

		// If we passed through multiple composers to get here, return
		if cur.ComposerIndex > startIndex {
			return candidates, cur.encode(), false, nil
		}

		// Still need more — continue with same composer
	}
}

// fetchComposerList fetches subcategories of the root category and extracts
// composer names from the category titles.
func (p *ClassicalCategoriesProvider) fetchComposerList(ctx context.Context) ([]composerInfo, error) {
	var all []composerInfo
	cmcontinue := ""

	for {
		q := url.Values{}
		q.Set("action", "query")
		q.Set("list", "categorymembers")
		q.Set("cmtitle", p.RootCategory)
		q.Set("cmtype", "subcat")
		q.Set("cmlimit", "500")
		q.Set("format", "json")
		q.Set("maxlag", "5")
		if cmcontinue != "" {
			q.Set("cmcontinue", cmcontinue)
		}

		body, err := p.Client.GetBytes(ctx, commonsAPI+"?"+q.Encode())
		if err != nil {
			return nil, err
		}
		var resp cmResp
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("categorymembers decode: %w", err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("categorymembers error %s: %s", resp.Error.Code, resp.Error.Info)
		}
		for _, m := range resp.Query.CategoryMembers {
			name := extractComposerFromCategory(m.Title)
			if name == "" {
				name = strings.TrimPrefix(m.Title, "Category:Audio files of music by ")
			}
			all = append(all, composerInfo{CategoryTitle: m.Title, Name: name})
		}
		if resp.Continue.CmContinue == "" {
			break
		}
		cmcontinue = resp.Continue.CmContinue
	}

	// Filter by allowlist if set
	if len(p.ComposerAllowlist) > 0 {
		allow := make(map[string]bool)
		for _, a := range p.ComposerAllowlist {
			allow[strings.ToLower(a)] = true
		}
		filtered := all[:0]
		for _, ci := range all {
			if allow[strings.ToLower(ci.CategoryTitle)] || allow[strings.ToLower(ci.Name)] {
				filtered = append(filtered, ci)
			}
		}
		all = filtered
	}

	return all, nil
}

// fileWithCtx attaches the originating subcategory title for performer extraction.
type fileWithCtx struct {
	title  string
	catCtx string
}

// traverse does a depth-limited, cycle-safe DFS through a composer's category tree.
func (p *ClassicalCategoriesProvider) traverse(ctx context.Context, rootCat string) ([]fileWithCtx, error) {
	maxDepth := p.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}

	seen := make(map[string]bool)
	type item struct {
		title string
		depth int
	}
	stack := []item{{title: rootCat, depth: 0}}
	var files []fileWithCtx

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if seen[cur.title] {
			continue
		}
		seen[cur.title] = true

		cmcontinue := ""
		for {
			q := url.Values{}
			q.Set("action", "query")
			q.Set("list", "categorymembers")
			q.Set("cmtitle", cur.title)
			q.Set("cmtype", "file|subcat")
			q.Set("cmlimit", "500")
			q.Set("format", "json")
			q.Set("maxlag", "5")
			if cmcontinue != "" {
				q.Set("cmcontinue", cmcontinue)
			}

			body, err := p.Client.GetBytes(ctx, commonsAPI+"?"+q.Encode())
			if err != nil {
				return nil, err
			}
			var resp cmResp
			if err := json.Unmarshal(body, &resp); err != nil {
				return nil, fmt.Errorf("categorymembers decode: %w", err)
			}
			if resp.Error != nil {
				return nil, fmt.Errorf("categorymembers error %s: %s", resp.Error.Code, resp.Error.Info)
			}

			for _, m := range resp.Query.CategoryMembers {
				if m.Ns == 6 { // File:
					files = append(files, fileWithCtx{title: m.Title, catCtx: cur.title})
				} else if m.Ns == 14 && cur.depth < maxDepth { // Subcategory
					if isSkippableSubcat(m.Title, p.SkipSubcatPatterns) {
						continue
					}
					stack = append(stack, item{title: m.Title, depth: cur.depth + 1})
				}
			}

			if resp.Continue.CmContinue == "" {
				break
			}
			cmcontinue = resp.Continue.CmContinue
		}
	}

	return files, nil
}

// resolveFiles batches imageinfo calls and builds filtered, deduped candidates.
func (p *ClassicalCategoriesProvider) resolveFiles(ctx context.Context, composerName string, files []fileWithCtx) ([]core.Candidate, error) {
	minBytes := p.MinBytes
	if minBytes <= 0 {
		minBytes = 250000
	}

	groups := map[string]*core.Candidate{}
	var order []string

	// Collect titles, deduplicate
	seen := map[string]bool{}
	var titles []string
	for _, f := range files {
		if !seen[f.title] {
			seen[f.title] = true
			titles = append(titles, f.title)
		}
	}
	if len(titles) == 0 {
		return nil, nil
	}

	// Build reverse lookup for performer extraction
	ctxMap := map[string]string{}
	for _, f := range files {
		if ctxMap[f.title] == "" {
			ctxMap[f.title] = f.catCtx
		}
	}

	for len(titles) > 0 {
		batch := titles
		if len(batch) > 50 {
			batch = batch[:50]
		}
		titles = titles[len(batch):]

		q := url.Values{}
		q.Set("action", "query")
		q.Set("titles", strings.Join(batch, "|"))
		q.Set("prop", "imageinfo")
		q.Set("iiprop", "url|mime|size|extmetadata")
		q.Set("format", "json")
		q.Set("maxlag", "5")

		body, err := p.Client.GetBytes(ctx, commonsAPI+"?"+q.Encode())
		if err != nil {
			return nil, err
		}
		var ir commonsImageInfoResp
		if err := json.Unmarshal(body, &ir); err != nil {
			return nil, fmt.Errorf("imageinfo decode: %w", err)
		}
		if ir.Error != nil {
			return nil, fmt.Errorf("imageinfo error %s: %s", ir.Error.Code, ir.Error.Info)
		}

		for _, page := range ir.Query.Pages {
			if len(page.ImageInfo) == 0 {
				continue
			}
			ii := page.ImageInfo[0]
			if ii.URL == "" {
				continue
			}
			ext := strings.ToLower(path.Ext(page.Title))

			// B.3.1 Audio mime gate
			if !isAudioMimeClassical(ii.Mime, ext) {
				continue
			}

			// Get format token
			token := classicalFormatToken(ii.Mime, page.Title)
			if token == "" {
				continue
			}

			// B.3.2 Fragment floor — cheap discovery gate (size)
			if ii.Size < minBytes {
				continue
			}

			// B.3.3 License gate
			extFn := func(k string) string { return strings.TrimSpace(string(ii.ExtMetadata[k].Value)) }
			licenseShort := stripHTML(extFn("LicenseShortName"))
			licenseURL := stripHTML(extFn("LicenseUrl"))
			if !p.licenseAllowed(licenseShort) {
				continue
			}

			displayTitle := cleanTitle(page.Title)
			workKey := classicalWorkKey(page.Title)

			cf := core.CandidateFile{URL: ii.URL, Format: token, Bytes: ii.Size}
			cand, ok := groups[workKey]
			if !ok {
				dateRaw := stripHTML(extFn("DateTimeOriginal"))
				artist := stripHTML(extFn("Artist"))

				performer := artist
				if catCtx, has := ctxMap[page.Title]; has {
					if cp := extractPerformerFromCategory(catCtx); cp != "" {
						performer = cp
					}
				}

				_, work, movement := parseClassicalTitle(displayTitle)

				cand = &core.Candidate{
					SourceItem: page.Title,
					WorkKey:    workKey,
					Meta: core.StructuredMeta{
						Title:        displayTitle,
						Work:         work,
						Movement:     movement,
						Composer:     composerName,
						Performer:    performer,
						DateRaw:      dateRaw,
						Year:         parseYear(dateRaw),
						LicenseShort: licenseShort,
						LicenseURL:   licenseURL,
					},
				}
				groups[workKey] = cand
				order = append(order, workKey)
			}
			cand.Files = append(cand.Files, cf)
		}
	}

	out := make([]core.Candidate, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	return out, nil
}

// --- Filtering helpers ---

func isAudioMimeClassical(mime, ext string) bool {
	if strings.HasPrefix(mime, "audio/") && mime != "audio/midi" {
		return true
	}
	if mime == "application/ogg" || mime == "application/x-ogg" {
		return true
	}
	switch ext {
	case ".opus", ".ogg", ".oga", ".flac", ".wav", ".mp3":
		return true
	}
	return false
}

func extToFormat(ext string) string {
	switch ext {
	case ".opus":
		return "opus"
	case ".ogg", ".oga":
		return "ogg"
	case ".flac":
		return "flac"
	case ".wav", ".wave":
		return "wav"
	case ".mp3":
		return "mp3"
	}
	return ""
}

func classicalFormatToken(mime, title string) string {
	t := commonsFormatToken(mime, title)
	if t != "" {
		return t
	}
	ext := strings.ToLower(path.Ext(title))
	return extToFormat(ext)
}

func (p *ClassicalCategoriesProvider) licenseAllowed(short string) bool {
	if short == "" {
		return false
	}
	switch p.LicensePolicy {
	case "attribution":
		return isPDOrCC0(short) || isAttributionLicense(short)
	default:
		return isPDOrCC0(short)
	}
}

func isPDOrCC0(short string) bool {
	s := strings.ToLower(short)
	if s == "cc0" || s == "public domain" {
		return true
	}
	return strings.HasPrefix(s, "pd-")
}

func isAttributionLicense(short string) bool {
	s := strings.ToLower(short)
	if strings.Contains(s, "cc by") {
		return true
	}
	if strings.Contains(s, "oal") {
		return true
	}
	return false
}

func isSkippableSubcat(title string, patterns []string) bool {
	lower := strings.ToLower(title)
	for _, pat := range patterns {
		if strings.Contains(lower, strings.ToLower(pat)) {
			return true
		}
	}
	return false
}

// --- Composer/performer name extraction ---

var composerFromCatRe = regexp.MustCompile(`Category:Audio files of (?:classical )?music by `)

func extractComposerFromCategory(title string) string {
	if s := composerFromCatRe.FindString(title); s != "" {
		return strings.TrimSpace(strings.TrimPrefix(title, s))
	}
	t := strings.TrimPrefix(title, "Category:")
	t = strings.TrimPrefix(t, "Audio files of ")
	if idx := strings.Index(t, "'s "); idx > 0 {
		pre := t[:idx]
		if !strings.Contains(strings.ToLower(pre), "played by") {
			return strings.TrimSpace(pre)
		}
	}
	for _, prefix := range []string{"classical music by ", "music by "} {
		if idx := strings.Index(strings.ToLower(t), prefix); idx >= 0 {
			return strings.TrimSpace(t[idx+len(prefix):])
		}
	}
	return strings.TrimSpace(t)
}

func extractPerformerFromCategory(title string) string {
	lower := strings.ToLower(title)
	idx := strings.Index(lower, "played by")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(title[idx+len("played by"):])
	for _, sep := range []string{",", "(", "["} {
		if i := strings.Index(rest, sep); i > 0 {
			rest = strings.TrimSpace(rest[:i])
		}
	}
	return rest
}

// --- Diacritics-aware work key ---

var diacReplacer = strings.NewReplacer(
	"ä", "a", "ö", "o", "ü", "u", "ß", "ss",
	"á", "a", "à", "a", "â", "a", "ã", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o",
	"ú", "u", "ù", "u", "û", "u",
	"ý", "y", "ÿ", "y",
	"ñ", "n", "ç", "c",
	"č", "c", "š", "s", "ž", "z",
	"ř", "r", "ď", "d", "ť", "t", "ň", "n",
	"ě", "e", "ů", "u",
	"ł", "l", "ø", "o", "å", "a", "æ", "ae", "œ", "oe",
	"ć", "c", "ś", "s", "ź", "z", "ń", "n",
	"đ", "d", "ħ", "h",
	"ğ", "g", "ş", "s", "ı", "i",
)

func classicalWorkKey(title string) string {
	t := cleanTitle(title)
	t = fmtWordRe.ReplaceAllString(t, "")
	t = strings.ToLower(t)
	t = diacReplacer.Replace(t)
	t = norm.NFKD.String(t)
	var b strings.Builder
	for _, r := range t {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ' ' {
			b.WriteRune(r)
		}
	}
	t = b.String()
	t = strings.Join(strings.Fields(t), " ")
	return t
}
