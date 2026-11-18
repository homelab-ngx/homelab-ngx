-- +goose Up
-- #87: a version 7 stack is built with a nixpkgs commit the daemon picks.
-- stacks.nixpkgs_rev is the commit the approved manifest's nixpkgs resolves
-- to now (a channel moves); a generation records the one it was built with.
-- Empty for a stack whose approved manifest is older than version 7.
ALTER TABLE stacks ADD COLUMN nixpkgs_rev TEXT NOT NULL DEFAULT '';
ALTER TABLE stack_generations ADD COLUMN nixpkgs_rev TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE stack_generations DROP COLUMN nixpkgs_rev;
ALTER TABLE stacks DROP COLUMN nixpkgs_rev;
