// Package meta builds normalized search text and keyword sets for tracks.
package meta

import (
	"sort"
	"strings"
	"unicode"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"golang.org/x/text/unicode/norm"
)

// stopwords is the starter English set from the spec; classical-meaningful
// words (major, minor, sharp, flat, sonata, op, no) are deliberately kept.
var stopwords = map[string]struct{}{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by for from had has have he her his in into is it
its of on or that the their then there these they this to was were will with`) {
		stopwords[w] = struct{}{}
	}
}

// IsStopword reports whether w is in the stopword set.
func IsStopword(w string) bool {
	_, ok := stopwords[strings.ToLower(w)]
	return ok
}

// stripDiacritics lowercases and removes combining marks (NFKD then drop Mn).
func stripDiacritics(s string) string {
	s = norm.NFKD.String(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Tokenize splits on non-alphanumerics, drops length-1 tokens and stopwords,
// and returns the surviving lowercase ASCII-folded tokens in input order.
func Tokenize(text string) []string {
	folded := stripDiacritics(text)
	fields := strings.FieldsFunc(folded, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len([]rune(f)) < 2 {
			continue
		}
		if _, stop := stopwords[f]; stop {
			continue
		}
		out = append(out, f)
	}
	return out
}

// Build returns the deduped sorted keyword set and the space-joined search
// blob for a track's descriptive fields.
func Build(t *core.Track) (keywords []string, searchText string) {
	parts := []string{
		t.Title, t.Work, t.Movement, t.Composer,
		t.Performer, t.Album, t.Source,
	}
	tokens := Tokenize(strings.Join(parts, " "))

	seen := make(map[string]struct{}, len(tokens))
	for _, tok := range tokens {
		if _, ok := seen[tok]; ok {
			continue
		}
		seen[tok] = struct{}{}
		keywords = append(keywords, tok)
	}
	sort.Strings(keywords)
	searchText = strings.Join(keywords, " ")
	return keywords, searchText
}
