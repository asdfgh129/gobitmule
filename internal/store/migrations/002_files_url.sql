-- Schema v2 (docs/06 §4.4 separated-stream model, ADR-0027): per-file source
-- URL so a multi-file task (video + audio streams) can address each stream.
-- Idempotent: safe to re-run on restart.
ALTER TABLE files ADD COLUMN url TEXT NOT NULL DEFAULT '';
