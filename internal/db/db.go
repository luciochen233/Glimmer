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
	ID        int64
	Name      string
	Title     string
	Content   string
	Format    string
	Token     string // empty = disabled
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Link struct {
	ID        int64
	Slug      string
	URL       string
	CreatedBy string
	Clicks    int64
	CreatedAt time.Time
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
	`)
	if err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	// Add columns if upgrading from older schema
	conn.Exec(`ALTER TABLE links ADD COLUMN created_by TEXT NOT NULL DEFAULT 'anonymous'`)
	conn.Exec(`ALTER TABLE links ADD COLUMN clicks INTEGER NOT NULL DEFAULT 0`)

	return nil
}

func (d *DB) Close() error {
	return d.conn.Close()
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

func (d *DB) CreatePaste(name, title, content, format, token string) (*Paste, error) {
	var tokenVal interface{}
	if token != "" {
		tokenVal = token
	}
	res, err := d.conn.Exec(
		"INSERT INTO pastes (name, title, content, format, token) VALUES (?, ?, ?, ?, ?)",
		name, title, content, format, tokenVal,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	now := time.Now()
	return &Paste{ID: id, Name: name, Title: title, Content: content, Format: format, Token: token, CreatedAt: now, UpdatedAt: now}, nil
}

func (d *DB) GetPasteByName(name string) (*Paste, error) {
	row := d.conn.QueryRow("SELECT id, name, title, content, format, COALESCE(token,''), created_at, updated_at FROM pastes WHERE name = ?", name)
	return scanPaste(row)
}

func (d *DB) GetPasteByID(id int64) (*Paste, error) {
	row := d.conn.QueryRow("SELECT id, name, title, content, format, COALESCE(token,''), created_at, updated_at FROM pastes WHERE id = ?", id)
	return scanPaste(row)
}

func (d *DB) ListPastes() ([]Paste, error) {
	rows, err := d.conn.Query("SELECT id, name, title, content, format, COALESCE(token,''), created_at, updated_at FROM pastes ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pastes []Paste
	for rows.Next() {
		var p Paste
		var created, updated string
		if err := rows.Scan(&p.ID, &p.Name, &p.Title, &p.Content, &p.Format, &p.Token, &created, &updated); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		p.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		pastes = append(pastes, p)
	}
	return pastes, rows.Err()
}

func (d *DB) UpdatePaste(id int64, name, title, content, format, token string) error {
	var tokenVal interface{}
	if token != "" {
		tokenVal = token
	}
	_, err := d.conn.Exec(
		"UPDATE pastes SET name=?, title=?, content=?, format=?, token=?, updated_at=datetime('now') WHERE id=?",
		name, title, content, format, tokenVal, id,
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

func scanPaste(row *sql.Row) (*Paste, error) {
	var p Paste
	var created, updated string
	if err := row.Scan(&p.ID, &p.Name, &p.Title, &p.Content, &p.Format, &p.Token, &created, &updated); err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	p.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	return &p, nil
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
