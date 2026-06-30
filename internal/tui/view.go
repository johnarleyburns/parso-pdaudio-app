package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
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
)

// statusRank orders pipeline statuses for cumulative funnel counting.
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

// View renders the whole UI.
func (m Model) View() string {
	if m.width < 20 || m.height < 12 {
		return "terminal too small (need >= 20x12)"
	}
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")
	b.WriteString(m.renderSources())
	b.WriteString("\n")
	b.WriteString(m.renderTotal())
	b.WriteString("\n")
	b.WriteString(m.renderTracks())
	b.WriteString("\n")
	b.WriteString(m.renderNowPlaying())
	b.WriteString("\n")
	b.WriteString(m.renderFooter())
	return b.String()
}

func (m Model) renderHeader() string {
	state := okStyle.Render("running")
	if m.eng.Paused() {
		state = failStyle.Render("PAUSED")
	}
	disc := ""
	if m.discovering {
		disc = dimStyle.Render("  discovering…")
	}
	left := titleStyle.Render("parso-pdaudio") + "  [" + state + "]" + disc
	right := headerStyle.Render(fmt.Sprintf("workers D:%d C:%d P:%d X:%d  pkg:%s",
		m.eng.PoolSize(pipeline.StageDownload),
		m.eng.PoolSize(pipeline.StageConvert),
		m.eng.PoolSize(pipeline.StagePackage),
		m.eng.PoolSize(pipeline.StageCleaner),
		m.eng.PackagerName()))
	return lineJustify(left, right, m.width)
}

func (m Model) renderSources() string {
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

func (m Model) renderTotal() string {
	var disc, done, fail int
	for _, src := range m.order {
		a := m.aggregate(src)
		disc += a.disc
		done += a.done
		fail += a.fail
	}
	ratio := 0.0
	if disc > 0 {
		ratio = float64(done) / float64(disc)
	}
	eta := "--:--"
	if rate := m.totalRate(); rate > 0 {
		if remaining := disc - done - fail; remaining > 0 {
			eta = fmtDuration(float64(remaining) / rate)
		} else {
			eta = "00:00"
		}
	}
	return fmt.Sprintf("TOTAL %d/%d  %s  ETA %s  %.1f MB/s",
		done, disc, m.prog.ViewAs(ratio), eta, m.mbps)
}

func (m Model) renderTracks() string {
	title := "TRACKS"
	if q := m.search.Value(); q != "" && !m.searchActive {
		title += fmt.Sprintf("  (filter: %q)", q)
	}
	header := headerStyle.Render(title) + dimStyle.Render(fmt.Sprintf("  %d shown", len(m.tracks)))

	searchLine := ""
	if m.searchActive {
		searchLine = m.search.View() + "\n"
	}

	rows := m.height - 16
	if rows < 3 {
		rows = 3
	}
	start := 0
	if m.sel >= rows {
		start = m.sel - rows + 1
	}
	end := start + rows
	if end > len(m.tracks) {
		end = len(m.tracks)
	}

	var lines []string
	if len(m.tracks) == 0 {
		lines = append(lines, dimStyle.Render("(no tracks yet — discovery/downloads in progress)"))
	}
	for i := start; i < end; i++ {
		t := m.tracks[i]
		line := displayLine(t, m.nowPlaying)
		if i == m.sel {
			lines = append(lines, selStyle.Render(padRight(line, m.width-6)))
		} else {
			lines = append(lines, truncate(statusGlyph(t)+" "+line, m.width-6))
		}
	}
	body := strings.Join(lines, "\n")
	return header + "\n" + searchLine + boxStyle.Width(m.width-2).Render(body)
}

func (m Model) renderNowPlaying() string {
	backend := m.play.Backend()
	if backend == "" {
		backend = "none"
	}
	np := dimStyle.Render("—")
	if m.nowPlaying != "" {
		for _, t := range m.tracks {
			if t.ID == m.nowPlaying {
				np = playStyle.Render("▶ " + displayTitle(t))
				break
			}
		}
	}
	return fmt.Sprintf("player[%s]: %s   %s", backend, np, dimStyle.Render(m.statusLine))
}

func (m Model) renderFooter() string {
	return footerStyle.Render(fmt.Sprintf(
		"[s]tart [p]ause  pool:%s [d/c/k/x]+/-  [↑↓]nav [enter]play [space]stop  [/]search [r]efresh [R]etry [q]uit",
		poolStages[m.focusPool]))
}

// --- helpers ---

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

func statusGlyph(t *core.Track) string {
	switch t.Status {
	case core.StatusDone:
		return okStyle.Render("●")
	case core.StatusFailed:
		return failStyle.Render("✗")
	case core.StatusSkipped:
		return dimStyle.Render("·")
	case core.StatusDiscovered:
		return dimStyle.Render("○")
	default:
		return playStyle.Render("◐")
	}
}

func displayTitle(t *core.Track) string {
	name := t.Title
	if name == "" {
		name = t.Work
	}
	if name == "" {
		name = t.SourceItem
	}
	if name == "" {
		name = t.ID
	}
	if t.Composer != "" {
		return fmt.Sprintf("%s — %s", t.Composer, name)
	}
	return fmt.Sprintf("[%s] %s", t.Source, name)
}

func displayLine(t *core.Track, nowID string) string {
	now := ""
	if t.ID == nowID {
		now = "♪ "
	}
	st := t.Status
	if len(st) > 4 {
		st = st[:4]
	}
	s := fmt.Sprintf("%s%-4s %s", now, st, displayTitle(t))
	if t.Status == core.StatusDone {
		s += fmt.Sprintf("  [opus %s caf %s]", human(t.OpusBytes), human(t.CafBytes))
	}
	return s
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
