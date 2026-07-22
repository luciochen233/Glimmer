package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Paste struct {
	ID         int64
	Name       string
	Title      string
	Content    string
	Summary    string
	FirstImage string
	Format     string
	Token      string // empty = disabled
	Hidden     bool   // return 404 instead of token prompt
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Link struct {
	ID        int64
	Slug      string
	URL       string
	CreatedBy string
	Clicks    int64
	CreatedAt time.Time
}

type Upload struct {
	Filename     string
	OriginalName string
	CreatedAt    time.Time
}

type DB struct {
	conn *sql.DB
}

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	conn.SetMaxOpenConns(1)

	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, err
	}

	return &DB{conn: conn}, nil
}

func migrate(conn *sql.DB) error {
	_, err := conn.Exec(`
		CREATE TABLE IF NOT EXISTS links (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			slug       TEXT NOT NULL UNIQUE,
			url        TEXT NOT NULL,
			created_by TEXT NOT NULL DEFAULT 'anonymous',
			clicks     INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_links_slug ON links(slug);
		CREATE TABLE IF NOT EXISTS pastes (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT NOT NULL UNIQUE,
			title      TEXT NOT NULL DEFAULT '',
			content    TEXT NOT NULL DEFAULT '',
			format     TEXT NOT NULL DEFAULT 'markdown',
			token      TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS uploads (
			filename      TEXT PRIMARY KEY,
			original_name TEXT NOT NULL DEFAULT '',
			created_at    TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS sessions (
			token      TEXT PRIMARY KEY,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
	`)
	if err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	// Add columns if upgrading from older schema
	conn.Exec(`ALTER TABLE links ADD COLUMN created_by TEXT NOT NULL DEFAULT 'anonymous'`)
	conn.Exec(`ALTER TABLE links ADD COLUMN clicks INTEGER NOT NULL DEFAULT 0`)
	conn.Exec(`ALTER TABLE pastes ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0`)
	// summary + first_image let the admin paste grid render without loading
	// paste content (a real memory cliff on small hardware).
	conn.Exec(`ALTER TABLE pastes ADD COLUMN summary TEXT NOT NULL DEFAULT ''`)
	conn.Exec(`ALTER TABLE pastes ADD COLUMN first_image TEXT NOT NULL DEFAULT ''`)
	// width + height cache decoded image dimensions so the uploads admin page
	// does not have to re-decode every image header on each load.
	conn.Exec(`ALTER TABLE uploads ADD COLUMN width INTEGER NOT NULL DEFAULT 0`)
	conn.Exec(`ALTER TABLE uploads ADD COLUMN height INTEGER NOT NULL DEFAULT 0`)

	return nil
}

func (d *DB) Close() error {
	return d.conn.Close()
}

// Ping verifies the database connection is usable. Used by the health endpoint.
func (d *DB) Ping() error {
	var one int
	return d.conn.QueryRow("SELECT 1").Scan(&one)
}

func (d *DB) Create(slug, url, createdBy string) (*Link, error) {
	res, err := d.conn.Exec("INSERT INTO links (slug, url, created_by) VALUES (?, ?, ?)", slug, url, createdBy)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Link{ID: id, Slug: slug, URL: url, CreatedBy: createdBy, CreatedAt: time.Now()}, nil
}

func (d *DB) GetBySlug(slug string) (*Link, error) {
	row := d.conn.QueryRow("SELECT id, slug, url, created_by, clicks, created_at FROM links WHERE slug = ?", slug)
	return scanLink(row)
}

func (d *DB) GetByID(id int64) (*Link, error) {
	row := d.conn.QueryRow("SELECT id, slug, url, created_by, clicks, created_at FROM links WHERE id = ?", id)
	return scanLink(row)
}

func (d *DB) IncrementClicks(slug string) error {
	_, err := d.conn.Exec("UPDATE links SET clicks = clicks + 1 WHERE slug = ?", slug)
	return err
}

func (d *DB) TopLinks(createdBy string, limit int) ([]Link, error) {
	rows, err := d.conn.Query(
		"SELECT id, slug, url, created_by, clicks, created_at FROM links WHERE created_by = ? ORDER BY clicks DESC LIMIT ?",
		createdBy, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinks(rows)
}

func (d *DB) List() ([]Link, error) {
	rows, err := d.conn.Query("SELECT id, slug, url, created_by, clicks, created_at FROM links ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinks(rows)
}

func (d *DB) ListByCreator(createdBy string) ([]Link, error) {
	rows, err := d.conn.Query("SELECT id, slug, url, created_by, clicks, created_at FROM links WHERE created_by = ? ORDER BY id DESC", createdBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinks(rows)
}

// ListRecentByCreator returns the most recent links by creator, capped at
// limit. Used by the admin dashboard to bound memory without losing the
// latest activity.
func (d *DB) ListRecentByCreator(createdBy string, limit int) ([]Link, error) {
	rows, err := d.conn.Query("SELECT id, slug, url, created_by, clicks, created_at FROM links WHERE created_by = ? ORDER BY id DESC LIMIT ?", createdBy, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinks(rows)
}

func (d *DB) Update(id int64, slug, url string) error {
	_, err := d.conn.Exec("UPDATE links SET slug = ?, url = ? WHERE id = ?", slug, url, id)
	return err
}

func (d *DB) Delete(id int64) error {
	_, err := d.conn.Exec("DELETE FROM links WHERE id = ?", id)
	return err
}

func (d *DB) GetByURL(url string) (*Link, error) {
	row := d.conn.QueryRow("SELECT id, slug, url, created_by, clicks, created_at FROM links WHERE url = ? LIMIT 1", url)
	return scanLink(row)
}

func (d *DB) SlugExists(slug string) (bool, error) {
	var count int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM links WHERE slug = ?", slug).Scan(&count)
	return count > 0, err
}

func scanLinks(rows *sql.Rows) ([]Link, error) {
	var links []Link
	for rows.Next() {
		var l Link
		var ts string
		if err := rows.Scan(&l.ID, &l.Slug, &l.URL, &l.CreatedBy, &l.Clicks, &ts); err != nil {
			return nil, err
		}
		l.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
		links = append(links, l)
	}
	return links, rows.Err()
}

// ---- Paste CRUD ----

func (d *DB) CreatePaste(name, title, content, summary, firstImage, format, token string, hidden bool) (*Paste, error) {
	var tokenVal interface{}
	if token != "" {
		tokenVal = token
	}
	hiddenVal := 0
	if hidden {
		hiddenVal = 1
	}
	res, err := d.conn.Exec(
		"INSERT INTO pastes (name, title, content, summary, first_image, format, token, hidden) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		name, title, content, summary, firstImage, format, tokenVal, hiddenVal,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	now := time.Now()
	return &Paste{ID: id, Name: name, Title: title, Content: content, Summary: summary, FirstImage: firstImage, Format: format, Token: token, Hidden: hidden, CreatedAt: now, UpdatedAt: now}, nil
}

func (d *DB) GetPasteByName(name string) (*Paste, error) {
	row := d.conn.QueryRow("SELECT id, name, title, content, format, COALESCE(token,''), hidden, created_at, updated_at FROM pastes WHERE name = ? COLLATE NOCASE", name)
	return scanPaste(row)
}

func (d *DB) GetPasteByID(id int64) (*Paste, error) {
	row := d.conn.QueryRow("SELECT id, name, title, content, format, COALESCE(token,''), hidden, created_at, updated_at FROM pastes WHERE id = ?", id)
	return scanPaste(row)
}

func (d *DB) ListPastes() ([]Paste, error) {
	rows, err := d.conn.Query("SELECT id, name, title, content, format, COALESCE(token,''), hidden, created_at, updated_at FROM pastes ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pastes []Paste
	for rows.Next() {
		var p Paste
		var hidden int
		var created, updated string
		if err := rows.Scan(&p.ID, &p.Name, &p.Title, &p.Content, &p.Format, &p.Token, &hidden, &created, &updated); err != nil {
			return nil, err
		}
		p.Hidden = hidden != 0
		p.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		p.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		pastes = append(pastes, p)
	}
	return pastes, rows.Err()
}

func (d *DB) UpdatePaste(id int64, name, title, content, summary, firstImage, format, token string, hidden bool) error {
	var tokenVal interface{}
	if token != "" {
		tokenVal = token
	}
	hiddenVal := 0
	if hidden {
		hiddenVal = 1
	}
	_, err := d.conn.Exec(
		"UPDATE pastes SET name=?, title=?, content=?, summary=?, first_image=?, format=?, token=?, hidden=?, updated_at=datetime('now') WHERE id=?",
		name, title, content, summary, firstImage, format, tokenVal, hiddenVal, id,
	)
	return err
}

func (d *DB) DeletePaste(id int64) error {
	_, err := d.conn.Exec("DELETE FROM pastes WHERE id = ?", id)
	return err
}

func (d *DB) PasteNameExists(name string) (bool, error) {
	var count int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM pastes WHERE name = ?", name).Scan(&count)
	return count > 0, err
}

// ListPasteSummaries returns paste rows WITHOUT content — summary and
// first_image stand in for it on the grid. Omitting content is what keeps the
// admin paste page cheap to load. Ordered by most-recently-updated, paginated.
func (d *DB) ListPasteSummaries(limit, offset int) ([]Paste, error) {
	rows, err := d.conn.Query(
		"SELECT id, name, title, COALESCE(summary,''), COALESCE(first_image,''), format, COALESCE(token,''), hidden, created_at, updated_at FROM pastes ORDER BY updated_at DESC LIMIT ? OFFSET ?",
		limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pastes []Paste
	for rows.Next() {
		var p Paste
		var hidden int
		var created, updated string
		if err := rows.Scan(&p.ID, &p.Name, &p.Title, &p.Summary, &p.FirstImage, &p.Format, &p.Token, &hidden, &created, &updated); err != nil {
			return nil, err
		}
		p.Hidden = hidden != 0
		p.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		p.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		pastes = append(pastes, p)
	}
	return pastes, rows.Err()
}

// PasteSummariesMissing returns id+content for pastes that predate the
// summary/first_image columns, for one-time backfill on startup.
func (d *DB) PasteSummariesMissing() ([]Paste, error) {
	rows, err := d.conn.Query("SELECT id, content FROM pastes WHERE summary = '' AND content != ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Paste
	for rows.Next() {
		var p Paste
		if err := rows.Scan(&p.ID, &p.Content); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPasteSummaryAndImage fills the summary + first_image columns for a paste.
func (d *DB) SetPasteSummaryAndImage(id int64, summary, firstImage string) error {
	_, err := d.conn.Exec("UPDATE pastes SET summary = ?, first_image = ? WHERE id = ?", summary, firstImage, id)
	return err
}

func scanPaste(row *sql.Row) (*Paste, error) {
	var p Paste
	var hidden int
	var created, updated string
	if err := row.Scan(&p.ID, &p.Name, &p.Title, &p.Content, &p.Format, &p.Token, &hidden, &created, &updated); err != nil {
		return nil, err
	}
	p.Hidden = hidden != 0
	p.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	p.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	return &p, nil
}

// ---- Sessions (persisted so logins survive a server restart) ----

// CreateSession stores a session token with an absolute expiry. Times are
// stored in UTC to match SQLite's datetime('now'), which is also UTC.
func (d *DB) CreateSession(token string, expiresAt time.Time) error {
	_, err := d.conn.Exec(
		"INSERT OR REPLACE INTO sessions (token, expires_at) VALUES (?, ?)",
		token, expiresAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// SessionValid reports whether a token exists and has not expired. The expiry
// comparison happens in SQL against datetime('now') (UTC).
func (d *DB) SessionValid(token string) (bool, error) {
	var count int
	err := d.conn.QueryRow(
		"SELECT COUNT(*) FROM sessions WHERE token = ? AND expires_at > datetime('now')",
		token,
	).Scan(&count)
	return count > 0, err
}

func (d *DB) DeleteSession(token string) error {
	_, err := d.conn.Exec("DELETE FROM sessions WHERE token = ?", token)
	return err
}

// CleanupSessions removes expired sessions.
func (d *DB) CleanupSessions() error {
	_, err := d.conn.Exec("DELETE FROM sessions WHERE expires_at <= datetime('now')")
	return err
}

// ---- Upload metadata ----

func (d *DB) RecordUpload(filename, originalName string) error {
	_, err := d.conn.Exec(
		"INSERT OR REPLACE INTO uploads (filename, original_name) VALUES (?, ?)",
		filename, originalName,
	)
	return err
}

func (d *DB) GetUploadName(filename string) (string, error) {
	var name string
	err := d.conn.QueryRow("SELECT original_name FROM uploads WHERE filename = ?", filename).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return name, err
}

func (d *DB) ListUploadNames() (map[string]string, error) {
	rows, err := d.conn.Query("SELECT filename, original_name FROM uploads")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := make(map[string]string)
	for rows.Next() {
		var f, n string
		if err := rows.Scan(&f, &n); err != nil {
			return nil, err
		}
		names[f] = n
	}
	return names, rows.Err()
}

// UploadMeta bundles the cached metadata the uploads admin page needs for one
// file, so it can render without re-decoding image headers.
type UploadMeta struct {
	OriginalName string
	Width        int
	Height       int
}

// ListUploadMeta returns original name + cached image dimensions per filename.
func (d *DB) ListUploadMeta() (map[string]UploadMeta, error) {
	rows, err := d.conn.Query("SELECT filename, original_name, width, height FROM uploads")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]UploadMeta)
	for rows.Next() {
		var f, n string
		var w, h int
		if err := rows.Scan(&f, &n, &w, &h); err != nil {
			return nil, err
		}
		out[f] = UploadMeta{OriginalName: n, Width: w, Height: h}
	}
	return out, rows.Err()
}

// SetUploadDims caches decoded image dimensions for an upload.
func (d *DB) SetUploadDims(filename string, width, height int) error {
	_, err := d.conn.Exec("UPDATE uploads SET width = ?, height = ? WHERE filename = ?", width, height, filename)
	return err
}

func (d *DB) DeleteUpload(filename string) error {
	_, err := d.conn.Exec("DELETE FROM uploads WHERE filename = ?", filename)
	return err
}

func scanLink(row *sql.Row) (*Link, error) {
	var l Link
	var ts string
	if err := row.Scan(&l.ID, &l.Slug, &l.URL, &l.CreatedBy, &l.Clicks, &ts); err != nil {
		return nil, err
	}
	l.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
	return &l, nil
}
