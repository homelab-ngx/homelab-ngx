package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"homelab-ngx.com/core/internal/llm"
)

// Severity is how much a finding matters on this host.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityInfo     Severity = "info"
)

// severityRank orders findings, most severe first.
var severityRank = map[Severity]int{SeverityCritical: 0, SeverityHigh: 1, SeverityMedium: 2, SeverityLow: 3, SeverityInfo: 4}

// severityWords are what a model may write for each severity.
var severityWords = map[string]Severity{
	"critical": SeverityCritical, "high": SeverityHigh, "medium": SeverityMedium, "moderate": SeverityMedium,
	"low": SeverityLow, "info": SeverityInfo, "informational": SeverityInfo, "note": SeverityInfo,
}

// Posture is the summary's overall verdict.
type Posture string

const (
	PostureGood Posture = "good"
	PostureFair Posture = "fair"
	PostureWeak Posture = "weak"
	PosturePoor Posture = "poor"
)

// Caps on what one answer may hold.
const (
	maxFindings  = 10
	maxStrengths = 4
	maxFirst     = 5
	maxText      = 1200
)

// Finding is one problem the audit found in an area.
type Finding struct {
	ID       string   `json:"id" doc:"Unique within the audit, such as exposure-2."`
	Area     Area     `json:"area" enum:"exposure,access,stacks,secrets,updates"`
	Severity Severity `json:"severity" enum:"critical,high,medium,low,info"`
	Title    string   `json:"title"`
	Evidence string   `json:"evidence" doc:"The line of the material that shows it, quoted by the model."`
	Risk     string   `json:"risk" doc:"What could happen, and who could do it."`
	Fix      string   `json:"fix" doc:"What to change: a dashboard action or the NixOS option."`
	Verified bool     `json:"verified" doc:"The evidence quote was found in the material the model was given. False: the model may have made it up."`
}

// chunkAnswer is what a chunk's model must answer, inside <report>.
type chunkAnswer struct {
	Findings []struct {
		Severity string `json:"severity"`
		Title    string `json:"title"`
		Evidence string `json:"evidence"`
		Risk     string `json:"risk"`
		Fix      string `json:"fix"`
	} `json:"findings"`
	Strengths []string `json:"strengths"`
}

// summaryAnswer is what the summary pass must answer, inside <report>.
type summaryAnswer struct {
	Summary string   `json:"summary"`
	Posture Posture  `json:"posture"`
	First   []string `json:"first"`
}

// reportJSON is the JSON inside an answer's <report> part, without a code
// fence the model may have put around it.
func reportJSON(answer string) (string, error) {
	parts, _ := llm.Parts(answer, "report")
	body := strings.TrimSpace(parts["report"])
	if body == "" {
		return "", errors.New("the answer had no <report> part")
	}
	body = strings.TrimPrefix(body, "```json")
	body = strings.TrimPrefix(body, "```")
	body = strings.TrimSuffix(body, "```")
	return strings.TrimSpace(body), nil
}

// parseChunk reads a chunk's answer: findings checked against the material
// they quote, sorted most severe first and numbered, and the strengths.
func parseChunk(area Area, answer, material string) ([]Finding, []string, error) {
	body, err := reportJSON(answer)
	if err != nil {
		return nil, nil, err
	}
	var a chunkAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		return nil, nil, fmt.Errorf("the report is not the JSON asked for: %v", err)
	}
	haystack := squash(material)
	out := make([]Finding, 0, len(a.Findings))
	for _, f := range a.Findings {
		sev, ok := severityWords[strings.ToLower(strings.TrimSpace(f.Severity))]
		if !ok {
			return nil, nil, fmt.Errorf("a finding has severity %q, not one of critical, high, medium, low or info", f.Severity)
		}
		title := clip(f.Title)
		if title == "" {
			continue
		}
		evidence := clip(strings.Trim(strings.TrimSpace(f.Evidence), "\"'`"))
		out = append(out, Finding{
			Area: area, Severity: sev, Title: title, Evidence: evidence,
			Risk: clip(f.Risk), Fix: clip(f.Fix),
			Verified: evidence != "" && strings.Contains(haystack, squash(evidence)),
		})
	}
	slices.SortStableFunc(out, func(x, y Finding) int { return severityRank[x.Severity] - severityRank[y.Severity] })
	if len(out) > maxFindings {
		out = out[:maxFindings]
	}
	for i := range out {
		out[i].ID = fmt.Sprintf("%s-%d", area, i+1)
	}
	strengths := []string{}
	for _, s := range a.Strengths {
		if s = clip(s); s != "" && len(strengths) < maxStrengths {
			strengths = append(strengths, s)
		}
	}
	return out, strengths, nil
}

// parseSummary reads the summary pass's answer; ids it names that no
// finding has are dropped.
func parseSummary(answer string, ids map[string]bool) (summaryAnswer, error) {
	body, err := reportJSON(answer)
	if err != nil {
		return summaryAnswer{}, err
	}
	var a summaryAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		return summaryAnswer{}, fmt.Errorf("the report is not the JSON asked for: %v", err)
	}
	a.Posture = Posture(strings.ToLower(strings.TrimSpace(string(a.Posture))))
	switch a.Posture {
	case PostureGood, PostureFair, PostureWeak, PosturePoor:
	default:
		return summaryAnswer{}, fmt.Errorf("the posture %q is not one of good, fair, weak or poor", a.Posture)
	}
	a.Summary = clip(a.Summary)
	first := []string{}
	for _, id := range a.First {
		id = strings.TrimSpace(id)
		if ids[id] && !slices.Contains(first, id) && len(first) < maxFirst {
			first = append(first, id)
		}
	}
	a.First = first
	return a, nil
}

// squash is s in lower case with every run of white space one space, so a
// quote matches across line breaks and indentation.
func squash(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), unicode.IsSpace), " ")
}

// clip trims s and cuts it to maxText.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxText {
		return s[:maxText] + "…"
	}
	return s
}
