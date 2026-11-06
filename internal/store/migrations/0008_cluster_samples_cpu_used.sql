-- Real CPU usage (sum of every node agent's cpu_used_cores, as a % of
-- cluster capacity) for the dashboard, replacing the reservation-based
-- cpu_reserved_pct there. Rows sampled before this migration read as 0.
ALTER TABLE cluster_samples ADD COLUMN cpu_used_pct REAL NOT NULL DEFAULT 0;
