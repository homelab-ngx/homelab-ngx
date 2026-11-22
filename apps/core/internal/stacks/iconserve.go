package stacks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"homelab-ngx.com/core/internal/httpx"
)

// DashboardIconsURL is where an icon named by slug is fetched from: the
// dashboard-icons set on jsDelivr, with svg/<slug>.svg and png/<slug>.png.
const DashboardIconsURL = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons"

const (
	// maxSlugIcon bounds an icon downloaded from the set.
	maxSlugIcon = 512 << 10
	// iconMissFor is how long a slug the set does not have is not asked
	// for again, and iconErrFor how long a failed download is not retried.
	iconMissFor = time.Hour
	iconErrFor  = time.Minute
	// iconFetchTimeout bounds one download from the set.
	iconFetchTimeout = 15 * time.Second
)

// Icon is a stack's or a service's icon as the API serves it: the image and
// its type, or URL, an https address the browser is sent to.
type Icon struct {
	Type string
	Data []byte
	URL  string
}

// iconTypes are the image types an icon is served as, by extension.
var iconTypes = map[string]string{".svg": "image/svg+xml", ".png": "image/png", ".webp": "image/webp"}

// Icon returns the icon of stack name, or of its service unit when unit is
// set (the unit as the manifest's services list names it). It is read from
// the approved commit's manifest and tree, or the fetched one's while
// nothing is approved.
func (s *Service) Icon(ctx context.Context, name, unit string) (Icon, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Icon{}, err
	}
	man, url := r.FetchedManifest, r.FetchedUrl
	if r.ApprovedRev != "" {
		man, url = r.ApprovedManifest, r.ApprovedUrl
	}
	m, err := decode(man)
	if err != nil {
		return Icon{}, fmt.Errorf("stack %s: %w", name, err)
	}
	return s.icons.icon(ctx, m, url, unit)
}

// iconCache resolves icons. Icons from the set are kept in dir, one file per
// slug; a file in the repo is read from the commit's tree in trees.
type iconCache struct {
	dir    string // "": icons from the set are fetched each time
	base   string
	client *http.Client
	trees  *treeCache
	now    func() time.Time
	log    *slog.Logger

	flight singleflight.Group
	mu     sync.Mutex
	misses map[string]iconMiss // slug: the answer until it may be asked again
}

type iconMiss struct {
	until time.Time
	err   error
}

func newIconCache(dir string, trees *treeCache, log *slog.Logger) *iconCache {
	return &iconCache{
		dir: dir, base: DashboardIconsURL, client: &http.Client{Timeout: iconFetchTimeout},
		trees: trees, now: time.Now, log: log, misses: map[string]iconMiss{},
	}
}

func noIcon(f string, a ...any) *httpx.Problem { return httpx.NotFound("no_icon", f, a...) }

// icon resolves the icon m names for the stack (unit empty) or for unit.
// url is the locked flake reference of the commit m was read from.
func (c *iconCache) icon(ctx context.Context, m Manifest, url, unit string) (Icon, error) {
	ref := m.Icon
	if unit != "" {
		ref = ""
		for _, s := range m.Services {
			if s.Unit == unit {
				ref = s.Icon
			}
		}
		if ref == "" {
			return Icon{}, noIcon("the manifest lists no service %q", unit)
		}
	}
	if ref == "" {
		return Icon{}, noIcon("the manifest names no icon")
	}
	switch IconKind(ref) {
	case IconURL:
		return Icon{URL: ref}, nil
	case IconFile:
		return c.file(ctx, url, ref)
	default:
		return c.slug(ref)
	}
}

// file reads rel from the commit's tree. A symbolic link on the way, a file
// that is not plain and one over MaxIconFile are refused.
func (c *iconCache) file(ctx context.Context, url, rel string) (Icon, error) {
	typ := iconTypes[strings.ToLower(path.Ext(rel))]
	if typ == "" || url == "" {
		return Icon{}, noIcon("%s is not an icon file", rel)
	}
	root, err := c.trees.storePath(ctx, url)
	if err != nil {
		return Icon{}, err
	}
	fi, err := lstatNoLinks(root, rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Icon{}, noIcon("%s is not in the repo", rel)
	case err != nil:
		return Icon{}, httpx.Unprocessable("invalid_icon", "%v", err)
	case !fi.Mode().IsRegular():
		return Icon{}, httpx.Unprocessable("invalid_icon", "%s is not a plain file", rel)
	case fi.Size() > MaxIconFile:
		return Icon{}, httpx.Unprocessable("invalid_icon", "%s is larger than %d KiB", rel, MaxIconFile>>10)
	}
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return Icon{}, err
	}
	return Icon{Type: typ, Data: data}, nil
}

// slug serves an icon from the set: from dir when it was fetched before,
// otherwise downloaded once, the SVG first and the PNG when there is none.
// A slug the set does not have is remembered for iconMissFor, a failed
// download for iconErrFor.
func (c *iconCache) slug(slug string) (Icon, error) {
	if !slugRE.MatchString(slug) {
		return Icon{}, noIcon("%q is not an icon name", slug)
	}
	if ic, ok := c.onDisk(slug); ok {
		return ic, nil
	}
	c.mu.Lock()
	miss, ok := c.misses[slug]
	c.mu.Unlock()
	if ok && c.now().Before(miss.until) {
		return Icon{}, miss.err
	}
	v, err, _ := c.flight.Do("slug "+slug, func() (any, error) {
		if ic, ok := c.onDisk(slug); ok {
			return ic, nil
		}
		ic, err := c.download(slug)
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil {
			wait := iconErrFor
			var p *httpx.Problem
			if errors.As(err, &p) && p.Code == "no_icon" {
				wait = iconMissFor
			}
			c.misses[slug] = iconMiss{until: c.now().Add(wait), err: err}
			return nil, err
		}
		delete(c.misses, slug)
		return ic, nil
	})
	if err != nil {
		return Icon{}, err
	}
	return v.(Icon), nil
}

// onDisk is the slug's icon as fetched before.
func (c *iconCache) onDisk(slug string) (Icon, bool) {
	if c.dir == "" {
		return Icon{}, false
	}
	for _, ext := range []string{".svg", ".png"} {
		if data, err := os.ReadFile(filepath.Join(c.dir, slug+ext)); err == nil {
			return Icon{Type: iconTypes[ext], Data: data}, true
		}
	}
	return Icon{}, false
}

// download fetches the slug's SVG, or its PNG when the set has no SVG, and
// keeps it in dir. The request is not tied to the caller's, so a caller
// that goes away does not fail the others waiting on the same download.
func (c *iconCache) download(slug string) (Icon, error) {
	ctx, cancel := context.WithTimeout(context.Background(), iconFetchTimeout)
	defer cancel()
	for _, ext := range []string{".svg", ".png"} {
		u := c.base + "/" + ext[1:] + "/" + slug + ext
		data, found, err := c.get(ctx, u)
		if err != nil {
			return Icon{}, httpx.BadGateway("icon_fetch_failed", "fetching icon %s: %v", slug, err)
		}
		if !found {
			continue
		}
		c.keep(slug+ext, data)
		return Icon{Type: iconTypes[ext], Data: data}, nil
	}
	return Icon{}, noIcon("%q is not in the dashboard-icons set", slug)
}

// get reads u: found is false when it answers 404.
func (c *iconCache) get(ctx context.Context, u string) (data []byte, found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return nil, false, nil
	case res.StatusCode != http.StatusOK:
		return nil, false, fmt.Errorf("%s answered %s", u, res.Status)
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, maxSlugIcon+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxSlugIcon {
		return nil, false, fmt.Errorf("%s is larger than %d KiB", u, maxSlugIcon>>10)
	}
	return data, true, nil
}

// keep writes a downloaded icon to dir. A failure is logged: the icon is
// still served, and fetched again next time.
func (c *iconCache) keep(name string, data []byte) {
	if c.dir == "" {
		return
	}
	err := os.MkdirAll(c.dir, 0o755)
	if err == nil {
		tmp := filepath.Join(c.dir, "."+name+".tmp")
		if err = os.WriteFile(tmp, data, 0o644); err == nil {
			err = os.Rename(tmp, filepath.Join(c.dir, name))
		}
	}
	if err != nil {
		c.log.Warn("icon not kept", "icon", name, "error", err)
	}
}
