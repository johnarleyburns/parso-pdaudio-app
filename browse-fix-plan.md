# Plan: Fix Browse Functionality

## Files to modify

| File | Lines | What changes |
|---|---|---|
| `internal/tui/tui.go` | ~88-95 | Add `browseTracks []db.Track` to model (new level 3 state) |
| `internal/db/db.go` | 565-589 | Fix `BrowseTitles`: `composer=?` → `composer IS NOT DISTINCT FROM ?`, add `ORDER BY title COLLATE NOCASE` |
| `internal/tui/update.go` | 406-429 | Wire `BrowseTracks` into `browseDrill()` case 2 → new case level 2 drills to tracks |
| `internal/tui/update.go` | 284-287 | At level 3 (tracks list), Enter calls `playSelected()` |
| `internal/tui/update.go` | 431-447 | Extend `browseBack()` to handle level 3 → level 2 |
| `internal/tui/update.go` | 395-404 | Reset browseTracks in `switchToBrowseTab()` |
| `internal/tui/view.go` | 441-525 | Render track rows at level 3 (reuse `trackRow()` or similar) |
| `internal/tui/view.go` | ~162 | Add case for level 3 in body routing |

## Step-by-step

### 1. Fix NULL composer in `BrowseTitles` (`internal/db/db.go:565-589`)
- Change `WHERE source=? AND composer=?` to `WHERE source=? AND (composer IS NOT DISTINCT FROM ?)`
- Add `ORDER BY title COLLATE NOCASE`

### 2. Add `browseTracks` field (`internal/tui/tui.go:88-95`)
- Add `browseTracks []db.Track` to the model struct

### 3. New message type/command (`internal/tui/tui.go:44-46`)
- Add `refreshBrowseTracksCmd(source, composer, title string)` that calls `db.BrowseTracks()`

### 4. Wire Level 3 drill-down (`internal/tui/update.go:406-429`)
- `browseDrill()` case 2: store selected title name, dispatch `refreshBrowseTracksCmd(m.browseSelSource, m.browseSelComposer, sel.Name)`
- This loads actual `[]db.Track` into `m.browseTracks` and sets `m.browseLevel = 3`

### 5. Play from browse (`internal/tui/update.go:284-287`)
- At `browseLevel == 3`, Enter calls `playSelected()` using `m.browseTracks[m.browseSel]`

### 6. Back from tracks (`internal/tui/update.go:431-447`)
- `browseBack()` level 3: clear `browseTracks`, set level back to 2, re-fetch titles

### 7. Reset state (`internal/tui/update.go:395-404`)
- `switchToBrowseTab()` also clears `browseTracks`

### 8. Render tracks (`internal/tui/view.go:441-525`)
- In `renderBrowse()`, add case `browseLevel == 3`: render `browseTracks` using `trackRow()` with selection highlighting
- Update the breadcrumb to show the full path including title

## What the user will experience
- Browse: `Sources → Composers → Titles → Track list`
- On the track list level, Enter plays the selected track (same as Tracks tab)
- Composers with NULL/empty values (shown as "—") now correctly show their titles
- Titles are sorted alphabetically
