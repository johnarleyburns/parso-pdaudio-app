package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/pipeline"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	selStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("212"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	failStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	playStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	boxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	tabStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Padding(0, 1)
	tabActive   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("39")).Padding(0, 1)
)

var statusRank = map[string]int{
	core.StatusDiscovered: 0, core.StatusDownloading: 1, core.StatusDownloaded: 2,
	core.StatusConverting: 3, core.StatusConverted: 4, core.StatusPackaging: 5,
	core.StatusPackaged: 6, core.StatusCleaning: 7, core.StatusDone: 8,
}

type srcAgg struct {
	disc, dl, conv, pkg, done, fail, skip int
}

func (m Model) aggregate(src string) srcAgg {
	c := m.counts[src]
	a := srcAgg{}
	for st, n := range c {
		switch st {
		case core.StatusSkipped:
			a.skip += n
			continue
		case core.StatusFailed:
			a.fail += n
			a.disc += n
			continue
		}
		a.disc += n
		r := statusRank[st]
		if r >= statusRank[core.StatusDownloaded] {
			a.dl += n
		}
		if r >= statusRank[core.StatusConverted] {
			a.conv += n
		}
		if r >= statusRank[core.StatusPackaged] {
			a.pkg += n
		}
		if r >= statusRank[core.StatusDone] {
			a.done += n
		}
	}
	return a
}

func (m Model) View() string {
	if m.width < 20 || m.height < 15 {
		return "terminal too small (need >= 20x15)"
	}

	headerH := 1
	tabH := 1
	footerH := 1
	bodyH := m.height - headerH - tabH - footerH
	if bodyH < 4 {
		bodyH = 4
	}

	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")
	b.WriteString(m.renderTabs())
	b.WriteString("\n")
	b.WriteString(m.renderBody(bodyH))
	b.WriteString("\n")
	b.WriteString(m.renderFooter())
	return b.String()
}

func (m Model) renderHeader() string {
	state := okStyle.Render("running")
	if m.eng.Paused() {
		state = failStyle.Render("PAUSED")
	} else if m.eng.IsThrottled() {
		state = playStyle.Render("THROTTLED")
	}
	left := titleStyle.Render("parso-pdaudio") + "  [" + state + "]  " + dimStyle.Render(string(m.eng.Phase()))
	right := headerStyle.Render(fmt.Sprintf("workers D:%d C:%d P:%d X:%d  pkg:%s",
		m.eng.PoolSize(pipeline.StageDownload),
		m.eng.PoolSize(pipeline.StageConvert),
		m.eng.PoolSize(pipeline.StagePackage),
		m.eng.PoolSize(pipeline.StageCleaner),
		m.eng.PackagerName()))
	return lineJustify(left, right, m.width)
}

func (m Model) renderTabs() string {
	tabs := []string{"Dashboard", "Tracks", "Player", "Browse", "Log"}
	var parts []string
	for i, s := range tabs {
		if i == m.tab {
			parts = append(parts, tabActive.Render(s))
		} else {
			parts = append(parts, tabStyle.Render(s))
		}
	}
	n := len(parts)
	if n <= 1 {
		return parts[0]
	}
	totalLabelW := 0
	for _, p := range parts {
		totalLabelW += lipgloss.Width(p)
	}
	gapCount := n - 1
	extra := m.width - totalLabelW
	if extra < 0 {
		extra = 0
	}
	spacing := extra / gapCount
	if spacing < 1 {
		spacing = 1
	}
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString(strings.Repeat(" ", spacing))
		}
		b.WriteString(p)
	}
	// Fill remainder of the row so the active tab background extends.
	remainder := m.width - lipgloss.Width(b.String())
	if remainder > 0 {
		b.WriteString(strings.Repeat(" ", remainder))
	}
	return b.String()
}

func (m Model) renderBody(h int) string {
	switch m.tab {
	case tabDashboard:
		return m.renderDashboard(h)
	case tabTracks:
		return m.renderTracks(h)
	case tabPlayer:
		return m.renderPlayerTab(h)
	case tabLog:
		return m.renderLog(h)
	case tabBrowse:
		return m.renderBrowse(h)
	}
	return ""
}

func (m Model) renderDashboard(h int) string {
	// Split: top = status + sources, bottom = workers.
	srcH := 2 + len(m.order)
	if srcH > h-4 {
		srcH = h - 4
		if srcH < 1 {
			srcH = 1
		}
	}
	workerH := h - srcH
	if workerH < 0 {
		workerH = 0
	}

	var b strings.Builder

	// Status line.
	phase := m.eng.Phase()
	pendingTotal := 0
	for _, n := range m.pending {
		pendingTotal += n
	}
	b.WriteString(fmt.Sprintf("Phase: %s  Pending: %d  ", phase, pendingTotal))
	if m.eng.IsDiscovering() {
		b.WriteString("discovering…  ")
	}
	b.WriteString(fmt.Sprintf("%.1f MB/s", m.mbps))
	b.WriteString("\n\n")

	// Sources table.
	b.WriteString(m.renderSourcesTable())
	b.WriteString("\n")

	// Workers panel.
	if workerH > 1 {
		b.WriteString(m.renderWorkers(workerH))
	}
	return b.String()
}

func (m Model) renderSourcesTable() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(fmt.Sprintf(
		"%-16s %5s %5s %5s %5s %5s %5s %7s %10s",
		"SOURCE", "disc", "dl", "conv", "pkg", "done", "fail", "%done", "ETA")))
	b.WriteString("\n")
	for _, src := range m.order {
		a := m.aggregate(src)
		pct := 0.0
		if a.disc > 0 {
			pct = float64(a.done) / float64(a.disc) * 100
		}
		failCol := fmt.Sprintf("%5d", a.fail)
		if a.fail > 0 {
			failCol = failStyle.Render(failCol)
		}
		b.WriteString(fmt.Sprintf("%-16s %5d %5d %5d %5d %5d %s %6.1f%% %10s",
			src, a.disc, a.dl, a.conv, a.pkg, a.done, failCol, pct, m.etaFor(src, a)))
		b.WriteString("\n")
	}
	return boxStyle.Width(m.width - 2).Render(strings.TrimRight(b.String(), "\n"))
}

func (m Model) renderWorkers(h int) string {
	ws := m.workers
	var b strings.Builder
	b.WriteString(headerStyle.Render("WORKERS"))
	b.WriteString("\n")
	shown := 0
	for _, w := range ws {
		if shown >= h-2 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  ... %d more", len(ws)-shown)))
			break
		}
		statusGlyph := "▶"
		style := okStyle
		if w.Status == "backoff" {
			statusGlyph = "⟳"
			style = failStyle
		} else if w.Status == "idle" {
			statusGlyph = "○"
			style = dimStyle
		}
		line := fmt.Sprintf("  %s %s/%s  %s/%s",
			statusGlyph, w.Stage, w.Source, w.Worker, truncate(w.Title, 30))
		if w.Status == "backoff" {
			line += "  " + w.Backoff
		} else if w.Bytes > 0 {
			line += fmt.Sprintf("  %s", humanSize(w.Bytes))
		}
		b.WriteString(style.Render(line))
		b.WriteString("\n")
		shown++
	}
	if len(ws) == 0 {
		b.WriteString(dimStyle.Render("  (no active workers)"))
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) renderTracks(h int) string {
	// Always-visible search bar.
	searchLine := m.search.View()
	bodyH := h - 3 // header + search + footer space
	if bodyH < 1 {
		bodyH = 1
	}

	header := " " + headerStyle.Render(fmt.Sprintf("%-7s %-12s %-14s %-60s %s", "ST", "SOURCE", "COMPOSER", "TITLE", "SIZE"))
	q := m.search.Value()
	info := fmt.Sprintf("%d shown", len(m.tracks))
	if q != "" {
		info = fmt.Sprintf("filter: %q  %d shown", q, len(m.tracks))
	}
	header += dimStyle.Render("  " + info)

	start := 0
	rows := bodyH - 2 // header + blank line
	if rows < 1 {
		rows = 1
	}
	if m.sel >= rows {
		start = m.sel - rows + 1
	}
	end := start + rows
	if end > len(m.tracks) {
		end = len(m.tracks)
	}

	contentW := m.width - 6 // box outer(m.width-2) - border(2) - padding(2)
	if contentW < 40 {
		contentW = 40
	}
	var lines []string
	if len(m.tracks) == 0 {
		lines = append(lines, dimStyle.Render("(no tracks yet — discovery/downloads in progress)"))
	}
	for i := start; i < end; i++ {
		t := m.tracks[i]
		line := trackRow(t, m.nowPlaying)
		if i == m.sel {
			lines = append(lines, selStyle.Render(padRight(">"+line, contentW)))
		} else {
			lines = append(lines, " "+line)
		}
	}
	lines = append([]string{header, ""}, lines...)
	body := strings.Join(lines, "\n")

	return searchLine + "\n" + boxStyle.Width(m.width-2).Render(body)
}

func trackRow(t *core.Track, nowID string) string {
	now := " "
	if t.ID == nowID {
		now = "♪"
	}
	st := t.Status
	if len(st) > 4 {
		st = st[:4]
	}
	var glyph string
	switch t.Status {
	case core.StatusDone:
		glyph = "●"
	case core.StatusFailed:
		glyph = "✗"
	case core.StatusSkipped:
		glyph = "·"
	case core.StatusDiscovered:
		glyph = "○"
	default:
		glyph = "◐"
	}
	state := glyph + st
	src := truncate(t.Source, 12)
	cmp := t.Composer
	if cmp == "" {
		cmp = "—"
	}
	title := t.Title
	if title == "" {
		title = t.Work
	}
	if title == "" {
		title = t.SourceItem
	}
	if title == "" {
		title = t.ID
	}
	size := ""
	if t.Status == core.StatusDone {
		size = fmt.Sprintf("opus %s caf %s", human(t.OpusBytes), human(t.CafBytes))
	}
	return fmt.Sprintf("%s %-4s %-12s %-14s %-60s %s", now, state, src, truncate(cmp, 12), truncate(title, 58), size)
}

func (m Model) renderPlayerTab(h int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("PLAYER"))
	b.WriteString("\n\n")

	if m.nowPlaying != "" {
		b.WriteString(playStyle.Render("▶ " + m.nowTitle))
		b.WriteString("\n\n")

		// Progress bar.
		frac := 0.0
		if m.durSec > 0 {
			frac = m.posSec / m.durSec
		}
		b.WriteString(m.prog.ViewAs(frac))
		b.WriteString("\n")

		pos := fmtDuration(m.posSec)
		dur := fmtDuration(m.durSec)
		state := m.playState
		if state == "" {
			state = "playing"
		}
		b.WriteString(fmt.Sprintf("%s / %s  [%s]", pos, dur, state))
		b.WriteString("\n")

		if m.playerErr != "" {
			b.WriteString(failStyle.Render("error: " + m.playerErr))
			b.WriteString("\n")
		}
	} else {
		b.WriteString(dimStyle.Render("No track playing. Select a 'done' track and press Enter."))
		b.WriteString("\n")
	}

	// Controls help.
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("[enter] play  [space] pause/resume  [←→] seek ±10s  [s] stop"))
	b.WriteString("\n")

	if len(m.tracks) > 0 && m.sel < len(m.tracks) {
		t := m.tracks[m.sel]
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("Selected: %s [%s]", displayTitle(t), t.Status))
	}

	return b.String()
}

func (m Model) renderLog(h int) string {
	lines := m.logLines
	bodyH := h - 1
	if bodyH < 1 {
		bodyH = 1
	}

	start := 0
	if len(lines) > bodyH {
		start = len(lines) - bodyH
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render("LOG"))
	b.WriteString("\n")
	for i := start; i < len(lines); i++ {
		b.WriteString(dimStyle.Render(lines[i]))
		b.WriteString("\n")
	}
	if len(lines) == 0 {
		b.WriteString(dimStyle.Render("(no activity yet)"))
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) renderBrowse(h int) string {
	var b strings.Builder

	// Breadcrumb.
	var crumbs []string
	crumbs = append(crumbs, "All sources")
	if m.browseLevel >= 1 && m.browseSelSource != "" {
		crumbs = append(crumbs, m.browseSelSource)
	}
	if m.browseLevel >= 2 && m.browseSelComposer != "" {
		crumbs = append(crumbs, m.browseSelComposer)
		if m.browseSelComposer == "" {
			crumbs[len(crumbs)-1] = "—"
		}
	}
	if m.browseLevel >= 3 && m.browseSelTitle != "" {
		crumbs = append(crumbs, m.browseSelTitle)
		if m.browseSelTitle == "" {
			crumbs[len(crumbs)-1] = "—"
		}
	}
	crumbStr := strings.Join(crumbs, " > ")
	b.WriteString(dimStyle.Render(crumbStr))
	b.WriteString("\n\n")

	bodyH := h - 4
	if bodyH < 1 {
		bodyH = 1
	}

	// Level 3: render tracks.
	if m.browseLevel == 3 {
		return m.renderBrowseTracks(bodyH, &b)
	}

	// Levels 0-2: render browse entries.
	var nodes []db.BrowseEntry
	var headerLabel string
	switch m.browseLevel {
	case 0:
		nodes = m.browseSources
		headerLabel = "SOURCES"
	case 1:
		nodes = m.browseComposers
		headerLabel = "COMPOSERS in " + m.browseSelSource
	case 2:
		nodes = m.browseTitles
		headerLabel = "TITLES"
	}

	total := 0
	for _, n := range nodes {
		total += n.Count
	}

	header := headerStyle.Render(fmt.Sprintf("%-40s %s", headerLabel, fmt.Sprintf("%d nodes, %d tracks", len(nodes), total)))
	b.WriteString(header)
	b.WriteString("\n")

	rows := bodyH
	if rows < 1 {
		rows = 1
	}

	start := 0
	if m.browseSel >= rows {
		start = m.browseSel - rows + 1
	}
	end := start + rows
	if end > len(nodes) {
		end = len(nodes)
	}

	hasSelection := m.browseSel >= 0 && m.browseSel < len(nodes)
	if len(nodes) == 0 {
		b.WriteString(dimStyle.Render("(no tracks yet)"))
	} else {
		for i := start; i < end; i++ {
			n := nodes[i]
			indent := "  "
			glyph := "  "
			if hasSelection && i == m.browseSel {
				glyph = "▶ "
			}
			line := fmt.Sprintf("%s%-50s (%d)", glyph, n.Name, n.Count)
			if hasSelection && i == m.browseSel {
				line = selStyle.Render(padRight(line, m.width-2))
			}
			b.WriteString(indent)
			b.WriteString(line)
			if i < end-1 {
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

func (m Model) renderBrowseTracks(bodyH int, b *strings.Builder) string {
	header := headerStyle.Render(fmt.Sprintf("%-7s %-14s %-60s %s", "ST", "COMPOSER", "TITLE", "SIZE"))
	info := fmt.Sprintf("%d tracks shown", len(m.browseTracks))
	b.WriteString(fmt.Sprintf("%s  %s", header, dimStyle.Render(info)))
	b.WriteString("\n")

	rows := bodyH - 2
	if rows < 1 {
		rows = 1
	}

	start := 0
	if m.browseSel >= rows {
		start = m.browseSel - rows + 1
	}
	end := start + rows
	if end > len(m.browseTracks) {
		end = len(m.browseTracks)
	}

	contentW := m.width - 6
	if contentW < 40 {
		contentW = 40
	}

	if len(m.browseTracks) == 0 {
		b.WriteString(dimStyle.Render("(no tracks yet)"))
	} else {
		var lines []string
		for i := start; i < end; i++ {
			t := m.browseTracks[i]
			line := trackRow(t, m.nowPlaying)
			if i == m.browseSel {
				lines = append(lines, selStyle.Render(padRight(">"+line, contentW)))
			} else {
				lines = append(lines, " "+line)
			}
		}
		b.WriteString(strings.Join(lines, "\n"))
	}
	return b.String()
}

// --- reused render helpers ---

func (m Model) renderFooter() string {
	pools := fmt.Sprintf("pool:%s [d/c/k/x]+/-", poolStages[m.focusPool])
	return footerStyle.Render(fmt.Sprintf(
		"[s]start/stop  [r]ediscover  [R]etry  [V]revive  %s  [1-5]tabs  [/]search  [↑↓]nav  [enter]play  [q]uit",
		pools))
}

func (m Model) etaFor(src string, a srcAgg) string {
	r := m.rates[src]
	if r == nil || r.ewma <= 1e-9 {
		if a.disc > 0 && a.done >= a.disc {
			return "00:00"
		}
		return "--:--"
	}
	remaining := a.disc - a.done - a.fail
	if remaining <= 0 {
		return "00:00"
	}
	return fmtDuration(float64(remaining) / r.ewma)
}

func (m Model) totalRate() float64 {
	var sum float64
	for _, r := range m.rates {
		sum += r.ewma
	}
	return sum
}

// displayTitle renders a track's fully-qualified label, preferring enriched
// composer/work/movement fields and falling back to the raw title.
func displayTitle(t *core.Track) string {
	return truncate(core.DisplayTitle(t, core.DisplayGlobal), 60)
}

func fmtDuration(sec float64) string {
	if sec < 0 || sec > 60*60*999 {
		return "--:--"
	}
	s := int(sec)
	h := s / 3600
	mm := (s % 3600) / 60
	ss := s % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, mm, ss)
	}
	return fmt.Sprintf("%02d:%02d", mm, ss)
}

func human(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGT"[exp])
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w])
}

func padRight(s string, w int) string {
	if w <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) >= w {
		return string(r[:w])
	}
	return s + strings.Repeat(" ", w-len(r))
}

func lineJustify(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGT"[exp])
}
