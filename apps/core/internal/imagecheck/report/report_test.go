package report

import (
	"bytes"
	"testing"
	"time"
)

func TestPrinter(t *testing.T) {
	var b bytes.Buffer
	p := New(&b, 19)
	p.Step("host", 4, "the installed disk boots")
	p.OK("host", "fine")
	p.Took("host", 4, 187*time.Second)
	p.Prefix = true
	p.Step("under", 14, "a blank disk")
	p.Fail("under", "broke")
	want := "\nprogress: 0%\n▸ 4. the installed disk boots\n  ✓ fine\n  step 4 took 3 min 07 s\n" +
		"\n[under] progress: 5%\n[under] ▸ 14. a blank disk\n[under]   ✗ broke\n"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}
}
