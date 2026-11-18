-- +goose Up
-- #87: the nixpkgs the operator chose for a version 7 stack (a channel or a
-- commit). Empty: the one its manifest recommends, followed as it changes.
ALTER TABLE stacks ADD COLUMN nixpkgs TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE stacks DROP COLUMN nixpkgs;
