CREATE TABLE media (
  id TEXT PRIMARY KEY,
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

CREATE VIRTUAL TABLE media_fts USING fts5(
  ocr_text, tags, path UNINDEXED, content='media', content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS media_ai AFTER INSERT ON media BEGIN
  INSERT INTO media_fts(rowid, ocr_text, tags, path)
  VALUES (new.rowid, new.ocr_text, new.tags, new.path);
END;

CREATE TRIGGER IF NOT EXISTS media_au AFTER UPDATE ON media BEGIN
  UPDATE media_fts SET ocr_text=new.ocr_text, tags=new.tags, path=new.path
  WHERE rowid=new.rowid;
END;

CREATE TRIGGER IF NOT EXISTS media_ad AFTER DELETE ON media BEGIN
  DELETE FROM media_fts WHERE rowid=old.rowid;
END;
