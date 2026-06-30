# parso-pdaudio

A single Go + [Bubble Tea](https://github.com/charmbracelet/bubbletea) TUI that downloads
public-domain classical recordings from a fixed set of sources, normalizes everything to **Opus**,
packages an iOS-ready **CAF** alongside it, and indexes rich, searchable metadata in one **SQLite**
database — all in one flat directory. It includes a **built-in player** so you can browse, search,
and listen to downloaded tracks to verify them on the spot.

## What it does

- Pulls recordings from **8 sources** (1 Internet Archive item + 7 Wikimedia Commons searches).
- For each track: downloads the best-available native file (preference `opus > ogg > wav > mp3`),
  converts it to a canonical `.opus`, packages a sibling `.caf`, then deletes the native download.
- Stores the SQLite DB and all media in one flat directory; records structured metadata, license,
  provenance (URL/format/codec/bytes/sha1), plus a stopword-filtered keyword index and an FTS5 index.
- The whole job is **resumable** (state lives in the DB) and **crash-safe** (the DB is the work queue).
- Presents a per-source dashboard (counts, %, ETA, throughput), a combined searchable track list,
  live worker-pool scaling, and an integrated audio player.

## Requirements

- **Go 1.25+** (pure-Go SQLite via `modernc.org/sqlite` — CGO-free, trivially cross-compiles).
- **ffmpeg + ffprobe** on `PATH` (runtime dependency for the convert stage). If missing, downloads
  still run but convert/package/cleaner are disabled.
- For the built-in player: **`afplay`** (macOS, built-in — uses CoreAudio, the same stack as iOS) or
  **`ffplay`** (ships with ffmpeg) elsewhere.

## Build

```sh
go build -o parso-pdaudio .
```

## Usage

```sh
# Download just the Chopin set, with the TUI:
./parso-pdaudio --sources chopin

# Everything (8 sources), headless, for unattended/bulk pulls:
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
