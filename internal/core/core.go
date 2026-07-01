// Package core holds shared domain types used across packages. It has no
// internal dependencies to keep the import graph acyclic.
package core

import "strings"

// Status values carried by a track row through the pipeline.
const (
	StatusDiscovered  = "discovered"
	StatusDownloading = "downloading"
	StatusDownloaded  = "downloaded"
	StatusConverting  = "converting"
	StatusConverted   = "converted"
	StatusPackaging   = "packaging"
	StatusPackaged    = "packaged"
	StatusCleaning    = "cleaning"
	StatusDone        = "done"
	StatusFailed      = "failed"
	StatusSkipped     = "skipped"
	// StatusDuplicate marks a done track whose audio duplicates a canonical
	// track (see DupOf). Deduped rows are retained for traceability but excluded
	// from playable/browse/upload sets.
	StatusDuplicate = "duplicate"
)

// TransientStatuses are the in-flight states reset to their input status on
// startup after a crash.
var TransientStatuses = map[string]string{
	StatusDownloading: StatusDiscovered,
	StatusConverting:  StatusDownloaded,
	StatusPackaging:   StatusConverted,
	StatusCleaning:    StatusPackaged,
}

// CandidateFile is one format variant of a single recording.
type CandidateFile struct {
	URL    string
	Format string // opus|ogg|wav|mp3|other
	Bytes  int64
}

// StructuredMeta is best-effort descriptive metadata for a recording.
type StructuredMeta struct {
	Title        string
	Work         string
	Movement     string
	Composer     string
	Performer    string
	Album        string
	Year         int
	DateRaw      string
	LicenseShort string
	LicenseURL   string
}

// Candidate is a discovered recording with one or more format variants.
type Candidate struct {
	SourceItem string
	WorkKey    string
	Files      []CandidateFile
	Meta       StructuredMeta
}

// Track is a row in the tracks table.
type Track struct {
	ID             string
	Source         string
	SourceItem     string
	Title          string
	Work           string
	Movement       string
	Composer       string
	Performer      string
	Album          string
	Year           int
	DateRaw        string
	DurationSec    float64
	OriginalURL    string
	OriginalFormat string
	OriginalCodec  string
	OriginalBytes  int64
	OriginalSHA1   string
	OpusPath       string
	OpusBytes      int64
	CafPath        string
	CafBytes       int64
	LicenseShort   string
	LicenseURL     string
	SearchText     string
	Status         string
	Worker         string
	Attempts       int
	StageError     string

	// Enrichment fields (populated by the `enrich` pass; may be empty).
	ComposerCanonical string
	WorkID            string
	WorkTitle         string
	Catalog           string
	MovementIndex     int
	MovementTitle     string
	DisplayTitleText  string
	ComposerCorrected bool

	// Dedup fields (populated by the `dedup` pass; may be empty).
	AudioFingerprint string
	DupOf            string
}

// Work is a grouped musical work (e.g. a symphony) that one or more tracks
// (its movements) belong to.
type Work struct {
	ID                string
	ComposerCanonical string
	Title             string
	Catalog           string
	SortKey           string
	TrackCount        int
}

// DisplayContext selects how much surrounding metadata DisplayTitle should
// include, so callers don't repeat information the UI already shows.
type DisplayContext int

const (
	// DisplayGlobal is a fully-qualified label (composer + work + movement),
	// used for search results and flat lists.
	DisplayGlobal DisplayContext = iota
	// DisplayComposer omits the composer (already shown by the browse column).
	DisplayComposer
	// DisplayWork shows only the movement (composer + work already shown).
	DisplayWork
)

// DisplayTitle renders a context-aware label for a track that avoids repeating
// information the surrounding UI already provides. It prefers the enriched
// composer/work/movement fields and falls back to the raw title when those are
// absent, so it is safe to call on un-enriched rows.
func DisplayTitle(t *Track, ctx DisplayContext) string {
	composer := firstNonEmpty(t.ComposerCanonical, t.Composer)
	work := firstNonEmpty(t.WorkTitle, t.Work)
	if work != "" && t.Catalog != "" && !containsFold(work, t.Catalog) {
		work = work + ", " + t.Catalog
	}
	movement := t.MovementTitle
	rawFallback := firstNonEmpty(t.Title, t.Work, t.SourceItem, t.ID)

	switch ctx {
	case DisplayWork:
		if movement != "" {
			return movement
		}
		// No structured movement: show whatever distinguishes this track.
		return rawFallback
	case DisplayComposer:
		body := joinParts(work, movement)
		if body != "" {
			return body
		}
		return rawFallback
	default: // DisplayGlobal
		if composer == "" && work == "" && movement == "" {
			// Un-enriched: keep the legacy "[source] title" / "Composer — title".
			if t.Composer != "" {
				return t.Composer + " — " + rawFallback
			}
			return "[" + t.Source + "] " + rawFallback
		}
		body := joinParts(work, movement)
		if body == "" {
			body = rawFallback
		}
		if composer != "" {
			return composer + " — " + body
		}
		return body
	}
}

func joinParts(work, movement string) string {
	switch {
	case work != "" && movement != "":
		return work + " · " + movement
	case work != "":
		return work
	default:
		return movement
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func containsFold(haystack, needle string) bool {
	return needle != "" && strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// ProgressMsg is emitted by workers to the TUI/headless reporter.
type ProgressMsg struct {
	Source     string
	Stage      string
	TrackID    string
	BytesDelta int64
	Completed  bool
	Failed     bool
	Skipped    bool
	Err        string
}
