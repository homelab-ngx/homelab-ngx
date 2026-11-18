package llm

import (
	"slices"
	"testing"
)

func TestPartsTakesTheTaggedPartsAndLeavesTheRest(t *testing.T) {
	parts, rest := Parts("Intro.\n<summary> Fixed the port. </summary>\n\n\n<todo>\n- Fill in TOKEN\n2. Make /pool/x\n  as root\n</todo>\nBye.", "summary", "changes", "todo")
	if parts["summary"] != "Fixed the port." || rest != "Intro.\n\nBye." {
		t.Fatalf("parts %q, rest %q", parts, rest)
	}
	if _, ok := parts["changes"]; ok {
		t.Fatal("an absent part is not there")
	}
	if got := Items(parts["todo"]); !slices.Equal(got, []string{"Fill in TOKEN", "Make /pool/x as root"}) {
		t.Fatalf("items %q", got)
	}
}

func TestPartsCutAnswerRunsToTheEnd(t *testing.T) {
	parts, rest := Parts("<summary>Half a sen", "summary")
	if parts["summary"] != "Half a sen" || rest != "" {
		t.Fatalf("parts %q, rest %q", parts, rest)
	}
	if got := Items(""); len(got) != 0 {
		t.Fatalf("items %q", got)
	}
}
