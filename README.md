# parso-pdaudio

A single Go + [Bubble Tea](https://github.com/charmbracelet/bubbletea) TUI that downloads
public-domain classical recordings from a fixed set of sources, normalizes everything to **Opus**,
packages an iOS-ready **CAF** alongside it, and indexes rich, searchable metadata in one **SQLite**
database — all in one flat directory. It includes a **built-in player** so you can browse, search,
and listen to downloaded tracks to verify them on the spot.

## What it does

- Pulls recordings from **10 sources** (3 Internet Archive items + 7 Wikimedia Commons searches).
- For each track: downloads the best-available native file (preference `opus > ogg > wav > mp3`),
  converts it to Opus, packages a `.caf` (lossless Opus-in-CAF), then deletes both the native
  download **and** the standalone `.opus` — the `.caf` is the sole persisted audio format.
- Stores the SQLite DB and all media in one flat directory; records structured metadata, license,
  provenance (URL/format/codec/bytes/sha1), plus a stopword-filtered keyword index and an FTS5 index.
- The whole job is **resumable** (state lives in the DB) and **crash-safe** (the DB is the work queue).
- Presents a per-source dashboard (counts, %, ETA, throughput), a combined searchable track list,
  live worker-pool scaling, and an integrated audio player.

## Post-processing & publishing pipeline

After the library is downloaded, a set of subcommands enrich the metadata, remove
duplicates, and publish everything to a Cloudflare R2 bucket. Run them in order:

```sh
# 0. Reclaim space: CAF (lossless Opus-in-CAF) is the sole persisted format, so
#    delete the redundant standalone .opus and .src.* files.
./parso-pdaudio compact --dir ./library

# 1-3. LLM enrichment: extract & validate composer/work/movement, correct
#      mis-attributed composers, group movements into works, and precompute
#      context-aware display titles. Uses the DeepSeek API — key read from
#      $DEEPSEEK_API_KEY or ~/.deepseek-api-key (never logged/committed).
./parso-pdaudio enrich --dir ./library

# 4. Dedup: fingerprint (Chromaprint/fpcalc) and collapse the same recording
#    appearing under multiple sources to one canonical track.
./parso-pdaudio dedup --dir ./library

# 5. Publish: upload canonical CAF files (audio/<id>.caf) + the distribution DB
#    (db/library.db) to R2. Refuses to run until dedup has completed.
./parso-pdaudio sync --dir ./library
```

R2 credentials are **secrets** and are never committed. Configure them via
`~/.parso-r2.json` / `./r2.config.json` (see `r2.config.example`) or `R2_*` env
vars; the account id also falls back to `~/.cloudflare-r2-api-account-id`.

## `parso-player` — standalone terminal client

A separate binary that consumes a library published to R2: it downloads the
distribution DB, lets you browse/search, and **streams CAF from R2** on demand
(cached locally). It is read-only against the bucket and never writes to R2.

### Build

```sh
go build -o parso-player ./cmd/parso-player
```

### Configure

Playback needs read access to the bucket. Provide R2 settings exactly as for
`sync` — either `~/.parso-r2.json` / `./r2.config.json` (see `r2.config.example`)
or `R2_*` env vars. For a public bucket you only need `R2_PUBLIC_BASE_URL` (audio
is fetched over plain HTTPS); for a private bucket you need the full credentials.

### Run

```sh
# Download the DB from R2 (cached), then browse/search/play:
./parso-player

# Force a fresh DB download:
./parso-player --refresh

# Use a local DB instead of downloading (browsing works even without R2 creds;
# playback still needs bucket access):
./parso-player --db ./library/library.db

# Custom cache directory (DB + streamed CAF files):
./parso-player --cache ~/.cache/parso
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--db PATH` | *(download)* | Open a local `library.db` instead of downloading from R2 |
| `--cache DIR` | OS cache dir `/parso-player` | Where the DB and streamed CAF files are cached |
| `--refresh` | `false` | Re-download the DB even if a cached copy exists |

### Keys

| Key | Action |
|-----|--------|
| `/` | Search (Enter runs, Esc returns to browse) |
| `↑`/`↓` or `j`/`k` | Move selection |
| `Enter` | Open a source/composer/work, or **play** a track/result |
| `Backspace` | Up one browse level |
| `Space` | Pause / resume |
| `←` / `→` | Seek −/+ 10s |
| `g` / `G` | Jump to top / bottom |
| `q` | Quit |

The browse tree is **source → composer → work → movement**. The first playback of
a track downloads its `audio/<id>.caf` into the cache; subsequent plays are local.

## `swift/ParsoKit` — embed a library in your app

A publishable SwiftPM package that opens the DB (download / local / bundled),
browses & searches, and vends R2 CAF stream URLs for AVFoundation. **No player is
included** — you own playback. See **[`swift/README.md`](swift/README.md)** for
the full API reference and a step-by-step SwiftUI + AVFoundation embedding guide.

## Requirements

- **Go 1.25+** (pure-Go SQLite via `modernc.org/sqlite` — CGO-free, trivially cross-compiles).
- **ffmpeg + ffprobe** on `PATH` (runtime dependency for the convert stage). If missing, downloads
  still run but convert/package/cleaner are disabled.
- For the built-in player: **`afplay`** (macOS, built-in — uses CoreAudio, the same stack as iOS) or
  **`ffplay`** (ships with ffmpeg) elsewhere.
- For `enrich`: a **DeepSeek API key** in `$DEEPSEEK_API_KEY` or `~/.deepseek-api-key`
  (uses the DeepSeek chat-completions API; the key is never logged or committed).
- For `dedup`: **`fpcalc`** (Chromaprint, e.g. `brew install chromaprint`) for acoustic
  fingerprinting. If absent, dedup falls back to exact size+duration+sha1 matching.

## Build

```sh
go build -o parso-pdaudio .
```

## Usage

```sh
# Download just the Chopin set, with the TUI:
./parso-pdaudio --sources chopin

# Everything (10 sources), headless, for unattended/bulk pulls:
./parso-pdaudio --sources all --no-tui

# Grab a quick sample to listen to, then browse it:
./parso-pdaudio --sources chopin --max-tracks 5
./parso-pdaudio --play --dir ./library      # player-only: no pipeline, just browse/listen
```

### TUI controls

```
[s] start (resume)   [p] pause              [/] search (FTS)   [r] re-run discovery
[d/c/k/x] select pool: download/convert/package/cleaner
[+]/[-]   scale the selected pool's workers up/down (live)
[↑]/[↓]   move selection      [enter] play selected (done) track    [space] stop
[R] reset failed rows         [q] quit (graceful: cancels work, flushes DB)
```

The **TRACKS** pane is the player: navigate, press `/` to filter via full-text search
(`chopin ballade`, `pathetique`, prefix `chop*`), and press `enter` on a `done` track to play it.
On macOS the player decodes the produced `.caf` through CoreAudio, doubling as a correctness check.

### Key flags

| flag | default | meaning |
|---|---|---|
| `--dir` | `./library` | output directory (DB + media) |
| `--sources` | `all` | comma list of keys or `all` |
| `--prefer` | `opus,ogg,wav,mp3` | format preference, high→low |
| `--allow-fallback` | `false` | if no preferred format, take any audio |
| `--require-license` | `""` | allowlist (e.g. `cc0,pd,pd-usgov`); `""` = allow all |
| `--opus-bitrate` | `128` | libopus VBR target kbps |
| `--packager` | `go` | `go` (pure-Go, iOS-safe) or `ffmpeg` |
| `--keep-source` | `false` | keep the native `.src.*` after packaging |
| `--dl/conv/pkg/clean-workers` | `4/2/1/1` | initial pool sizes |
| `--max-tracks` | `0` | cap tracks processed this run (0 = unlimited) |
| `--max-attempts` | `3` | per-stage retry ceiling |
| `--no-tui` | `false` | headless mode (prints progress) |
| `--play` | `false` | open the player UI only (no pipeline) |

## Sources

| key | provider | content | license |
|---|---|---|---|
| `chopin` | Internet Archive (`musopen-chopin`) | ~104 Chopin tracks (Ogg Vorbis derivatives) | CC0 |
| `bach_wtc1` | Internet Archive (`bach-well-tempered-clavier-book-1`) | Well-Tempered Clavier Book 1 | PD |
| `goldberg` | Internet Archive (`The_Open_Goldberg_Variations-11823`) | Open Goldberg Variations | CC0 |
| `beethoven_pitman` | Commons (`"Beethoven" "Pitman"`) | Paul Pitman sonatas (Ogg) | PD |
| `marine` / `army` / `navy` / `airforce` / `coastguard` | Commons (band name) | US military band recordings (mixed) | PD-USGov mostly |
| `spaceforce` | Commons | ~empty by design (band founded 2020) | — |

Coverage is uneven by design: Chopin is clean and rich; the bands are a Commons grab-bag with some
false positives the mime/dedup filter mostly removes; Space Force is ~empty (and that is not an
error).

## Architecture

A staged pipeline where **the SQLite DB is the queue** (gives free resume + crash-safety):

```
discover → [download] → [convert→opus] → [package→caf] → [cleaner→delete native] → done
```

Each worker atomically claims a row with a single `UPDATE … RETURNING` under SQLite's write lock
(WAL + `busy_timeout=5000`), does its work, and writes the result or marks `failed`. Pools scale
live from the TUI. Workers communicate to the UI only through a buffered results channel (the model
is never touched off the Bubble Tea goroutine).

## A note on Opus-in-CAF and ffmpeg

The pure-Go packager (`github.com/nabil6391/opus_caf_converter`) is the **default and primary** path
and produces a genuine opus-in-CAF (`ffprobe` reports `codec_name=opus`, and macOS `afplay`/CoreAudio
plays it). **ffmpeg's `opus → caf` stream-copy muxer is _not implemented_ in ffmpeg 8.x**
(`muxing codec currently unsupported`), so `--packager ffmpeg` only works on older builds (it was
verified on 6.1.1). This is exactly why the Go packager is the default; keep it unless you have a
specific reason.

## Testing

```sh
go test ./...                 # unit + ffmpeg integration tests (ffmpeg tests skip if not installed)
go test -tags e2e ./e2e/...   # LIVE network e2e: real sources + real playable tracks
PARSO_E2E_PLAY=1 go test -tags e2e -run TestSingleTrackPipelinePerSource ./e2e/...   # also audibly play
```

The `e2e` suite (`-tags e2e`) is the requested local verification that **each source actually works
and loads real tracks you can play**:

- `TestDiscoverEachSource` — every source enumerates real audio (Space Force allowed empty, no error).
- `TestSingleTrackPipelinePerSource` — downloads one real track per source and runs
  download→opus→caf, asserting `ffprobe` reads the CAF as `codec_name=opus` (covers ogg/vorbis,
  mp3, and wav source formats).
- `TestEngineEndToEndChopin` — the full DB-backed engine on a few Chopin tracks: verifies `done`
  rows, opus+caf artifacts, `codec_name=opus`, **no leftover `.src.*`**, and surviving provenance.

## CI

`.github/workflows/ci.yml`:

- **test** (every push/PR): `gofmt`, `go vet`, `go test -race ./...` (with ffmpeg installed), build.
- **cross-build**: `CGO_ENABLED=0` builds for linux/darwin/windows × amd64/arm64 (proves the
  single-static-binary goal).
- **e2e**: the live network suite, on manual dispatch + a weekly schedule (kept off PRs to avoid
  network flakiness).

## License

Code: see repository. Downloaded media carries its own per-track license, recorded in the DB
(`license_short` / `license_url`); use `--require-license` to filter.
