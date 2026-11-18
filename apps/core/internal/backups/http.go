package backups

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the backups slice of the API.
type HTTP struct{ svc *Service }

// NewHTTP returns the HTTP adapter for svc.
func NewHTTP(svc *Service) *HTTP { return &HTTP{svc: svc} }

type idIn struct {
	Id int64 `path:"id"`
}

type createIn struct{ Body BackupInput }

type updateIn struct {
	Id   int64 `path:"id"`
	Body BackupPatch
}

type runsIn struct {
	Id    int64 `path:"id"`
	Limit int   `query:"limit" minimum:"1" maximum:"500" default:"50"`
}

type runIn struct {
	Run int64 `path:"run"`
}

type destCreateIn struct{ Body DestinationInput }

type destUpdateIn struct {
	Id   int64 `path:"id"`
	Body DestinationInput
}

type revealIn struct {
	Id   int64 `path:"id"`
	Body RevealRequest
}

type listOut struct{ Body []Backup }
type backupOut struct{ Body Backup }
type startedOut struct{ Body BackupRunStarted }
type logsOut struct{ Body Logs }
type runsOut struct{ Body []BackupRun }
type runOut struct{ Body BackupRunDetail }
type coverageOut struct{ Body Coverage }
type destsOut struct{ Body []Destination }
type destOut struct{ Body Destination }
type destCreatedOut struct{ Body DestinationCreated }
type destTestOut struct{ Body DestinationTest }
type passwordOut struct{ Body DestinationPassword }

// Register adds the backups operations. Reading backups, runs and coverage
// is read; destinations and every change are operator.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"backups"}
	read := func(id, method, path, summary string, errs ...int) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Tags: tags, Summary: summary, Errors: errs}
	}
	httpx.Register(a, httpx.ScopeRead, read("listBackups", http.MethodGet, "/backups",
		"Backups with their sources, destinations and last/next run."), h.listBackups)
	httpx.Register(a, httpx.ScopeRead, read("getBackup", http.MethodGet, "/backups/{id}",
		"One backup.", http.StatusNotFound), h.getBackup)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "createBackup", Method: http.MethodPost, Path: "/backups", Tags: tags,
		Summary: "Create a backup.", DefaultStatus: http.StatusCreated,
		Description: "Errors: `400` with a code naming the field (`invalid_schedule`, `invalid_path`, " +
			"`invalid_pattern`, `invalid_source`, `invalid_destination`, ...); `409 name_taken`.",
		Errors: []int{http.StatusBadRequest, http.StatusConflict},
	}, h.createBackup)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "updateBackup", Method: http.MethodPatch, Path: "/backups/{id}", Tags: tags,
		Summary: "Change a backup.",
		Description: "An absent member stays as it is; `sources` and `destination_ids` replace the whole list " +
			"when present. Errors as for create, plus `404 unknown_backup`.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict},
	}, h.updateBackup)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "deleteBackup", Method: http.MethodDelete, Path: "/backups/{id}", Tags: tags,
		Summary: "Delete a backup; its runs stay in the history.", DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusNotFound, http.StatusConflict},
	}, h.deleteBackup)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "runBackup", Method: http.MethodPost, Path: "/backups/{id}/run", Tags: tags,
		Summary: "Start a backup now.", DefaultStatus: http.StatusAccepted,
		Description: "Errors: `404 unknown_backup`; `409 already_running`; `409 not_ready` when it has no " +
			"source, no destination, or a restic destination whose password is not confirmed.",
		Errors: []int{http.StatusNotFound, http.StatusConflict},
	}, h.runBackup)
	httpx.Register(a, httpx.ScopeRead, read("getBackupLogs", http.MethodGet, "/backups/{id}/logs",
		"Recent journal lines of the backup's runs.", http.StatusNotFound), h.getBackupLogs)
	httpx.Register(a, httpx.ScopeRead, read("listBackupRuns", http.MethodGet, "/backups/{id}/runs",
		"Run history of one backup (newest first).", http.StatusNotFound), h.listBackupRuns)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getBackupRun", Method: http.MethodGet, Path: "/backup-runs/{run}", Tags: tags,
		Summary: "One run with its result per destination and folder.",
		Description: "While the run is in flight the items and `progress` are live; poll it. A run that is " +
			"still preparing (prepare steps, database snapshot) has no items yet.",
		Errors: []int{http.StatusNotFound},
	}, h.getBackupRun)
	httpx.Register(a, httpx.ScopeRead, read("getBackupCoverage", http.MethodGet, "/backup-coverage",
		"Every stack's recommended folders and the backups that include them."), h.getBackupCoverage)

	httpx.Register(a, httpx.ScopeOperator, read("listBackupDestinations", http.MethodGet, "/backup-destinations",
		"Backup destinations, without their secrets."), h.listDestinations)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "createBackupDestination", Method: http.MethodPost, Path: "/backup-destinations", Tags: tags,
		Summary: "Add a destination.", DefaultStatus: http.StatusCreated,
		Description: "For a restic repository without a password the daemon generates one and returns it in " +
			"`generated_password`, this once. Errors: `400` with a code naming the field; `409 name_taken`.",
		Errors: []int{http.StatusBadRequest, http.StatusConflict},
	}, h.createDestination)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "updateBackupDestination", Method: http.MethodPut, Path: "/backup-destinations/{id}", Tags: tags,
		Summary:     "Replace a destination's settings.",
		Description: "The kind cannot change, a secret left out is kept, and a restic password never changes.",
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict},
	}, h.updateDestination)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "deleteBackupDestination", Method: http.MethodDelete, Path: "/backup-destinations/{id}", Tags: tags,
		Summary: "Remove a destination no backup uses.", DefaultStatus: http.StatusNoContent,
		Description: "Errors: `409 destination_in_use`.",
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}, h.deleteDestination)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "duplicateBackupDestination", Method: http.MethodPost, Path: "/backup-destinations/{id}/duplicate", Tags: tags,
		Summary: "Copy a destination.", DefaultStatus: http.StatusCreated,
		Description: "The copy has the same settings and secrets and is named `<name>-copy`, then `-copy2`, `-copy3` and so on.",
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}, h.duplicateDestination)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "testBackupDestination", Method: http.MethodPost, Path: "/backup-destinations/{id}/test", Tags: tags,
		Summary:     "Try a destination without copying anything.",
		Description: "Answers 200 either way; `ok` says whether the destination answered and `detail` why not.",
		Errors:      []int{http.StatusNotFound},
	}, h.testDestination)
	httpx.Register(a, httpx.ScopeOperator, read("confirmBackupDestinationPassword", http.MethodPost,
		"/backup-destinations/{id}/confirm-password",
		"Record that a restic repository's password is saved, so runs may use it.",
		http.StatusBadRequest, http.StatusNotFound), h.confirmPassword)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "revealBackupDestinationPassword", Method: http.MethodPost,
		Path: "/backup-destinations/{id}/reveal-password", Tags: tags,
		Summary:     "Show a restic repository's password.",
		Description: "`confirm` must be the destination's name (`400 confirm_mismatch` otherwise). Every reveal is logged.",
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound},
	}, h.revealPassword)
}

// problem maps a domain error to a problem.
func problem(err error) error {
	var inv *InvalidError
	switch {
	case errors.As(err, &inv):
		return httpx.BadRequest(inv.Code, "%s", inv.Msg)
	case errors.Is(err, ErrUnknownBackup):
		return httpx.NotFound("unknown_backup", "%s", err)
	case errors.Is(err, ErrUnknownDestination):
		return httpx.NotFound("unknown_destination", "%s", err)
	case errors.Is(err, ErrUnknownRun):
		return httpx.NotFound("unknown_run", "%s", err)
	case errors.Is(err, ErrAlreadyRunning):
		return httpx.Conflict("already_running", "%s", err)
	case errors.Is(err, ErrNotReady):
		return httpx.Conflict("not_ready", "%s", err)
	case errors.Is(err, ErrNameTaken):
		return httpx.Conflict("name_taken", "%s", err)
	case errors.Is(err, ErrDestinationInUse):
		return httpx.Conflict("destination_in_use", "%s", err)
	case errors.Is(err, ErrConfirm):
		return httpx.BadRequest("confirm_mismatch", "%s", err)
	}
	return err
}

func (h *HTTP) listBackups(ctx context.Context, _ *struct{}) (*listOut, error) {
	list, err := h.svc.List(ctx)
	if err != nil {
		return nil, problem(err)
	}
	return &listOut{Body: list}, nil
}

func (h *HTTP) getBackup(ctx context.Context, in *idIn) (*backupOut, error) {
	b, err := h.svc.Get(ctx, in.Id)
	if err != nil {
		return nil, problem(err)
	}
	return &backupOut{Body: b}, nil
}

func (h *HTTP) createBackup(ctx context.Context, in *createIn) (*backupOut, error) {
	b, err := h.svc.Create(ctx, in.Body)
	if err != nil {
		return nil, problem(err)
	}
	return &backupOut{Body: b}, nil
}

func (h *HTTP) updateBackup(ctx context.Context, in *updateIn) (*backupOut, error) {
	b, err := h.svc.Update(ctx, in.Id, in.Body)
	if err != nil {
		return nil, problem(err)
	}
	return &backupOut{Body: b}, nil
}

func (h *HTTP) deleteBackup(ctx context.Context, in *idIn) (*struct{}, error) {
	return nil, problem(h.svc.Delete(ctx, in.Id))
}

func (h *HTTP) runBackup(ctx context.Context, in *idIn) (*startedOut, error) {
	id, err := h.svc.Trigger(ctx, in.Id, TriggerManual)
	if err != nil {
		return nil, problem(err)
	}
	return &startedOut{Body: BackupRunStarted{RunId: id}}, nil
}

func (h *HTTP) getBackupLogs(ctx context.Context, in *idIn) (*logsOut, error) {
	logs, err := h.svc.Logs(ctx, in.Id)
	if err != nil {
		return nil, problem(err)
	}
	return &logsOut{Body: Logs{Logs: logs}}, nil
}

func (h *HTTP) listBackupRuns(ctx context.Context, in *runsIn) (*runsOut, error) {
	runs, err := h.svc.Runs(ctx, in.Id, in.Limit)
	if err != nil {
		return nil, problem(err)
	}
	return &runsOut{Body: runs}, nil
}

func (h *HTTP) getBackupRun(ctx context.Context, in *runIn) (*runOut, error) {
	d, err := h.svc.RunDetail(ctx, in.Run)
	if err != nil {
		return nil, problem(err)
	}
	return &runOut{Body: d}, nil
}

func (h *HTTP) getBackupCoverage(ctx context.Context, _ *struct{}) (*coverageOut, error) {
	c, err := h.svc.Coverage(ctx)
	if err != nil {
		return nil, problem(err)
	}
	return &coverageOut{Body: c}, nil
}

func (h *HTTP) listDestinations(ctx context.Context, _ *struct{}) (*destsOut, error) {
	list, err := h.svc.Destinations(ctx)
	if err != nil {
		return nil, problem(err)
	}
	return &destsOut{Body: list}, nil
}

func (h *HTTP) createDestination(ctx context.Context, in *destCreateIn) (*destCreatedOut, error) {
	d, err := h.svc.CreateDestination(ctx, in.Body)
	if err != nil {
		return nil, problem(err)
	}
	return &destCreatedOut{Body: d}, nil
}

func (h *HTTP) updateDestination(ctx context.Context, in *destUpdateIn) (*destOut, error) {
	d, err := h.svc.UpdateDestination(ctx, in.Id, in.Body)
	if err != nil {
		return nil, problem(err)
	}
	return &destOut{Body: d}, nil
}

func (h *HTTP) deleteDestination(ctx context.Context, in *idIn) (*struct{}, error) {
	return nil, problem(h.svc.DeleteDestination(ctx, in.Id))
}

func (h *HTTP) duplicateDestination(ctx context.Context, in *idIn) (*destOut, error) {
	d, err := h.svc.DuplicateDestination(ctx, in.Id)
	if err != nil {
		return nil, problem(err)
	}
	return &destOut{Body: d}, nil
}

func (h *HTTP) testDestination(ctx context.Context, in *idIn) (*destTestOut, error) {
	t, err := h.svc.TestDestination(ctx, in.Id)
	if err != nil {
		return nil, problem(err)
	}
	return &destTestOut{Body: t}, nil
}

func (h *HTTP) confirmPassword(ctx context.Context, in *idIn) (*destOut, error) {
	d, err := h.svc.ConfirmPassword(ctx, in.Id)
	if err != nil {
		return nil, problem(err)
	}
	return &destOut{Body: d}, nil
}

func (h *HTTP) revealPassword(ctx context.Context, in *revealIn) (*passwordOut, error) {
	pw, err := h.svc.RevealPassword(ctx, in.Id, in.Body.Confirm)
	if err != nil {
		return nil, problem(err)
	}
	return &passwordOut{Body: DestinationPassword{Password: pw}}, nil
}
