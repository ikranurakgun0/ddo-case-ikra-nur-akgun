-- Simülatörün (seed) durumu: tek satır. Doğrulama komutu seed bitene kadar bekler.
CREATE TABLE seed_state (
    id          int PRIMARY KEY CHECK (id = 1),
    seed        bigint NOT NULL,
    status      text   NOT NULL CHECK (status IN ('running','done')),
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    fingerprint text,
    stats       jsonb
);
