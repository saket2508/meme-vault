CREATE TABLE IF NOT EXISTS media (
  id INTEGER PRIMARY KEY,
  path TEXT NOT NULL,
  thumb TEXT,
  mime TEXT NOT NULL,
  width INT,
  height INT,
  size_bytes INT,
  tags TEXT,
  ocr_text TEXT,
  sha256 TEXT UNIQUE,
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE VIRTUAL TABLE IF NOT EXISTS media_fts USING fts4(
  id, ocr_text, tags, path
);


