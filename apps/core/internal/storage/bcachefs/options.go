package bcachefs

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
)

// FileOptions are the per-subvolume placement and copy overrides read from
// `bcachefs get-file-option`. Zero/empty means the option inherits the pool default.
type FileOptions struct {
	DataReplicas     int
	ForegroundTarget string
	BackgroundTarget string
	PromoteTarget    string
}

// FileOptionChanges are the file-option flags passed to `bcachefs set-file-option`.
// A nil pointer leaves that option unchanged; 0 or "" clears it with `-`.
type FileOptionChanges struct {
	DataReplicas     *int
	ForegroundTarget *string
	BackgroundTarget *string
	PromoteTarget    *string
}

// GetFileOptions reads per-subvolume placement and replica overrides for paths
// in a single `bcachefs get-file-option` call.
func (c *Client) GetFileOptions(ctx context.Context, paths []string) (map[string]FileOptions, error) {
	if len(paths) == 0 {
		return map[string]FileOptions{}, nil
	}
	out, err := c.run(ctx, "bcachefs", append([]string{"get-file-option"}, paths...)...)
	if err != nil {
		return nil, err
	}
	return parseFileOptions(out, paths), nil
}

func parseFileOptions(out string, paths []string) map[string]FileOptions {
	opts := make(map[string]FileOptions)
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		last := strings.LastIndexByte(line, '\t')
		if last < 0 {
			continue
		}
		beforeVal, val := line[:last], strings.TrimSpace(line[last+1:])
		if val == "" || val == "-" {
			continue
		}
		prev := strings.LastIndexByte(beforeVal, '\t')
		var p, opt string
		if prev >= 0 {
			p = filepath.Clean(beforeVal[:prev])
			opt = strings.TrimSpace(beforeVal[prev+1:])
		} else if len(paths) == 1 {
			p = filepath.Clean(paths[0])
			opt = strings.TrimSpace(beforeVal)
		} else {
			continue
		}
		cur := opts[p]
		switch opt {
		case "data_replicas":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cur.DataReplicas = n
			}
		case "foreground_target":
			cur.ForegroundTarget = val
		case "background_target":
			cur.BackgroundTarget = val
		case "promote_target":
			cur.PromoteTarget = val
		}
		opts[p] = cur
	}
	return opts
}

func setFileOptionArgs(path string, ch FileOptionChanges) []string {
	args := []string{"set-file-option"}
	if ch.DataReplicas != nil {
		if *ch.DataReplicas <= 0 {
			args = append(args, "--data_replicas=-")
		} else {
			args = append(args, "--data_replicas="+strconv.Itoa(*ch.DataReplicas))
		}
	}
	for _, opt := range []struct {
		name string
		val  *string
	}{
		{"foreground_target", ch.ForegroundTarget},
		{"background_target", ch.BackgroundTarget},
		{"promote_target", ch.PromoteTarget},
	} {
		if opt.val == nil {
			continue
		}
		v := strings.TrimSpace(*opt.val)
		if v == "" {
			v = "-"
		}
		args = append(args, "--"+opt.name+"="+v)
	}
	if len(args) == 1 {
		return nil
	}
	return append(args, path)
}

// SetFileOptions updates per-subvolume options on path via `bcachefs set-file-option`.
func (c *Client) SetFileOptions(ctx context.Context, path string, ch FileOptionChanges) error {
	args := setFileOptionArgs(path, ch)
	if len(args) == 0 {
		return nil
	}
	_, err := c.run(ctx, "bcachefs", args...)
	return err
}
