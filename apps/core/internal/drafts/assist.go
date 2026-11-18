package drafts

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/llm"
	"homelab-ngx.com/core/internal/stacks"
)

// The assistant: one request to the configured language model with the
// draft's files, the latest check and build, and the authoring guide. The
// answer is notes plus whole files, which the editor shows as a diff for the
// operator to apply. The model sees no variable value and no secret: only
// the two files and what Check and Build printed.

//go:embed guide.md
var guide string

// Guide is the assistant's system prompt: the authoring guide and the
// template. It is the same for every request, so a provider caches it.
func Guide() string {
	t := Template("example")
	return guide + "\n" + fileBlock(stacks.ManifestFile, t.Manifest) + "\n" + fileBlock(stacks.FlakeFile, t.Flake)
}

func fileBlock(path, content string) string {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return fmt.Sprintf("<file path=%q>\n%s</file>\n", path, content)
}

// Assistant gives the configured provider; llm.ErrOff when there is none.
type Assistant interface {
	Provider(ctx context.Context) (llm.Provider, string, error)
}

// SetAssistant sets where the assistant's provider comes from.
func (s *Service) SetAssistant(a Assistant) { s.assistant = a }

// AssistKind is the kind of question.
type AssistKind string

const (
	AssistDraft  AssistKind = "draft"
	AssistReview AssistKind = "review"
	AssistFix    AssistKind = "fix"
	AssistAsk    AssistKind = "ask"
)

// DraftAssist is a question to the assistant about the editor's files.
type DraftAssist struct {
	Kind  AssistKind `json:"kind" enum:"draft,review,fix,ask" doc:"draft: write the stack Text describes; review: look for mistakes; fix: fix what the last check or build reported; ask: Text is the question."`
	Text  string     `json:"text,omitempty" maxLength:"4000" doc:"The description (draft), the question (ask), or extra words for review and fix."`
	Files DraftFiles `json:"files" doc:"The editor's files, saved or not."`
}

// AssistText is a piece of the answer as it arrives.
type AssistText = llm.AssistText

// AssistFiles are the files an answer proposes; absent is unchanged.
type AssistFiles struct {
	Manifest *string `json:"manifest,omitempty"`
	Flake    *string `json:"flake,omitempty"`
}

// DraftAnswer is the whole answer, read.
type DraftAnswer struct {
	Model   string          `json:"model"`
	Usage   llm.AnswerUsage `json:"usage" doc:"What the answer cost, as far as the server says."`
	Summary string          `json:"summary" doc:"What it did or found, in a sentence or two."`
	Changes []string        `json:"changes" doc:"One line per change it made."`
	Todo    []string        `json:"todo" doc:"What the operator must do themselves."`
	Notes   string          `json:"notes" doc:"Anything it said outside those parts and the file blocks."`
	Files   AssistFiles     `json:"files" doc:"Whole files it proposes."`
	// Dropped says why a block was left out: an unknown path, or an answer
	// that ended inside a block.
	Dropped []string `json:"dropped"`
}

// AssistError ends a stream that failed.
type AssistError = llm.AssistError

// assistTimeout bounds one answer; a long one on a slow local model takes
// minutes.
const assistTimeout = 15 * time.Minute

// Assist asks the assistant and streams the answer: send gets each piece
// of text, think each piece of reasoning a server sends (nil: none), and
// the answer, read, is returned.
func (s *Service) Assist(ctx context.Context, name string, in DraftAssist, send, think func(string)) (DraftAnswer, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return DraftAnswer{}, err
	}
	if s.assistant == nil {
		return DraftAnswer{}, httpx.Conflict("assistant_off", "no assistant is set up; set one up in Settings")
	}
	p, model, err := s.assistant.Provider(ctx)
	if errors.Is(err, llm.ErrOff) {
		return DraftAnswer{}, httpx.Conflict("assistant_off", "no assistant is set up; set one up in Settings")
	}
	if err != nil {
		return DraftAnswer{}, err
	}
	d, err := s.view(ctx, r)
	if err != nil {
		return DraftAnswer{}, err
	}
	user, err := prompt(d, in)
	if err != nil {
		return DraftAnswer{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, assistTimeout)
	defer cancel()
	var answer strings.Builder
	timer := llm.StartTimer()
	req := llm.Request{System: Guide(), User: user, Session: "homelab-draft-" + name, OnThinking: func(t string) {
		timer.Thinking(t)
		if think != nil {
			think(t)
		}
	}}
	usage, err := p.Stream(ctx, req, func(t string) {
		timer.Text(t)
		answer.WriteString(t)
		send(t)
	})
	s.log.Info("assistant answered", append([]any{"draft", name, "kind", in.Kind, "model", model,
		"input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens, "reasoning_tokens", usage.ReasoningTokens,
		"cost", usage.Cost, "allowance", usage.Allowance, "error", err}, timer.LogArgs()...)...)
	if err != nil {
		return DraftAnswer{}, httpx.BadGateway("assistant_failed", "%v", err)
	}
	a := ParseAnswer(answer.String())
	a.Model, a.Usage = model, usage
	return a, nil
}

// prompt is the user text of a question: what is asked, the files, and what
// Check and Build said.
func prompt(d Draft, in DraftAssist) (string, error) {
	text := strings.TrimSpace(in.Text)
	problems := CheckFiles(d.Name, in.Files)
	failed := d.Build != nil && d.Build.Result == BuildFailed
	var b strings.Builder
	switch in.Kind {
	case AssistDraft:
		if text == "" {
			return "", httpx.BadRequest("text_required", "describe the stack to write")
		}
		fmt.Fprintf(&b, "Write the stack described below, replacing the files as they are now. Keep what fits from them.\n\nDescription:\n%s\n", text)
	case AssistReview:
		b.WriteString("Review this stack for mistakes: the manifest against the rules, the module against NixOS and the rules " +
			"(services on 127.0.0.1, missing health units, a secret written into the files, grants it asks for and does not use, " +
			"grants it needs and lacks). Fix what is wrong; if nothing is, say so and change nothing.\n")
		if text != "" {
			fmt.Fprintf(&b, "\nAlso:\n%s\n", text)
		}
	case AssistFix:
		if len(problems) == 0 && !failed {
			return "", httpx.Conflict("nothing_to_fix", "the check finds no problem and the last build did not fail")
		}
		b.WriteString("Fix the problems the check and the last build report below, changing as little else as you can.\n")
		if text != "" {
			fmt.Fprintf(&b, "\nAlso:\n%s\n", text)
		}
	case AssistAsk:
		if text == "" {
			return "", httpx.BadRequest("text_required", "ask a question")
		}
		fmt.Fprintf(&b, "%s\n", text)
	default:
		return "", httpx.BadRequest("invalid_kind", "kind is draft, review, fix or ask")
	}

	fmt.Fprintf(&b, "\nThe stack is installed under the name %q.\n", d.Name)
	if in.Files != d.Files {
		b.WriteString("\nThe files as they are in the editor now (with edits not saved yet):\n\n")
	} else {
		b.WriteString("\nThe files as they are now (saved):\n\n")
	}
	b.WriteString(fileBlock(stacks.ManifestFile, in.Files.Manifest))
	b.WriteString("\n")
	b.WriteString(fileBlock(stacks.FlakeFile, in.Files.Flake))

	b.WriteString("\nCheck of these files: ")
	if len(problems) == 0 {
		b.WriteString("no problems.\n")
	} else {
		b.WriteString("\n")
		for _, p := range problems {
			fmt.Fprintf(&b, "- %s: %s\n", p.File, p.Message)
		}
	}

	b.WriteString("\nLast build: ")
	switch {
	case d.Build == nil:
		b.WriteString("none yet.\n")
	case d.Build.Result == BuildRunning:
		b.WriteString("still running.\n")
	default:
		fmt.Fprintf(&b, "%s, of the files saved as commit %s", d.Build.Result, short(d.Build.Rev))
		if d.Build.Rev != d.Rev || in.Files != d.Files {
			b.WriteString(" (the files have changed since)")
		}
		b.WriteString(".\n")
		if failed {
			msg := d.Build.Error
			if msg == "" {
				msg = tail(d.Build.Log, 60)
			}
			fmt.Fprintf(&b, "Its error:\n%s\n", msg)
		}
	}
	return b.String(), nil
}

// tail is the last n lines of s, without colours.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(ansiRE.ReplaceAllString(s, ""), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ParseAnswer splits an answer into its notes and the file blocks for the
// two known paths. Another path, or a block the answer ended inside, is
// dropped with a note saying so.
func ParseAnswer(answer string) DraftAnswer {
	a := DraftAnswer{Dropped: []string{}}
	var notes strings.Builder
	rest := answer
	for {
		i := strings.Index(rest, "<file path=")
		if i < 0 {
			notes.WriteString(rest)
			break
		}
		notes.WriteString(rest[:i])
		rest = rest[i:]
		end := strings.Index(rest, ">")
		if end < 0 {
			a.Dropped = append(a.Dropped, "The answer ended inside a file block, so it was left out.")
			break
		}
		path := strings.Trim(strings.TrimPrefix(rest[:end], "<file path="), `"'`)
		rest = rest[end+1:]
		closing := strings.Index(rest, "</file>")
		if closing < 0 {
			a.Dropped = append(a.Dropped, fmt.Sprintf("The answer ended inside %s, so it was left out.", path))
			break
		}
		content := cleanFile(rest[:closing])
		rest = rest[closing+len("</file>"):]
		switch path {
		case stacks.ManifestFile:
			a.Files.Manifest = &content
		case stacks.FlakeFile:
			a.Files.Flake = &content
		default:
			a.Dropped = append(a.Dropped, fmt.Sprintf("It proposed a file %s; a stack has only homelab-stack.json and flake.nix, so it was left out.", path))
		}
	}
	parts, rest := llm.Parts(notes.String(), "summary", "changes", "todo")
	a.Summary, a.Changes, a.Todo = parts["summary"], llm.Items(parts["changes"]), llm.Items(parts["todo"])
	a.Notes = tidyNotes(rest)
	return a
}

// cleanFile is a block's content as a file: without the line breaks around
// it and the Markdown fence a model may wrap it in despite the guide, and
// ending in one newline.
func cleanFile(s string) string {
	s = strings.TrimPrefix(s, "\r")
	s = strings.TrimPrefix(s, "\n")
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "```") && strings.HasSuffix(t, "```") && len(t) > 6 {
		if nl := strings.IndexByte(t, '\n'); nl >= 0 {
			s = strings.TrimSuffix(t[nl+1:], "```")
		}
	}
	return strings.TrimRight(s, " \t\r\n") + "\n"
}

// tidyNotes trims the notes and folds the gaps the blocks left.
func tidyNotes(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		if l == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
