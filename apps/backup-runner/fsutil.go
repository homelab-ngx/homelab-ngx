package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// mountinfoPath is where mounted filesystems are read from; tests replace it.
var mountinfoPath = "/proc/self/mountinfo"

// checkSource refuses a folder that is not there, is not a directory, or sits
// on a pool that is not mounted. An unmounted pool's mountpoint is an empty
// directory, and copying that would look like "everything was deleted".
func checkSource(path string, poolMounts []string) error {
	for _, m := range poolMounts {
		m = filepath.Clean(m)
		if path != m && !strings.HasPrefix(path, strings.TrimSuffix(m, "/")+"/") {
			continue
		}
		mounted, err := isMountpoint(m)
		if err != nil {
			return fmt.Errorf("cannot tell whether pool %s is mounted: %w", m, err)
		}
		if !mounted {
			return fmt.Errorf("pool %s is not mounted", m)
		}
	}
	fi, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return fmt.Errorf("%s does not exist", path)
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s is not a folder", path)
	}
	return nil
}

func isMountpoint(dir string) (bool, error) {
	f, err := os.Open(mountinfoPath)
	if err != nil {
		return false, err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadString('\n')
		// Field 5 is the mount point, with space, tab, newline and backslash
		// written as octal escapes.
		if fields := strings.Fields(line); len(fields) >= 5 && unescapeMount(fields[4]) == dir {
			return true, nil
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}

func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func dirEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
}

// expandFiles returns the existing paths under root that match any of the
// patterns (relative to root; literals, *, ** and ?). A pattern without **
// is globbed directly, so a large tree is only walked when a pattern asks.
func expandFiles(root string, patterns []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	var deep []*regexp.Regexp
	for _, p := range patterns {
		rel := strings.TrimPrefix(p, "/")
		if !strings.Contains(rel, "**") {
			// Escape what Glob treats specially but the pattern syntax does not.
			g := strings.NewReplacer(`\`, `\\`, `[`, `\[`).Replace(rel)
			matches, err := filepath.Glob(filepath.Join(globQuote(root), g))
			if err != nil {
				return nil, fmt.Errorf("pattern %q: %w", p, err)
			}
			for _, m := range matches {
				add(m)
			}
			continue
		}
		deep = append(deep, patternRegexp(p))
	}
	if len(deep) > 0 {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entries are the tool's to report
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil || rel == "." {
				return nil
			}
			for _, re := range deep {
				if re.MatchString("/" + rel) {
					add(path)
					if d.IsDir() {
						return filepath.SkipDir
					}
					break
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func globQuote(s string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(s)
}

// patternRegexp turns a pattern into a regexp over "/"+relative path. A
// pattern that does not start with / matches at any depth.
func patternRegexp(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	if !strings.HasPrefix(p, "/") {
		b.WriteString("(?:.*/)?")
		p = strings.TrimPrefix(p, "**/")
	} else {
		b.WriteString("/")
		p = p[1:]
	}
	for i := 0; i < len(p); i++ {
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case p[i] == '*':
			b.WriteString("[^/]*")
		case p[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(p[i])))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// bwlimitKiB converts an rclone-style rate ("4M", "512k", "100") to KiB/s for
// restic. rclone's default unit is KiB/s. ok is false for anything else, such
// as a timetable, which restic cannot express.
func bwlimitKiB(s string) (kib int64, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "off" {
		return 0, false
	}
	mult := float64(1)
	switch s[len(s)-1] {
	case 'B', 'b':
		mult, s = 1.0/1024, s[:len(s)-1]
	case 'K', 'k':
		s = s[:len(s)-1]
	case 'M', 'm':
		mult, s = 1024, s[:len(s)-1]
	case 'G', 'g':
		mult, s = 1024*1024, s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	kib = int64(n * mult)
	if kib < 1 {
		kib = 1
	}
	return kib, true
}
