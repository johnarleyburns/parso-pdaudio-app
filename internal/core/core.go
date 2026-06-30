// Package core holds shared domain types used across packages. It has no
// internal dependencies to keep the import graph acyclic.
package core

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
