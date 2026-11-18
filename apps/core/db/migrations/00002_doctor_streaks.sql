-- +goose Up
-- Consecutive-failure counts per doctor check, so the daily audit alerts only
-- after two failures in a row. An absent row means a streak of zero.
CREATE TABLE doctor_streaks (
    check_name TEXT PRIMARY KEY,
    fails      INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE doctor_streaks;
