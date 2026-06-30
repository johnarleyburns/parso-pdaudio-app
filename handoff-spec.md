# Handoff Spec — `parso-pdaudio` (working title)

**A Go + Bubble Tea TUI that downloads public-domain classical recordings from a fixed set of
sources, normalizes everything to Opus, packages an iOS-ready CAF alongside it, and indexes rich,
searchable metadata in a single SQLite database — all in one flat directory.**

Status: implementation-ready. All external APIs and the ffmpeg pipeline below were verified against
live endpoints / a real ffmpeg 6.1.1 before this doc was written. Treat the "VERIFIED" notes as
ground truth; do not substitute remembered API shapes.

Target implementer: a coding agent. Be literal. Where this doc gives an exact command or query, use
it verbatim.

---

## 0. Verified facts you can rely on

1. **IA Metadata API** `GET https://archive.org/metadata/{id}` returns JSON with `server`, `dir`, and
   `files[]`; each file has `name`, `format` (e.g. `"Ogg Vorbis"`, `"VBR MP3"`, `"Apple Lossless
   Audio"`), `size` (bytes, string), `length` (`mm:ss` or seconds), and often `track`/`title`/
   `album`/`artist`. Non-existent identifiers return `{}` (2 bytes) with HTTP 200 — **never** infer
   existence from the status code; check for a non-empty `files` array.
2. **Commons MediaWiki API** enumerates audio via `list=search` (namespace 6, `filetype:audio`) and
   resolves direct download URLs + license via `prop=imageinfo&iiprop=url|mime|size|extmetadata`.
3. **ffmpeg pipeline (VERIFIED on ffmpeg 6.1.1 with libopus + caf muxer):**
   - any audio → Ogg Opus: `-c:a libopus` works.
   - **Opus → CAF is a lossless stream copy**: `ffmpeg -i in.opus -c:a copy out.caf` produces a CAF
     whose audio stream is genuinely `opus` (confirmed via ffprobe), identical payload bytes, no
     re-encode.
   - If a source is already Opus, the convert step is `-c:a copy` (no quality loss, no CPU).
4. **iOS supports Opus inside CAF** (per Apple developer forums: Opus is supported in the `.caf`
   container on iOS/Safari, though not in `.mp4`). CAF is therefore the correct iOS wrapper.
   *Caveat:* generic Opus→CAF output has historically had iOS/Safari packet-framing quirks; a
   purpose-built pure-Go converter exists (`github.com/nabil6391/opus_caf_converter`). Use it as the
   primary packager (§7.3) and validate playback on a real device early.

---

## 1. Goal & scope

Build a single statically-linkable Go executable (`parso-pdaudio`) that:

- Pulls recordings from the **ten sources** in §2.
- For each track: downloads the best-available native file (format preference **opus > ogg > wav >
  mp3**), converts it to a canonical `.opus`, packages a sibling `.caf`, then deletes the native
  download (keeping only `.opus` + `.caf`).
- Stores everything — the SQLite DB and all media files — in **one flat directory**.
- Records structured metadata (title, composer, work, source, date, performer, license, durations,
  byte sizes, hashes, original-format provenance) **and** a stopword-filtered keyword index for
  easy search, even after the native source file is deleted.
- Presents a **Bubble Tea TUI**: a per-source dashboard (counts + % + ETA), a combined searchable
  track list, and controls to scale each worker pool.

The whole job is resumable: kill it, restart it, and it continues. State lives in the DB.

---

## 2. Source registry (the manifest) — READ THIS FIRST

This is the honest, verified source reality. The implementer bakes this table into a `sources`
registry (code or a small embedded JSON). Counts are approximate live results and will drift.

| key | provider | locator | native formats present | license | notes |
|---|---|---|---|---|---|
| `chopin` | `ia` | item `musopen-chopin` | **Ogg Vorbis**, VBR MP3, Apple Lossless | CC0 (`publicdomain/zero/1.0`) | 82 ALAC originals → 104 derived audio files. Clean. Preference picks the **Ogg Vorbis** derivative. |
| `bach_wtc1` | `ia` | item `bach-well-tempered-clavier-book-1` | Ogg Vorbis, MP3, FLAC, WAV | PD | Well-Tempered Clavier Book 1, multiple performers, public domain recordings. |
| `goldberg` | `ia` | item `The_Open_Goldberg_Variations-11823` | Ogg Vorbis, MP3, FLAC | CC0 | Kimiko Ishizaka's Open Goldberg Variations, CC0 dedicated. |
| `beethoven_pitman` | `commons` | search `"Beethoven" "Pitman" filetype:audio` (Paul Pitman, 32 sonatas) | **Ogg Vorbis** (`.ogg`) | PD | **Not on IA** (all plausible identifiers return `{}`). Commons only, Ogg. ~60–70 candidate files; filter to the Pitman performances (see §6.2 dedup/grouping). |
| `marine` | `commons` | search `"United States Marine Band" filetype:audio` | mostly Ogg/Opus/WAV, some MP3 | PD-USGov (mostly) | ~523 audio hits; includes false positives — filter by `audio/*` mime + dedup by work. |
| `army` | `commons` | search `"United States Army Band" filetype:audio` | mixed | PD-USGov (mostly) | ~197 audio hits. |
| `navy` | `commons` | search `"United States Navy Band" filetype:audio` | mixed | PD-USGov (mostly) | ~594 audio hits. |
| `airforce` | `commons` | search `"United States Air Force Band" filetype:audio` | mixed | PD-USGov (mostly) | ~580 audio hits. |
| `coastguard` | `commons` | search `"United States Coast Guard Band" filetype:audio` | mixed | PD-USGov (mostly) | ~43 audio hits. |
| `spaceforce` | `commons` | search `"United States Space Force Band" filetype:audio` | mixed | PD-USGov (mostly) | **~2 audio hits — effectively empty.** Band founded 2020; almost no open corpus exists. Include for completeness; expect ~0 and do not treat zero results as an error. |

Why no official-band-site provider: the bands' official sites (e.g. `marineband.marines.mil`) return
**HTTP 403 to scripted requests** (bot protection) and expose no JSON listing. Wikimedia Commons is
their machine-enumerable PD home. Do not attempt to scrape the official sites.

Why no Musopen provider: Musopen performer pages are client-rendered JS shells with no server-side
track data, and downloading from Musopen is rate-limited (the thing we're routing around). Commons +
IA cover the same recordings.

**License handling:** mixed-license is expected. Capture per-track license (IA `metadata.licenseurl`;
Commons `imageinfo.extmetadata.LicenseShortName` / `LicenseUrl`). Store it. Provide a config flag
`--require-license=cc0,pd,pd-usgov` that, when set, skips tracks whose captured license is not in the
allowlist (default: allow all, but always record what was found).

---

## 3. Architecture overview

A staged pipeline. Each stage is a worker pool. Work is handed between stages **through the SQLite
DB** (the DB is the queue), not in-memory channels — this gives free resume and crash-safety.

```
                ┌─────────────────────────────────────────────────────────────┐
 discover  ──▶  │  tracks rows: status = 'discovered'                          │
 (per source)   └─────────────────────────────────────────────────────────────┘
                          │ claim                ▲ result write
                          ▼                       │
   [download workers] ── download native ──▶ status='downloaded' (orig_path,fmt,bytes,sha1)
                          │
                          ▼
   [convert workers]  ── ffmpeg → .opus  ──▶ status='converted'  (opus_path,bytes,duration)
                          │
                          ▼
   [package workers]  ── opus → .caf     ──▶ status='packaged'   (caf_path,bytes)
                          │
                          ▼
   [cleaner workers]  ── delete native   ──▶ status='done'       (orig_path cleared on disk; record kept)
```

Each worker loop:
1. Atomically **claim** one row in its input status (UPDATE … RETURNING, §7.0).
2. Do the work (network / ffmpeg / fs).
3. Write the result row (new status + fields) or mark `failed` with `attempts++` and `stage_error`.
4. Emit a progress message to the TUI (via a results channel, §9.4).

`discover` runs once per source on startup (and on a manual "refresh" key), inserting/upserting
`discovered` rows. It is not a long-lived pool.

Terminal states: `done` (success), `failed` (after max attempts), `skipped` (no preferred format /
license filtered / dedup loser).

---

## 4. Directory layout & file naming

One flat directory, chosen by `--dir` (default `./library`):

```
library/
  library.db          library.db-wal   library.db-shm
  01J9Z....opus        01J9Z....caf     01J9Z....src.ogg   ← .src.* is transient, deleted by cleaner
  01J9Z2...opus        01J9Z2...caf
  ...
```

- Basename = a **ULID** (`github.com/oklog/ulid/v2`) generated at discover time, stored as
  `tracks.id`. ULIDs are collision-free and lexically sortable — ideal for a flat dir.
- `{id}.src.{ext}` = native download (ext from chosen format: `ogg`/`opus`/`wav`/`mp3`). Deleted by
  the cleaner once `.opus` + `.caf` exist.
- `{id}.opus`, `{id}.caf` = the kept artifacts.
- Human-readable identity (title/composer/work) lives only in the DB, never in filenames. Lookups go
  through the DB.

---

## 5. SQLite schema

Open the DB **with WAL and a busy timeout** so concurrent stage workers don't hit `SQLITE_BUSY`:

DSN (modernc.org/sqlite): `file:library.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(on)`

```sql
-- one row per track, carried through the whole pipeline
CREATE TABLE IF NOT EXISTS tracks (
    id              TEXT PRIMARY KEY,         -- ULID; also the file basename
    source          TEXT NOT NULL,            -- 'chopin' | 'marine' | ...
    source_item     TEXT,                     -- IA identifier or Commons File: title
    -- structured metadata (nullable; populate what is reliably known)
    title           TEXT,
    work            TEXT,                     -- e.g. "Piano Sonata No. 8, Op. 13 'Pathétique'"
    movement        TEXT,                     -- e.g. "III. Rondo - Allegro"
    composer        TEXT,
    performer       TEXT,                     -- ensemble / soloist
    album           TEXT,
    year            INTEGER,                  -- best-effort 4-digit year
    date_raw        TEXT,                     -- whatever the source gave, unparsed
    duration_sec    REAL,
    -- provenance of the pre-conversion source (kept even after the file is deleted)
    original_url    TEXT NOT NULL,
    original_format TEXT,                     -- 'ogg' | 'opus' | 'wav' | 'mp3'
    original_codec  TEXT,                     -- ffprobe codec_name: 'vorbis','opus','pcm_s16le','mp3'
    original_bytes  INTEGER,
    original_sha1   TEXT,
    -- canonical kept artifacts
    opus_path       TEXT,                     -- relative filename, e.g. '01J9Z....opus'
    opus_bytes      INTEGER,
    caf_path        TEXT,
    caf_bytes       INTEGER,
    -- license
    license_short   TEXT,                     -- 'CC0','PD-USGov','Public domain', ...
    license_url     TEXT,
    -- normalized search blob (stopwords removed, lowercased) — see §8
    search_text     TEXT,
    -- pipeline state
    status          TEXT NOT NULL DEFAULT 'discovered',
                    -- discovered|downloading|downloaded|converting|converted
                    -- |packaging|packaged|cleaning|done|failed|skipped
    worker          TEXT,                     -- id of the worker currently holding the row
    attempts        INTEGER NOT NULL DEFAULT 0,
    stage_error     TEXT,
    created_at      INTEGER NOT NULL,         -- unix seconds
    updated_at      INTEGER NOT NULL,
    UNIQUE(source, original_url)              -- idempotent discovery
);

CREATE INDEX IF NOT EXISTS idx_tracks_status      ON tracks(status);
CREATE INDEX IF NOT EXISTS idx_tracks_source      ON tracks(source);
CREATE INDEX IF NOT EXISTS idx_tracks_src_status  ON tracks(source, status);

-- explicit keyword lookup (stopwords removed). J asked for this specifically.
CREATE TABLE IF NOT EXISTS track_keyword (
    track_id  TEXT NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    keyword   TEXT NOT NULL,
    PRIMARY KEY (track_id, keyword)
);
CREATE INDEX IF NOT EXISTS idx_keyword ON track_keyword(keyword);

-- ranked full-text search (modernc.org/sqlite ships FTS5). body = stopword-stripped blob.
CREATE VIRTUAL TABLE IF NOT EXISTS tracks_fts USING fts5(
    body,
    track_id UNINDEXED,
    tokenize = 'unicode61 remove_diacritics 2'
);

-- bookkeeping for resumable discovery / pagination cursors
CREATE TABLE IF NOT EXISTS source_state (
    source        TEXT PRIMARY KEY,
    last_cursor   TEXT,        -- IA page or Commons sroffset/continue token
    discovered_at INTEGER,
    total_known   INTEGER      -- best-effort total track count for % math
);
```

Notes:
- `UNIQUE(source, original_url)` makes re-running discover idempotent (`INSERT … ON CONFLICT DO
  NOTHING`, or `DO UPDATE` to refresh metadata).
- Keep both `track_keyword` (exact term lookup / faceting) and `tracks_fts` (ranked `MATCH`
  queries). They're cheap and serve different UX needs.

---

## 6. Providers (exact enumeration recipes)

A provider implements:

```go
type Candidate struct {
    SourceItem string             // IA id or Commons File: title
    WorkKey    string             // normalized grouping key for dedup (lowercased title sans ext/fmt hints)
    Files      []CandidateFile    // one or more format variants of the SAME recording
    Meta       StructuredMeta     // title, composer, work, performer, album, date, license...
}
type CandidateFile struct {
    URL    string
    Format string                 // 'opus'|'ogg'|'wav'|'mp3'|other
    Bytes  int64
}
type Provider interface {
    Discover(ctx context.Context, cur string) (cands []Candidate, nextCur string, done bool, err error)
}
```

After discovery, for each `Candidate`: pick the single `CandidateFile` whose `Format` ranks highest
in the preference list (`opus>ogg>wav>mp3`); if none of the present formats are in the list, mark the
resulting track `skipped` (configurable to fall back to any audio). Insert one `tracks` row per
chosen candidate.

### 6.1 `ia` provider

```
GET https://archive.org/metadata/{identifier}
User-Agent: parso-pdaudio/1.0 (+contact)
```

- Existence check: `len(resp.files) > 0`.
- Group `files[]` by a stripped track key (filename without extension). Within a group, map IA
  `format` → our format token:
  `"Ogg Vorbis"→ogg`, `"VBR MP3"→mp3`, `"Apple Lossless Audio"→other(skip-by-default)`,
  `"Opus"→opus` (rare), `"WAVE"/"WAV"→wav`.
- Build the download URL as
  `https://archive.org/download/{identifier}/{pathEscape(file.name)}` (use `url.PathEscape` on the
  filename segment — names contain spaces/parens/accents).
- Metadata per file: `title`, `track`, `album`, `artist` when present; item-level
  `metadata.creator`, `metadata.date`, `metadata.licenseurl`. Composer/work for `chopin` are best
  parsed from the title (`metadata.creator` is "Aaron Dunn" the producer, **not** the composer — for
  this item composer is Chopin; set `composer='Frédéric Chopin'` for the whole item).

### 6.2 `commons` provider

Enumerate (paginate with `sroffset`, or follow `continue.sroffset`; page size 50):

```
GET https://commons.wikimedia.org/w/api.php
  ?action=query&list=search
  &srsearch={URLEncode(`"<BAND NAME>" filetype:audio`)}
  &srnamespace=6&srlimit=50&sroffset={N}
  &format=json&maxlag=5
User-Agent: parso-pdaudio/1.0 (your-contact)   ← Wikimedia REQUIRES a descriptive UA
```

Resolve URLs + license + mime in batches of up to 50 titles:

```
GET https://commons.wikimedia.org/w/api.php
  ?action=query&titles={URLEncode(pipe-joined File: titles)}
  &prop=imageinfo&iiprop=url|mime|size|extmetadata
  &format=json&maxlag=5
```

- Keep only pages whose `imageinfo[0].mime` starts with `audio/`. Drop PDFs, `.webm` video, etc.
- Map mime/extension → format token: `audio/ogg`+`.ogg/.oga`→`ogg`; `audio/ogg`+`.opus` or
  `audio/opus`→`opus`; `audio/x-wav`/`audio/wav`→`wav`; `audio/mpeg`→`mp3`. When the container is
  ambiguous (`.oga`), trust ffprobe's `codec_name` at convert time and store it in `original_codec`.
- **Dedup / format preference:** the same recording frequently appears in multiple formats (e.g.
  `… America the Beautiful ….oga` **and** `….wav`). Compute `WorkKey` = lowercased title with the
  extension and trailing format words removed, collapse whitespace. Group by `WorkKey`; emit ONE
  candidate per group carrying all variants; the selector then picks the best format. Mark the others
  `skipped` (or simply don't insert them).
- Metadata from `extmetadata`: `ObjectName`/page title → `title`; `Artist` → `performer`;
  `DateTimeOriginal` → `date_raw` (+ parse `year`); `LicenseShortName` → `license_short`;
  `LicenseUrl` → `license_url`. Composer/work parsed best-effort from the title (these classical
  titles follow `Composer, Work, Movement` or `Work - Performer` patterns — write a light heuristic;
  leave fields null when unsure, but always feed the raw title into keywords/FTS).
- Precision tip (optional refinement): instead of free-text search you may resolve each band's real
  Commons **category** and traverse `list=categorymembers` recursively. The exact category
  `Category:Audio files by the United States Marine Band` is empty (wrong name), so do a
  `srnamespace=14` search for the band to find the right category root first. Search-based
  enumeration (above) is the verified baseline; category traversal is a quality upgrade if false
  positives are a problem.

---

## 7. Pipeline workers

### 7.0 Atomic claim (the DB-as-queue primitive)

Every stage claims work with one statement (SQLite ≥3.35 `RETURNING`, supported by
modernc.org/sqlite). Example for the download stage:

```sql
UPDATE tracks
   SET status='downloading', worker=:wid, updated_at=unixepoch()
 WHERE id = (SELECT id FROM tracks
              WHERE status='discovered'
              ORDER BY created_at
              LIMIT 1)
RETURNING id, source, original_url, original_format;
```

Run inside `BEGIN IMMEDIATE … COMMIT`. With `busy_timeout=5000` concurrent claims serialize and
retry transparently. If no row is returned, the worker sleeps briefly (e.g. 250 ms) and retries, or
parks until the TUI signals new work. Map each stage's input/output status:

| worker pool | claims status | sets on success |
|---|---|---|
| download | `discovered` → `downloading` | `downloaded` |
| convert  | `downloaded` → `converting` | `converted` |
| package  | `converted` → `packaging`   | `packaged` |
| cleaner  | `packaged` → `cleaning`     | `done` |

On failure: `status='failed'`, `attempts=attempts+1`, `stage_error=<msg>`. A `--retry-failed` action
(and a TUI key) resets `failed` rows whose `attempts < maxAttempts` back to the prior input status.

### 7.1 Download worker

- HTTP GET `original_url` with `context`, descriptive UA, and a timeout. Stream to
  `{dir}/{id}.src.{ext}` (ext from `original_format`). Compute SHA-1 while streaming.
- Report progress per chunk for the **byte-level** ETA (write throughput samples to the TUI channel,
  §9.4). Bytes total for the source come from candidate `Bytes` / IA `size` / Commons `size`.
- On success set `original_bytes`, `original_sha1`, `status='downloaded'`.
- Retries: up to 3 with exponential backoff on 5xx/timeouts. 404 → `failed` (no retry).
- Politeness: cap **Commons** concurrent downloads low (≤4) and honor `maxlag`; IA tolerates a few
  parallel streams. Make per-host concurrency configurable.

### 7.2 Convert worker → `.opus`

Detect the source codec first:

```
ffprobe -v error -select_streams a:0 -show_entries stream=codec_name -of default=nw=1 {src}
ffprobe -v error -show_entries format=duration -of default=nw=1 {src}     # seconds (float)
```

Then:

- If `codec_name == "opus"` → **stream copy** (no re-encode, no quality loss):
  ```
  ffmpeg -hide_banner -loglevel error -y -i {src} -map_metadata 0 -c:a copy {id}.opus
  ```
- Else (vorbis / pcm_* / mp3 / etc.) → **encode** with libopus:
  ```
  ffmpeg -hide_banner -loglevel error -y -i {src} -map_metadata 0 \
         -c:a libopus -b:a {opusBitrateK}k -vbr on {id}.opus
  ```
  Default `opusBitrateK=128` (music). Make it a flag.

Record `opus_bytes`, `duration_sec`, `status='converted'`. (VERIFIED: vorbis→opus and wav→opus both
succeed on ffmpeg 6.1.1.)

### 7.3 Package worker → `.caf`

Primary (pure Go, iOS-safe framing, no extra ffmpeg call):

```go
import caf "github.com/nabil6391/opus_caf_converter/caf"
err := caf.ConvertOpusToCaf(opusPath, cafPath)   // confirm exact exported name against the repo
```

Fallback (VERIFIED lossless stream copy):

```
ffmpeg -hide_banner -loglevel error -y -i {id}.opus -c:a copy {id}.caf
```

Record `caf_bytes`, `status='packaged'`. Add a `--packager=go|ffmpeg` flag (default `go`).
**Acceptance gate:** the first packaged file must play in an `AVAudioPlayer` smoke test on a device
or simulator before trusting the batch (see §14).

### 7.4 Cleaner worker

- Verify `{id}.opus` and `{id}.caf` both exist and are non-zero. **Only then** `os.Remove` the
  `{id}.src.*` native file. Never delete opus/caf. Set `status='done'`.
- Config `--keep-source` skips deletion (keeps `.src.*` for debugging) but still advances to `done`.
- The DB row already carries `original_url/format/codec/bytes/sha1`, so provenance survives deletion.

### 7.5 Metadata + search indexing

Run keyword/FTS indexing at the convert step (when title/composer/work are finalized), or as a tiny
final touch in the cleaner. Build `search_text` and keywords as in §8, then:

```sql
INSERT INTO tracks_fts(body, track_id) VALUES (:blob, :id);
-- and per keyword:
INSERT OR IGNORE INTO track_keyword(track_id, keyword) VALUES (:id, :kw);
```

---

## 8. Metadata extraction & search

**Keyword build (per track):**

1. Concatenate the meaningful text fields: `title`, `work`, `movement`, `composer`, `performer`,
   `album`, plus `source`.
2. Lowercase; Unicode-normalize (NFKD) and strip diacritics so "Pathétique" matches "pathetique"
   (FTS5 `remove_diacritics 2` covers the FTS side; do the same for `track_keyword`).
3. Tokenize on non-alphanumerics. Drop tokens of length 1. Drop pure punctuation.
4. **Remove stopwords** (starter English set — tune later; deliberately keep classical-meaningful
   words like *major, minor, sharp, flat, sonata, op, no*):
   ```
   a an and are as at be but by for from had has have he her his in into is it
   its of on or that the their then there these they this to was were will with
   ```
5. Dedupe → `track_keyword` rows; join with spaces → `search_text` and `tracks_fts.body`.

**Search queries the app/TUI can run:**

- Ranked: `SELECT track_id FROM tracks_fts WHERE body MATCH :q ORDER BY rank;` (supports
  `pathetique`, `chopin ballade`, `beethoven NEAR sonata`, prefix `chop*`).
- Exact term / facet: `SELECT track_id FROM track_keyword WHERE keyword = :kw;`
- Join back to `tracks` for display.

---

## 9. TUI spec (Bubble Tea)

Charm stack: `bubbletea`, `bubbles` (table, progress, viewport, spinner, textinput), `lipgloss`.

### 9.1 Layout (single full-screen model)

```
┌ parso-pdaudio ─────────────────────────────────────────────── workers D:4 C:2 P:1 X:1 ┐
│ SOURCES                                                                                 │
│ source        disc  dl  conv  pkg  done  fail   %done   ETA                             │
│ chopin         104  104  104  101  101    0    97.1%   00:00:12                          │
│ marine         523  140   96   80   80    2    15.3%   01:42:30                          │
│ navy           594  ...                                                                  │
│ ...                                                                                      │
│ ───────────────────────────────────────────────────────────────────────────────────── │
│ TOTAL         1.. │ overall ████████░░░░░░░░  41.2% │ ETA 02:15:04 │ 3.1 MB/s            │
├─ TRACKS (filter: "chopin ballade") ────────────────────────────────────────────────────┤
│ ● Chopin — Ballade No. 1, Op. 23           done    opus 3.2MB  caf 3.2MB                 │
│ ◐ Chopin — Ballade No. 2, Op. 38           converting                                   │
│ ...                                                                                      │
├─ keys: [s]tart [p]ause  [d/c/k/x]+/-: scale pool  [/]search  [r]efresh  [R]etry  [q]uit ┤
└─────────────────────────────────────────────────────────────────────────────────────────┘
```

- **Top pane:** a `bubbles/table` (or hand-rolled) with one row per source showing counts by stage
  (`discovered, downloading+downloaded, converted, packaged, done, failed`), `%done`, and per-source
  ETA.
- **Total bar:** a `bubbles/progress` bar = total `done` / total `discovered`, plus aggregate ETA and
  live throughput.
- **Bottom pane:** combined track list (`viewport` over a slice, or a second table) with a live
  status glyph per track. A `textinput` filter (`/`) runs the FTS query in §8 and narrows the list.
- **Controls:** `s`/`p` start/pause all pools; `d/c/k/x` select a pool and `+`/`-` to change its
  goroutine count live (download/convert/package/cleaner); `r` re-run discovery; `R` reset failed;
  `q` quit (graceful: cancel ctx, let in-flight ffmpeg finish or kill, flush DB).

### 9.2 Counts

Maintain in-memory per-source counters updated from worker messages, and reconcile periodically with
a cheap `SELECT source, status, COUNT(*) FROM tracks GROUP BY source, status;` (e.g. every 2 s) so
the dashboard self-heals after a restart.

### 9.3 Progress % and ETA math

- **%done (per source):** `done / max(discovered,1)`. Total: `Σdone / Σdiscovered`.
- **ETA:** keep an EWMA of completion rate. Two complementary signals:
  - byte throughput from the download stage (`bytesDone/sec`) for a near-term "MB/s" readout and a
    download-bound ETA = `remainingBytes / rate`;
  - track completion rate (`tracksDone/sec`, EWMA α≈0.2 over ~10 s) for the headline ETA =
    `remainingTracks / rate`.
  Use track-completion ETA as the displayed per-source/total ETA (it captures convert+package CPU
  time, which byte-rate alone misses). Guard divide-by-zero → show `--:--` until a rate exists.

### 9.4 Worker↔TUI messaging (the part cheap models get wrong)

`tea.Program` updates the model on a single goroutine. Workers must **not** touch the model. Pattern:

```go
type progressMsg struct {
    Source string; Stage string; TrackID string
    BytesDelta int64; Completed bool; Failed bool; Err string
}
results := make(chan progressMsg, 1024)        // workers send here

func waitForActivity(ch <-chan progressMsg) tea.Cmd {
    return func() tea.Msg { return <-ch }      // blocks; returns one msg
}
// In Update(): handle the msg, then RE-ISSUE the command to keep listening:
case progressMsg:
    m.apply(msg)
    return m, waitForActivity(results)
```

Workers run as plain goroutines launched from `main` (not from `Init`), holding `results` and the
`*sql.DB`. Scaling a pool = start/stop goroutines for that stage; track counts in the model and
adjust on `+`/`-`. A `time.Tick` command drives the 2 s DB reconciliation and ETA recompute.

---

## 10. Concurrency & DB-write discipline

- modernc.org/sqlite + WAL allows many readers + one writer; `busy_timeout` makes the serialized
  writes from multiple worker pools safe. Keep each write a short single statement/transaction.
- If `SQLITE_BUSY` still appears under high worker counts, funnel all writes through **one dedicated
  DB-writer goroutine** consuming a `chan dbOp`; workers send ops, writer executes serially. Start
  with busy_timeout; add the writer goroutine only if needed. Document which you chose.
- ffmpeg/ffprobe are external processes via `os/exec` with the job's `context` (so quit/cancel kills
  them). Bound total concurrent ffmpeg processes (convert+package) by CPU (e.g. default
  `min(NumCPU, 4)`).

---

## 11. Tech stack & dependencies

```
module github.com/johnarleyburns/parso-pdaudio      // adjust to taste
go 1.22+

require (
    github.com/charmbracelet/bubbletea
    github.com/charmbracelet/bubbles
    github.com/charmbracelet/lipgloss
    modernc.org/sqlite                               // pure Go, CGO-free, ships FTS5
    github.com/oklog/ulid/v2
    github.com/nabil6391/opus_caf_converter          // pure-Go Opus→CAF (verify import path/API)
)
```

- **Pure-Go SQLite (`modernc.org/sqlite`)** is chosen deliberately: CGO-free → trivial static builds
  and cross-compilation, matching the "single Go executable" goal. (If FTS5 turns out unavailable in
  the pinned version, fall back to `mattn/go-sqlite3` with `-tags fts5`, accepting CGO.)
- **ffmpeg + ffprobe are runtime dependencies** (not Go libs). Detect both at startup
  (`exec.LookPath`); if missing, print an actionable error and disable convert/package stages (still
  allow download). Document the requirement in the README.

---

## 12. Config & CLI

```
parso-pdaudio [flags]

--dir string            output directory (DB + media)         default "./library"
--sources string        comma list or "all"                   default "all"
--prefer string         format preference, high→low           default "opus,ogg,wav,mp3"
--allow-fallback        if no preferred format, take any audio default false
--require-license string allowlist; skip others; "" = allow all  default ""
--opus-bitrate int      libopus VBR target kbps                default 128
--packager string       "go" | "ffmpeg"                        default "go"
--keep-source           don't delete native after packaging    default false
--dl-workers int        initial download pool size             default 4
--conv-workers int      initial convert pool size              default 2
--pkg-workers int       initial package pool size              default 1
--clean-workers int     initial cleaner pool size              default 1
--max-attempts int      per-stage retry ceiling                default 3
--commons-concurrency int  max parallel Commons GETs           default 4
--no-tui                headless mode (log progress)           default false
```

Headless mode (`--no-tui`) runs the same pipeline and prints periodic progress lines — useful for
unattended bulk pulls and CI.

---

## 13. Networking etiquette, retries, resume

- **User-Agent:** always descriptive with contact (Wikimedia requires it; IA appreciates it).
- **Commons `maxlag=5`** on every API call; on a `maxlag` error, back off per the `Retry-After`
  header.
- **Retries:** exponential backoff (e.g. 1s,2s,4s) on 429/5xx/timeouts; give up after `--max-attempts`
  and mark `failed`.
- **Resume:** everything is in the DB. On startup, any rows stuck in a transient status
  (`downloading/converting/packaging/cleaning`) from a previous crash are reset to their input status
  (`discovered/downloaded/converted/packaged`) before workers start. `source_state.last_cursor` lets
  discovery resume mid-pagination.
- **Idempotency:** `UNIQUE(source, original_url)` + "skip if `{id}.opus` already exists and row is
  `done`" make re-runs safe and cheap.

---

## 14. Acceptance criteria

1. `parso-pdaudio --sources chopin` produces, in `./library`, a `library.db` plus `{id}.opus` and
   `{id}.caf` for ~100 Chopin tracks, with **no `.src.*` files left** and every row `status='done'`.
2. ffprobe on any produced `.caf` reports `codec_name=opus`.
3. **On-device/simulator smoke test:** an `AVAudioPlayer` (or `AVAudioFile`) loads and plays a
   produced `.caf` end-to-end on iOS. (This is the gate that validates the packager choice; do it on
   the first batch.)
4. `SELECT count(*) FROM tracks WHERE source='spaceforce';` returns ~0 and the run does **not** error
   on the empty source.
5. FTS works: `… tracks_fts WHERE body MATCH 'chopin ballade'` returns the Ballade rows; a stopword
   like `the` is absent from `track_keyword`.
6. License is recorded for every `done` track (`license_short` non-null where the source provided it).
7. Kill the process mid-run and restart: it resumes, no duplicate files, no rows wedged in a
   transient status.
8. TUI: per-source counts, %s, and ETAs update live; `+`/`-` visibly changes a pool's worker count;
   `/` filters the track list via FTS.
9. Provenance survives cleanup: after `done`, `original_url`/`original_format`/`original_sha1` are
   still populated though the `.src.*` file is gone.

---

## 15. Known limitations & decisions log

- **Coverage is uneven by design.** Chopin is rich and clean; the bands are a Commons grab-bag with
  false positives the dedup/mime filter only mostly removes; Space Force is ~empty. This is the real
  state of the open corpus, not a bug. Do not synthesize sources to "fill" it.
- **Lossy→lossy on some tracks.** Vorbis/MP3 sources re-encoded to Opus incur a small quality loss;
  WAV and already-Opus sources do not. Acceptable for a PD background-listening corpus; surfaced
  via `original_codec` if you ever want to filter.
- **Opus-in-CAF iOS framing.** Use the pure-Go packager and the §14.3 device test; keep the ffmpeg
  `-c:a copy` fallback. Re-validate if you bump the packager dependency.
- **Composer/work parsing is heuristic** for Commons titles; fields are nullable and keywords always
  capture the raw title, so search still works even when structured parsing misses.
- **Not in scope:** Musopen API, official-band-site scraping, lossless retention, audio
  fingerprinting/dedup across sources, tagging the Opus/CAF with embedded cover art.

---

### Appendix A — tested ffmpeg/ffprobe commands (copy verbatim)

```bash
# detect source codec + duration
ffprobe -v error -select_streams a:0 -show_entries stream=codec_name -of default=nw=1 IN
ffprobe -v error -show_entries format=duration -of default=nw=1 IN

# convert → opus  (encode; for music)
ffmpeg -hide_banner -loglevel error -y -i IN -map_metadata 0 -c:a libopus -b:a 128k -vbr on OUT.opus
# convert → opus  (when IN is already opus: lossless copy)
ffmpeg -hide_banner -loglevel error -y -i IN.opus -map_metadata 0 -c:a copy OUT.opus

# package → caf  (lossless stream copy; VERIFIED to yield codec_name=opus)
ffmpeg -hide_banner -loglevel error -y -i OUT.opus -c:a copy OUT.caf
```

### Appendix B — sample API payloads

IA file entry (from `https://archive.org/metadata/musopen-chopin`, `files[]`):
```json
{ "name": "Ballade no. 1 - Op. 23.mp3", "format": "VBR MP3",
  "length": "10:13", "size": "12382720" }
```
Download URL → `https://archive.org/download/musopen-chopin/Ballade%20no.%201%20-%20Op.%2023.mp3`
Item license → `metadata.licenseurl = "http://creativecommons.org/publicdomain/zero/1.0/"`.

Commons search hit (`list=search`, namespace 6, `"United States Marine Band" filetype:audio`):
```
File:Violin Concerto No. 2 in D minor - III. Allegro con fuoco - United States Marine Band.mp3
File:1812 Overture - United States Marine Band.opus
File:"America the Beautiful", performed by the United States Marine Band in the 1950s.oga
File:"America the Beautiful", performed by the United States Marine Band in the 1950s.wav   ← dedup vs .oga
```
Resolve via `prop=imageinfo&iiprop=url|mime|size|extmetadata` → `imageinfo[0].url` (direct download),
`.mime` (`audio/…` filter), `.extmetadata.LicenseShortName.value`, `.extmetadata.Artist.value`.