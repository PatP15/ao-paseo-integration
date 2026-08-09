-- +goose Up
CREATE UNIQUE INDEX idx_ec_session_sequence
    ON execution_commands(session_id, sequence);

-- +goose Down
DROP INDEX idx_ec_session_sequence;
