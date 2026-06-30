# Addendum §B (revised) — Commons classical via composer-category traversal

Supersedes the free-text composer approach. Same `commons` provider, new `classical_categories`
mode. Every category name, count, and field below was verified against the live Commons API.

## B.0 Why this route

Free-text composer search misses recordings whose metadata doesn't name the composer in searchable
text, and forces a hardcoded composer list with hand-typed diacritics (which silently fails — ASCII
"Frederic Chopin" / "Antonin Dvorak" return 0). Category traversal fixes both: Commons already
groups classical audio by composer with canonical accented names, and it surfaces performer/work
subcategories (e.g. historic Schnabel/Horenstein transfers) that free-text buries. The cost is a
real filtering layer (§B.3) — the categories are high-recall but noisy.

## B.1 The roots (canonical, verified)

- **Worklist root:** `Category:Audio files of classical music by composer` → **129 composer
  subcategories**, correctly accented (Isaac Albéniz, Béla Bartók, Frédéric Chopin, Antonín Dvořák,
  Georg Frideric Händel, Leoš Janáček, Carl Philipp Emanuel Bach, …). Enumerate it once to get the
  composer worklist — do **not** hardcode names.

  ```
  GET https://commons.wikimedia.org/w/api.php
    ?action=query&list=categorymembers
    &cmtitle=Category:Audio files of classical music by composer
    &cmtype=subcat&cmlimit=500&format=json&maxlag=5
  ```
  Returns members with `ns=14` titles like `Category:Audio files of music by Ludwig van Beethoven`.
  (Note: the broader `Category:Audio files of music by composer` is the WRONG root — it's dominated
  by royalty-free/netlabel artists like Kevin MacLeod, NEFFEX, C418. Use the **classical** one.)

- Each composer category carries direct files **and** subcats. Verified file counts (direct only):
  Beethoven 313 (4 subcats), Bach 312 (12), Mendelssohn 169, Brahms 145, Mozart 124, Tchaikovsky 85,
  Mahler 56, Palestrina 16. Subcats hold more — recursion required (§B.2).

## B.2 Traversal algorithm (recursive, depth-limited, cycle-safe)

For each composer category from B.1:

```
seen = set()                      # category titles already visited (cycle guard)
queue = [(composerCat, depth=0)]
while queue:
    (cat, depth) = queue.pop()
    if cat in seen: continue
    seen.add(cat)
    members = categorymembers(cat, cmtype="file|subcat", paginate cmcontinue)
    for m in members:
        if m.ns == 6:                          # File:
            emit file candidate (carry `cat` as context for performer/work, §B.3.5)
        elif m.ns == 14 and depth < MAX_DEPTH:  # subcategory
            if is_skippable_subcat(m.title): continue
            queue.append((m.title, depth+1))
```

- `MAX_DEPTH = 3` (covers composer → work/instrument/performer subcats without runaway).
- **Skip-subcat patterns** (`is_skippable_subcat`, case-insensitive substring): `MIDI files`,
  `Synthesized`, `Sheet music`, `Scores`, `Sound files of metronome`. These are non-recordings.
- `categorymembers` mechanics: `cmtype=file|subcat`, `cmlimit=500`, follow `continue.cmcontinue`
  until absent. Separate members by `ns` (6 = file, 14 = subcat).
- The cycle guard (`seen`) is mandatory — Commons category graphs contain loops.
- Subcat names are signal, not just structure: `Audio files of Beethoven's Piano Sonatas Played by
  Artur Schnabel` yields the complete historic Schnabel cycle. Capture the subcat title as
  `category_context` on each emitted file.

## B.3 The filtering layer (the real work — apply in order)

Verified noise in these categories: webm **video** files, MIDI, mixed licenses (PD, CC0, CC-BY-SA
3.0/4.0, EFF OAL-1), FLAC, and micro-fragments (`Beet5mov1bars1to5.ogg` = five bars). Four gates:

**B.3.1 Audio-mime gate.** Resolve `imageinfo.mime` (§B.4) and keep a file only if it is audio:
`mime` starts with `audio/` **OR** `mime ∈ {application/ogg, application/x-ogg}` **OR** extension ∈
`{opus, ogg, oga, flac, wav, mp3}`. **Drop** `video/webm`, `image/*`, `application/pdf`, and anything
else. (Recall: `.ogg` reports `application/ogg`, not `audio/ogg` — a naive `audio/` check drops every
Ogg.) Also drop `audio/midi` here as a backstop to the subcat skip.

**B.3.2 Fragment floor (two-stage).**
- Cheap discovery gate: drop files with `imageinfo.size < MIN_BYTES` (default 250_000 ≈ a sub-30s Ogg
  is well under this). Kills `…bars1to5.ogg`-type theory snippets before download.
- Authoritative convert gate: after ffprobe (the pipeline already runs it, §7.2), drop files with
  `duration_sec < MIN_DURATION` (default 30). Set `status='skipped'`, reason `fragment`.

**B.3.3 License gate.** From `imageinfo.extmetadata` (§B.4), read `LicenseShortName` + `LicenseUrl`.
- **Strict (default):** keep only `LicenseShortName` ∈ {`CC0`, `Public domain`, any `PD-*`}.
- **Permissive (`--commons-allow-attribution`):** also keep `CC BY *`, `CC BY-SA *`, `EEF OAL-1`;
  record the attribution string (cleaned `Artist` + license + source URL) for a later credits file.
- **Caveat (verified):** a file's Structured-Data license statement (`haswbstatement:P275=…`) can
  disagree with its wikitext license template — one file carried a CC0 SDC tag while its page template
  was `{{PD-EU-audio}}`. Treat **`extmetadata.LicenseShortName` (the rendered template) as
  authoritative**; do not gate on SDC statements for this route.

**B.3.4 Format preference (incl FLAC fallback).** Use preference `opus > ogg > flac > wav > mp3` for
this source (note FLAC added vs the band/IA default). Rationale: each Commons file is standalone (no
auto-derivatives), and the best historical transfers are often FLAC-only (e.g. the Horenstein
Beethoven 9th). The converter handles `flac→opus` fine and the cleaner deletes the FLAC afterward, so
allowing it costs only transient disk. Gate behind `--commons-allow-flac` (default **on** for this
source) if you want to forbid large downloads.

**B.3.5 Dedup.** The same performance recurs under translated/renamed titles (German
`Klaviersonate Nr. 1 Op. 2.1 - I.Allegro` and English `Piano Sonata N° 1 - 1. Allegro
(Beethoven, Schnabel)`) and across formats (`.oga` + `.wav`). Compute `WorkKey` = lowercased title,
extension stripped, format/qualifier words removed, whitespace collapsed, **and** transl/diacritics
folded; group by `(composer, WorkKey)`; keep one file per group by B.3.4 preference; mark the rest
`skipped`. `UNIQUE(source, original_url)` is the backstop.

## B.4 License + metadata resolution

Batch up to 50 `File:` titles per call:

```
GET https://commons.wikimedia.org/w/api.php
  ?action=query&titles={pipe-joined titles}
  &prop=imageinfo&iiprop=url|mime|size|extmetadata
  &format=json&maxlag=5
Header: User-Agent: parso-pdaudio/1.0 (your-contact)   ← Wikimedia REQUIRES a descriptive UA
```

Per file pull: `imageinfo[0].url` (direct download), `.mime`, `.size`, and from `extmetadata`:
`LicenseShortName`, `LicenseUrl`/`License`, `Artist`, `ImageDescription`, `DateTimeOriginal`.
**Strip HTML + unescape entities** on every extmetadata value before storing (they arrive wrapped in
`<a>`/`<span>`).

Field mapping → `tracks`:
- `composer` = the worklist composer (the category you're traversing), override-able by title parse.
- `performer` = parsed from `category_context` ("…Played by <X>") or filename tail ("(Composer,
  <Performer>)"); else null. (This route recovers performer far better than the EA set, which had
  none.)
- `title`/`work`/`movement` = best-effort parse of the filename; nullable.
- `license_short`/`license_url`, `original_url`, `original_format` (from extension), `original_bytes`.
- `date_raw` from `DateTimeOriginal` if present, but note it is often the **upload** date, not the
  recording date — do not trust it as recording year; leave `year` null unless the title/description
  gives a real one.

## B.5 Provider config shape

```jsonc
{
  "key": "commons_classical",
  "provider": "commons",
  "mode": "classical_categories",
  "root_category": "Category:Audio files of classical music by composer",
  "max_depth": 3,
  "skip_subcat_patterns": ["MIDI files","Synthesized","Sheet music","Scores","metronome"],
  "format_preference": ["opus","ogg","flac","wav","mp3"],
  "min_bytes": 250000,
  "min_duration_sec": 30,
  "license_policy": "strict",            // "strict" = CC0/PD only; "attribution" = also CC-BY/SA/OAL
  "composer_allowlist": null             // null = all 129; or a subset of category titles
}
```

Provider loop: enumerate `root_category` subcats → (optional) intersect with `composer_allowlist` →
for each, run §B.2 traversal → batch-resolve imageinfo (§B.4) → apply §B.3 gates in order → group by
WorkKey → select by preference → emit `tracks` rows. Band sources keep `mode:"band_name"`; the two
Bach items keep `provider:"ia"`.

## B.6 Expected yield & quality reality (honest)

- Coverage is broad (~129 composers; hundreds of files for major names) but **uneven and noisy**.
  After the four gates, expect to discard a meaningful fraction (fragments, MIDI, video, non-CC0/PD,
  dupes). Treat the post-filter count as the real corpus, not the raw category counts.
- Quality ranges from professional historical transfers (Schnabel sonatas, Horenstein symphonies) to
  amateur home recordings and synthesized-but-rendered-to-audio pieces. The min-duration floor and
  license gate remove the worst, but performance quality is not knowable from metadata — accept
  variance, or add an optional `prefer_subcat_contains` boost for performer-named subcats (which skew
  professional).
- `year`/recording-date is mostly unavailable; `performer` is partially recoverable. Plan the DB and
  any UI to treat both as frequently-null.

## B.7 Acceptance criteria (additions)

1. Enumerating `root_category` returns ~129 accented composer categories; the pipeline reads names
   from the API, not a hardcoded list (verify by spot-checking that `Frédéric Chopin` / `Antonín
   Dvořák` are traversed and yield rows).
2. Traversal is recursive and cycle-safe: `MIDI files of…` subcats are skipped, the Schnabel-type
   performer subcats are entered, and no category is visited twice.
3. The audio-mime gate retains `application/ogg` files and drops a `video/webm` member from a
   composer category.
4. The fragment floor removes a known micro-file (e.g. a `…bars1to5.ogg`-style snippet) — no row
   shorter than `min_duration_sec` reaches `done`.
5. Strict license mode leaves every row with `license_short` ∈ {CC0, Public domain, PD-*}; a CC-BY-SA
   file in a traversed category is `skipped` (and would be kept only under `attribution` mode).
6. Dedup collapses the German/English Schnabel duplicates to one row per movement.
7. `performer` is populated for at least the performer-subcat recordings (e.g. "Artur Schnabel").