-- Owldrop telemetry events (site worker). One row per anonymous event.
CREATE TABLE IF NOT EXISTS events (
  ts INTEGER NOT NULL,          -- unix seconds
  install_id TEXT NOT NULL,     -- anonymous install id ('site' for downloads)
  name TEXT NOT NULL,           -- heartbeat | file_received | ... | download
  platform TEXT NOT NULL DEFAULT '',  -- windows | mac | linux | nix | ''
  version TEXT NOT NULL DEFAULT ''    -- app version ('site' downloads: '')
);

CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);
CREATE INDEX IF NOT EXISTS idx_events_name ON events(name);
CREATE INDEX IF NOT EXISTS idx_events_install ON events(install_id);
