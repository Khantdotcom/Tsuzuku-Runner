-- +goose Up
ALTER TABLE verification_checks
    DROP CONSTRAINT verification_checks_kind_check;
ALTER TABLE verification_checks
    ADD CONSTRAINT verification_checks_kind_check
    CHECK (kind IN ('exit_code', 'command', 'acceptance', 'integrity'));

-- +goose Down
ALTER TABLE verification_checks
    DROP CONSTRAINT verification_checks_kind_check;
ALTER TABLE verification_checks
    ADD CONSTRAINT verification_checks_kind_check
    CHECK (kind IN ('exit_code', 'command'));
