// Package dedup identifies the same recording appearing under multiple sources
// and collapses each cluster to a single canonical track, so only one copy is
// browsed, searched, and uploaded to R2.
package dedup

import (
	"context"
	"fmt"
	"math/bits"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
)

// Options configure a dedup pass.
type Options struct {
	Dir       string
	Force     bool // re-fingerprint even if already fingerprinted
	BER       float64
	DurTolSec float64
	Progress  func(done, total int)
}

// Run fingerprints tracks, clusters duplicates, and marks non-canonical rows.
func Run(ctx context.Context, store *db.DB, opt Options) error {
	if opt.BER == 0 {
		opt.BER = 0.20
	}
	if opt.DurTolSec == 0 {
		opt.DurTolSec = 3
	}
	haveFpcalc := fpcalcAvailable()
	if !haveFpcalc {
		fmt.Println("dedup: fpcalc (chromaprint) not found — falling back to exact size+duration+sha1 clustering")
	}

	// 1. Fingerprint.
	todo, err := store.ListForFingerprint(ctx, opt.Force)
	if err != nil {
		return err
	}
	for i, t := range todo {
		fp := ""
		if haveFpcalc {
			fp, _ = fingerprint(ctx, filepath.Join(opt.Dir, t.CafPath))
		}
		if fp == "" {
			// Fall back to an exact-match signature so clustering still works.
			fp = "exact:" + strconv.FormatInt(t.CafBytes, 10) + ":" + strconv.Itoa(int(t.DurationSec)) + ":" + t.OriginalSHA1
		}
		if err := store.SetFingerprint(ctx, t.ID, fp); err != nil {
			return err
		}
		if opt.Progress != nil {
			opt.Progress(i+1, len(todo))
		}
	}

	// 2. Cluster.
	tracks, err := store.ListFingerprinted(ctx)
	if err != nil {
		return err
	}
	clusters := cluster(tracks, opt.BER, opt.DurTolSec)

	// 3. Mark duplicates.
	var dupCount int
	for _, cl := range clusters {
		if len(cl) < 2 {
			continue
		}
		canon := pickCanonical(cl)
		for _, t := range cl {
			if t.ID == canon.ID {
				continue
			}
			if err := store.MarkDuplicate(ctx, t.ID, canon.ID); err != nil {
				return err
			}
			dupCount++
		}
	}
	fmt.Printf("dedup: %d tracks, %d clusters with duplicates, %d rows marked duplicate\n",
		len(tracks), countMultiClusters(clusters), dupCount)
	return nil
}

func fpcalcAvailable() bool {
	_, err := exec.LookPath("fpcalc")
	return err == nil
}

// fingerprint returns the raw Chromaprint fingerprint (comma-separated uint32s).
func fingerprint(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, "fpcalc", "-raw", "-length", "120", path).Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "FINGERPRINT=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "FINGERPRINT=")), nil
		}
	}
	return "", fmt.Errorf("no fingerprint line")
}

type parsedFP struct {
	track *core.Track
	words []uint32 // empty for exact-match fallback signatures
	exact string   // non-empty for exact-match fallback
}

func parse(t *core.Track) parsedFP {
	fp := t.AudioFingerprint
	if strings.HasPrefix(fp, "exact:") {
		return parsedFP{track: t, exact: fp}
	}
	parts := strings.Split(fp, ",")
	words := make([]uint32, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(p), 10, 32)
		if err != nil {
			continue
		}
		words = append(words, uint32(n))
	}
	return parsedFP{track: t, words: words}
}

// cluster groups tracks into duplicate sets using union-find. Comparisons are
// limited to tracks within DurTolSec of each other (bucketed by duration) to
// keep it near-linear.
func cluster(tracks []*core.Track, maxBER, durTol float64) [][]*core.Track {
	n := len(tracks)
	parsed := make([]parsedFP, n)
	uf := newUnionFind(n)
	// Bucket by integer second so we only compare nearby-duration tracks.
	buckets := map[int][]int{}
	for i, t := range tracks {
		parsed[i] = parse(t)
		b := int(t.DurationSec)
		buckets[b] = append(buckets[b], i)
	}
	span := int(durTol) + 1
	for i, pi := range parsed {
		bi := int(pi.track.DurationSec)
		for db2 := -span; db2 <= span; db2++ {
			for _, j := range buckets[bi+db2] {
				if j <= i {
					continue
				}
				if isDup(pi, parsed[j], maxBER, durTol) {
					uf.union(i, j)
				}
			}
		}
	}
	groups := map[int][]*core.Track{}
	for i := range tracks {
		r := uf.find(i)
		groups[r] = append(groups[r], tracks[i])
	}
	out := make([][]*core.Track, 0, len(groups))
	for _, g := range groups {
		out = append(out, g)
	}
	return out
}

func isDup(a, b parsedFP, maxBER, durTol float64) bool {
	if durAbs(a.track.DurationSec, b.track.DurationSec) > durTol {
		return false
	}
	// Exact-match fallback signatures: equal size+duration+sha1 (or identical sha1).
	if a.exact != "" || b.exact != "" {
		if a.track.OriginalSHA1 != "" && a.track.OriginalSHA1 == b.track.OriginalSHA1 {
			return true
		}
		return a.exact != "" && a.exact == b.exact
	}
	if a.track.OriginalSHA1 != "" && a.track.OriginalSHA1 == b.track.OriginalSHA1 {
		return true
	}
	return berMatch(a.words, b.words, maxBER)
}

// berMatch reports whether two chromaprint fingerprints match, searching a small
// range of offsets for the best (lowest) bit error rate.
func berMatch(a, b []uint32, maxBER float64) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	best := 1.0
	const maxOffset = 12
	for off := -maxOffset; off <= maxOffset; off++ {
		var diffBits, totalBits int
		for i := range a {
			j := i + off
			if j < 0 || j >= len(b) {
				continue
			}
			diffBits += bits.OnesCount32(a[i] ^ b[j])
			totalBits += 32
		}
		if totalBits < 32*20 { // require a meaningful overlap
			continue
		}
		ber := float64(diffBits) / float64(totalBits)
		if ber < best {
			best = ber
		}
	}
	return best <= maxBER
}

// pickCanonical selects the representative of a duplicate cluster by license
// permissiveness, then source trust, then largest CAF, then oldest (ULID id).
func pickCanonical(cl []*core.Track) *core.Track {
	best := cl[0]
	for _, t := range cl[1:] {
		if betterCanonical(t, best) {
			best = t
		}
	}
	return best
}

func betterCanonical(a, b *core.Track) bool {
	if la, lb := licenseRank(a), licenseRank(b); la != lb {
		return la > lb
	}
	if sa, sb := sourceRank(a.Source), sourceRank(b.Source); sa != sb {
		return sa > sb
	}
	if a.CafBytes != b.CafBytes {
		return a.CafBytes > b.CafBytes
	}
	return a.ID < b.ID // ULIDs sort by creation time; earlier = older
}

func licenseRank(t *core.Track) int {
	s := strings.ToLower(t.LicenseShort + " " + t.LicenseURL)
	switch {
	case strings.Contains(s, "cc0"), strings.Contains(s, "publicdomain/zero"):
		return 4
	case strings.Contains(s, "public domain"), strings.Contains(s, "publicdomain"), strings.Contains(s, "pd"):
		return 3
	case strings.Contains(s, "by-sa"):
		return 1
	case strings.Contains(s, "by"):
		return 2
	default:
		return 0
	}
}

// sourceRank prefers better-structured sources when choosing a canonical.
var sourceRankMap = map[string]int{
	"bach_wtc1": 60, "goldberg": 58, "chopin": 56, "beethoven_pitman": 54,
	"commons_classical": 40,
	"navy":              20, "marine": 20, "airforce": 20, "army": 20, "coastguard": 20,
}

func sourceRank(src string) int {
	if r, ok := sourceRankMap[src]; ok {
		return r
	}
	return 10
}

func durAbs(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func countMultiClusters(clusters [][]*core.Track) int {
	n := 0
	for _, c := range clusters {
		if len(c) > 1 {
			n++
		}
	}
	return n
}

// unionFind is a small disjoint-set structure.
type unionFind struct{ parent, rank []int }

func newUnionFind(n int) *unionFind {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &unionFind{parent: p, rank: make([]int, n)}
}

func (u *unionFind) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if u.rank[ra] < u.rank[rb] {
		ra, rb = rb, ra
	}
	u.parent[rb] = ra
	if u.rank[ra] == u.rank[rb] {
		u.rank[ra]++
	}
}
