-- +goose Up
-- What the runtime knows about a stack. `generation` is the generation of
-- the stack's system profile that is switched to (0: nothing built yet).
-- `want_running` is what the operator last asked for, so the daemon can start
-- the stack again after a reboot. `subnet` is the stack's private network,
-- allocated once. `secrets_key` is the public half of the stack's sops key.
ALTER TABLE stacks ADD COLUMN generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE stacks ADD COLUMN want_running INTEGER NOT NULL DEFAULT 0;
ALTER TABLE stacks ADD COLUMN subnet TEXT NOT NULL DEFAULT '';
ALTER TABLE stacks ADD COLUMN secrets_key TEXT NOT NULL DEFAULT '';

-- One row per generation of a stack's system profile, numbered as nix
-- numbers them. The manifest is the approved one the generation was built
-- under: it decides the binds, rules and routes while that generation runs.
-- `failed` marks a generation that was rolled back from as unhealthy.
CREATE TABLE stack_generations (
    stack    TEXT NOT NULL,
    id       INTEGER NOT NULL,
    rev      TEXT NOT NULL,
    url      TEXT NOT NULL,
    manifest TEXT NOT NULL,
    system   TEXT NOT NULL,
    built_at INTEGER NOT NULL,
    failed   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (stack, id)
);

-- +goose Down
DROP TABLE stack_generations;
ALTER TABLE stacks DROP COLUMN secrets_key;
ALTER TABLE stacks DROP COLUMN subnet;
ALTER TABLE stacks DROP COLUMN want_running;
ALTER TABLE stacks DROP COLUMN generation;
