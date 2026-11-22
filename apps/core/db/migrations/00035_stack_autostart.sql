-- +goose Up
-- `autostart`: the stack starts after the host boots. On the daemon's first
-- start in a boot, each stack's want_running is set to it. It starts as what
-- the next boot would have done: start the stacks left running.
ALTER TABLE stacks ADD COLUMN autostart INTEGER NOT NULL DEFAULT 1;
UPDATE stacks SET autostart = want_running;

-- +goose Down
ALTER TABLE stacks DROP COLUMN autostart;
