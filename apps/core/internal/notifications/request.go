package notifications

import (
	"context"
	"errors"
	"fmt"
)

// ErrInvalid marks a malformed NotifyRequest (as opposed to a delivery
// failure).
var ErrInvalid = errors.New("invalid notification request")

// Apply executes a request against n. It serves the daemon API and the CLI's
// direct fallback alike.
func (n *Notifier) Apply(ctx context.Context, req NotifyRequest) (NotifyResponse, error) {
	var res Result
	var out NotifyResponse
	var err error
	switch req.Action {
	case Report:
		if req.Key == "" || req.Title == "" {
			return out, fmt.Errorf("%w: report needs key and title", ErrInvalid)
		}
		res, err = n.Report(ctx, Problem{Key: req.Key, Title: req.Title, Detail: req.Body, Path: req.Path})
	case Resolve:
		if req.Key == "" {
			return out, fmt.Errorf("%w: resolve needs key", ErrInvalid)
		}
		res, err = n.Resolve(ctx, req.Key)
	case Send:
		if req.Title == "" {
			return out, fmt.Errorf("%w: send needs title", ErrInvalid)
		}
		sev := req.Severity
		if sev != Alert {
			sev = Info
		}
		out.Channel, err = n.Send(ctx, Message{Severity: sev, Title: req.Title, Body: req.Body, Path: req.Path, Key: req.Key})
	default:
		err = fmt.Errorf("%w: unknown action %q", ErrInvalid, req.Action)
	}
	out.Opened, out.Reminded, out.Resolved = nonNil(res.Opened), nonNil(res.Reminded), nonNil(res.Resolved)
	return out, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
