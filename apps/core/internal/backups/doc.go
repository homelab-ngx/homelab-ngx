// Package backups owns the backups: named sets of sources copied to one or
// more destinations on a schedule, all stored in SQLite and edited through
// the API. The daemon is the control plane only. It schedules a run (robfig
// cron), starts the stack prepare units and the database snapshot, and
// launches the copy as homelab-backup-runner in a sandboxed transient
// systemd unit. The runner reports through a state file (package runfile)
// that the daemon reads for live progress and imports into backup_runs and
// backup_run_items when the unit ends, so a run survives a daemon restart.
package backups
