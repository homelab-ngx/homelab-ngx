package llm

import (
	"regexp"
	"strings"
)

// Parts takes the tagged parts an answer was asked to give, such as
// <summary>…</summary>, out of it: each name's text, trimmed, and the rest
// of the answer. A part the answer ended inside runs to the end. A model
// that ignores the format leaves everything in rest.
func Parts(answer string, names ...string) (map[string]string, string) {
	parts := map[string]string{}
	rest := answer
	for _, n := range names {
		open, end := "<"+n+">", "</"+n+">"
		i := strings.Index(rest, open)
		if i < 0 {
			continue
		}
		body := rest[i+len(open):]
		after := ""
		if j := strings.Index(body, end); j >= 0 {
			body, after = body[:j], body[j+len(end):]
		}
		parts[n] = strings.TrimSpace(body)
		rest = rest[:i] + after
	}
	return parts, strings.TrimSpace(blankRuns.ReplaceAllString(rest, "\n\n"))
}

var (
	blankRuns = regexp.MustCompile(`\n\s*\n(\s*\n)+`)
	bullet    = regexp.MustCompile(`^(?:[-*•]|\d+[.)])\s+`)
)

// Items reads a part written as a list, one item a line, without the
// bullets or numbers. An item's wrapped lines join it.
func Items(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if bullet.MatchString(l) || len(out) == 0 {
			out = append(out, bullet.ReplaceAllString(l, ""))
		} else {
			out[len(out)-1] += " " + l
		}
	}
	return out
}
