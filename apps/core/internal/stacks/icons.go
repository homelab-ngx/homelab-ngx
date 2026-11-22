package stacks

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The forms of a version 9 icon, as IconKind tells them apart.
const (
	// IconURL is an https URL.
	IconURL = "url"
	// IconFile is a file in the stack's repo, relative to its root.
	IconFile = "file"
	// IconSlug is a name in the dashboard-icons set, such as jellyfin.
	IconSlug = "slug"
)

// IconKind is the form of icon s: a URL when it starts with https://, a
// file in the repo when it holds a '/' or a '.', and a slug otherwise. It
// does not check s; Validate does.
func IconKind(s string) string {
	switch {
	case strings.HasPrefix(s, "https://"):
		return IconURL
	case strings.ContainsAny(s, "/."):
		return IconFile
	default:
		return IconSlug
	}
}

// maxIconURL bounds an icon URL.
const maxIconURL = 500

// MaxIconFile bounds an icon file in a stack's repo.
const MaxIconFile = 256 << 10

// iconExts are the image types an icon file may be.
var iconExts = []string{".svg", ".png", ".webp"}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// validIcon checks one icon in any of its forms.
func validIcon(s string) error {
	switch IconKind(s) {
	case IconURL:
		u, err := url.Parse(s)
		if err != nil || u.Host == "" || len(s) > maxIconURL || strings.ContainsAny(s, " \t\r\n") {
			return fmt.Errorf("%q is not an https URL of at most %d bytes", s, maxIconURL)
		}
	case IconFile:
		if strings.Contains(s, "://") {
			return fmt.Errorf("%q: a URL must start with https://", s)
		}
		if strings.HasPrefix(s, "/") || path.Clean(s) != s || s == ".." || strings.HasPrefix(s, "../") ||
			strings.ContainsAny(s, "\\:\n\x00") {
			return fmt.Errorf("%q must be a plain path in the repo, such as icons/app.svg", s)
		}
		if !slices.Contains(iconExts, strings.ToLower(path.Ext(s))) {
			return fmt.Errorf("%q must be an .svg, .png or .webp file", s)
		}
	default:
		if !slugRE.MatchString(s) {
			return fmt.Errorf("%q is not an https URL, a file in the repo or an icon name of a-z, 0-9 and '-'", s)
		}
	}
	return nil
}

// validateIcons checks the version 9 icon and services.
func (m Manifest) validateIcons() error {
	if m.Version < 9 {
		if m.Icon != "" || len(m.Services) > 0 {
			return errors.New("version: icon and services need version 9")
		}
		return nil
	}
	if m.Icon != "" {
		if err := validIcon(m.Icon); err != nil {
			return fmt.Errorf("icon: %w", err)
		}
	}
	for i, s := range m.Services {
		f := fmt.Sprintf("services[%d]", i)
		if !unitRE.MatchString(s.Unit) {
			return fmt.Errorf("%s.unit: %q is not a .service unit name", f, s.Unit)
		}
		if slices.ContainsFunc(m.Services[:i], func(o ServiceMeta) bool { return o.Unit == s.Unit }) {
			return fmt.Errorf("%s.unit: %q is listed twice", f, s.Unit)
		}
		if s.Icon == "" {
			return fmt.Errorf("%s.icon: a service needs an icon", f)
		}
		if err := validIcon(s.Icon); err != nil {
			return fmt.Errorf("%s.icon: %w", f, err)
		}
	}
	return nil
}

// iconFiles are the icons of m that are files in the repo, each with the
// field that names it.
func (m Manifest) iconFiles() (fields, files []string) {
	if m.Icon != "" && IconKind(m.Icon) == IconFile {
		fields, files = append(fields, "icon"), append(files, m.Icon)
	}
	for i, s := range m.Services {
		if IconKind(s.Icon) == IconFile {
			fields, files = append(fields, fmt.Sprintf("services[%d].icon", i)), append(files, s.Icon)
		}
	}
	return fields, files
}

// CheckIconFiles checks that every icon m names as a file is a regular file
// of at most MaxIconFile bytes in the repo tree at root. A symbolic link on
// the way is refused, so an icon cannot point outside the tree.
func CheckIconFiles(root string, m Manifest) error {
	fields, files := m.iconFiles()
	for i, file := range files {
		fi, err := lstatNoLinks(root, file)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("%s: %s is not in the repo", fields[i], file)
		case err != nil:
			return fmt.Errorf("%s: %w", fields[i], err)
		case !fi.Mode().IsRegular():
			return fmt.Errorf("%s: %s is not a plain file", fields[i], file)
		case fi.Size() > MaxIconFile:
			return fmt.Errorf("%s: %s is larger than %d KiB", fields[i], file, MaxIconFile>>10)
		}
	}
	return nil
}

// lstatNoLinks stats rel under root, refusing a symbolic link at any step.
// The last step's link is returned for the caller to refuse.
func lstatNoLinks(root, rel string) (fs.FileInfo, error) {
	parts := strings.Split(rel, "/")
	p := root
	for i, part := range parts {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if err != nil || i == len(parts)-1 {
			return fi, err
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("%s is not a plain file", rel)
		}
	}
	return nil, fs.ErrNotExist // rel is never empty
}
