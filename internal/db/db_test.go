package db

import (
	"path/filepath"
	"testing"
)

// TestPasteSummaryBackfill exercises the summary/first_image migration path:
// rows written before those columns existed have empty summaries and must be
// discoverable + backfillable, and the summary query must not return content.
func TestPasteSummaryBackfill(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	// Simulate two pre-migration rows by inserting without summary/first_image.
	if _, err := d.conn.Exec(`INSERT INTO pastes (name, content, format) VALUES ('a', 'hello world body', 'text')`); err != nil {
		t.Fatalf("insert a: %v", err)
	}
	if _, err := d.conn.Exec(`INSERT INTO pastes (name, content, format) VALUES ('b', '', 'text')`); err != nil {
		t.Fatalf("insert b: %v", err)
	}

	missing, err := d.PasteSummariesMissing()
	if err != nil {
		t.Fatalf("missing: %v", err)
	}
	// Only the non-empty paste qualifies (query filters content != '').
	if len(missing) != 1 {
		t.Fatalf("expected 1 missing paste, got %d", len(missing))
	}
	if missing[0].Content != "hello world body" {
		t.Fatalf("expected missing content for 'a', got %q", missing[0].Content)
	}
	if err := d.SetPasteSummaryAndImage(missing[0].ID, "hello world body", ""); err != nil {
		t.Fatalf("set summary: %v", err)
	}

	if m, _ := d.PasteSummariesMissing(); len(m) != 0 {
		t.Fatalf("expected 0 missing after backfill, got %d", len(m))
	}

	// ListPasteSummaries must return the summary and NOT the content.
	got, err := d.ListPasteSummaries(10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var summary, content string
	for _, p := range got {
		if p.Name == "a" {
			summary, content = p.Summary, p.Content
		}
	}
	if summary != "hello world body" {
		t.Fatalf("expected stored summary, got %q", summary)
	}
	if content != "" {
		t.Fatalf("ListPasteSummaries leaked content %q", content)
	}
}

func TestListRecentByCreatorCapsResults(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	for i := range 5 {
		if _, err := d.Create("slug-"+string(rune('a'+i)), "https://x.example", "anonymous"); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	links, err := d.ListRecentByCreator("anonymous", 3)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(links) != 3 {
		t.Fatalf("expected cap of 3, got %d", len(links))
	}
	// Newest first (highest id first).
	if links[0].Slug != "slug-e" {
		t.Fatalf("expected newest first, got %q", links[0].Slug)
	}
}
