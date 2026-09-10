-- Additive: older agent rows retain the legacy schema and no v2 selection.
ALTER TABLE agents ADD COLUMN agent_schema TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN skill_config_json TEXT NOT NULL DEFAULT 'null';
ALTER TABLE agents ADD COLUMN skill_lock_json TEXT NOT NULL DEFAULT 'null';
CREATE TABLE skill_defaults (id INTEGER PRIMARY KEY CHECK(id=1), config_json TEXT NOT NULL, lock_json TEXT NOT NULL);
