package session

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Cross-session content search is backed by an FTS5 index so that a query does
// not have to re-read every transcript (~2.5 GB) on each keystroke.
//
// Design decisions worth knowing before changing anything here:
//
//   - tokenize='trigram' — the TUI's search has always been substring-based
//     (strings.Contains), and transcripts are heavily Korean. The default
//     unicode61 tokenizer splits on whitespace/punctuation, so it misses both
//     mid-word matches ("orktre") and Korean ("워크트리" inside "워크트리를").
//     Trigram reproduces the existing semantics; unicode61 measurably does not
//     (1930 vs 10471 hits for "worktree" on a 10% corpus sample).
//
//   - content='' (contentless) — the block text also lives in the transcript on
//     disk, so storing a second copy inside FTS is pure overhead. Contentless
//     cut the index from 410 MB to 284 MB on the sample. Snippets are rebuilt by
//     re-reading the one matching line via its byte offset.
//
//   - detail=full — required, not chosen. Trigram matching is internally a
//     phrase query, and FTS5 rejects phrase queries unless detail=full. That
//     rules out the smaller detail=none layout.
//
//   - contentless_delete=1 — lets us delete a file's rows without knowing their
//     original text, which is what makes incremental reindexing possible.
//
//   - tool_result blocks are not indexed. They are half of all transcript text
//     but are mostly file dumps and command output; excluding them takes the
//     full-corpus index from ~850 MB to ~450 MB.

const (
	// indexSchemaVersion is bumped whenever the schema or the set of indexed
	// blocks changes in a way that makes an existing index wrong rather than
	// merely stale. On mismatch the index is dropped and rebuilt.
	indexSchemaVersion = 1

	// minTrigramTerm is the shortest term a trigram index can match. Queries
	// containing anything shorter cannot be answered from the index at all.
	minTrigramTerm = 3
)

// Index is an FTS5 index over transcript content blocks.
type Index struct {
	db   *sql.DB
	path string
}

func indexFilePath(claudeDir string) string {
	return filepath.Join(claudeDir, ".ccx-index.db")
}

// OpenIndex opens (creating if needed) the content index for claudeDir.
// A schema-version mismatch or an unreadable file rebuilds from scratch rather
// than failing: the index is a cache, and a corrupt cache must not break search.
func OpenIndex(claudeDir string) (*Index, error) {
	path := indexFilePath(claudeDir)

	idx, err := openIndexAt(path)
	if err == nil {
		return idx, nil
	}

	// Unusable index — discard and retry once from empty.
	os.Remove(path)
	os.Remove(path + "-wal")
	os.Remove(path + "-shm")
	return openIndexAt(path)
}

func openIndexAt(path string) (*Index, error) {
	// synchronous=normal: losing the tail of the index after a crash costs a
	// reindex of a few sessions, which is cheaper than fsync per commit.
	dsn := path + "?_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	idx := &Index{db: db, path: path}
	if err := idx.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return idx, nil
}

func (ix *Index) migrate() error {
	var version int
	row := ix.db.QueryRow(`select value from meta where key='schema_version'`)
	if err := row.Scan(&version); err != nil {
		if !isMissingTable(err) && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		version = 0
	}

	if version == indexSchemaVersion {
		return nil
	}
	if version != 0 {
		// Stale layout: drop everything and fall through to a fresh create.
		for _, stmt := range []string{
			`drop table if exists blocks`,
			`drop table if exists locs`,
			`drop table if exists files`,
			`drop table if exists meta`,
		} {
			if _, err := ix.db.Exec(stmt); err != nil {
				return err
			}
		}
	}

	schema := []string{
		`create table if not exists meta(key text primary key, value text)`,

		// One row per indexed transcript file. mod_unix/size detect changes.
		`create table if not exists files(
			id        integer primary key,
			path      text unique not null,
			mod_unix  integer not null,
			size      integer not null
		)`,

		// Locations of indexed blocks. Shares rowids with the FTS table, which
		// is what lets us delete a file's blocks and filter by role/tool.
		`create table if not exists locs(
			rowid     integer primary key,
			file_id   integer not null,
			line_off  integer not null,
			block_idx integer not null,
			role      text not null,
			tool      text not null
		)`,
		`create index if not exists locs_file on locs(file_id)`,

		`create virtual table if not exists blocks using fts5(
			body,
			tokenize='trigram',
			content='',
			contentless_delete=1,
			detail=full
		)`,
	}
	for _, stmt := range schema {
		if _, err := ix.db.Exec(stmt); err != nil {
			return err
		}
	}

	_, err := ix.db.Exec(
		`insert into meta(key,value) values('schema_version',?)
		 on conflict(key) do update set value=excluded.value`,
		fmt.Sprint(indexSchemaVersion))
	return err
}

func isMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

func (ix *Index) Close() error {
	if ix == nil || ix.db == nil {
		return nil
	}
	return ix.db.Close()
}

// Path returns the on-disk location of the index.
func (ix *Index) Path() string { return ix.path }

// indexedFile is the stored fingerprint of an already-indexed transcript.
type indexedFile struct {
	id      int64
	modUnix int64
	size    int64
}

func (ix *Index) knownFiles() (map[string]indexedFile, error) {
	rows, err := ix.db.Query(`select id, path, mod_unix, size from files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	known := make(map[string]indexedFile)
	for rows.Next() {
		var f indexedFile
		var p string
		if err := rows.Scan(&f.id, &p, &f.modUnix, &f.size); err != nil {
			return nil, err
		}
		known[p] = f
	}
	return known, rows.Err()
}

// SyncStats reports what a Sync did, for progress reporting and tests.
type SyncStats struct {
	Scanned int           // transcripts considered
	Indexed int           // transcripts (re)indexed
	Removed int           // transcripts dropped from the index
	Blocks  int           // content blocks written
	Elapsed time.Duration //
}

// Sync brings the index in line with the given sessions: transcripts whose
// mtime or size changed are reindexed, transcripts that disappeared are
// dropped, and unchanged transcripts are skipped. It is safe to call on every
// search — the common case is a stat of each file and no writes.
func (ix *Index) Sync(ctx context.Context, sessions []*Session, progress func(done, total int)) (SyncStats, error) {
	start := time.Now()
	var stats SyncStats

	known, err := ix.knownFiles()
	if err != nil {
		return stats, err
	}

	type work struct {
		path    string
		modUnix int64
		size    int64
		prevID  int64
		hasPrev bool
	}

	var todo []work
	seen := make(map[string]bool, len(sessions))

	for _, s := range sessions {
		if s == nil || s.FilePath == "" || seen[s.FilePath] {
			continue
		}
		seen[s.FilePath] = true
		stats.Scanned++

		fi, err := os.Stat(s.FilePath)
		if err != nil {
			continue
		}
		mod, size := fi.ModTime().Unix(), fi.Size()

		prev, ok := known[s.FilePath]
		if ok && prev.modUnix == mod && prev.size == size {
			continue // unchanged
		}
		todo = append(todo, work{
			path: s.FilePath, modUnix: mod, size: size,
			prevID: prev.id, hasPrev: ok,
		})
	}

	// Transcripts that vanished (project deleted, session pruned).
	for path, f := range known {
		if !seen[path] {
			if err := ix.dropFile(f.id); err != nil {
				return stats, err
			}
			stats.Removed++
		}
	}

	for i, w := range todo {
		select {
		case <-ctx.Done():
			stats.Elapsed = time.Since(start)
			return stats, ctx.Err()
		default:
		}

		if progress != nil {
			progress(i, len(todo))
		}

		n, err := ix.indexFile(w.path, w.modUnix, w.size, w.prevID, w.hasPrev)
		if err != nil {
			// A single unreadable transcript must not abort the whole sync.
			continue
		}
		stats.Indexed++
		stats.Blocks += n
	}
	if progress != nil && len(todo) > 0 {
		progress(len(todo), len(todo))
	}

	stats.Elapsed = time.Since(start)
	return stats, nil
}

func (ix *Index) dropFile(fileID int64) error {
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`delete from blocks where rowid in (select rowid from locs where file_id=?)`, fileID); err != nil {
		return err
	}
	if _, err := tx.Exec(`delete from locs where file_id=?`, fileID); err != nil {
		return err
	}
	if _, err := tx.Exec(`delete from files where id=?`, fileID); err != nil {
		return err
	}
	return tx.Commit()
}

// indexFile reindexes one transcript, replacing any previously indexed rows.
func (ix *Index) indexFile(path string, modUnix, size, prevID int64, hasPrev bool) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	tx, err := ix.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	fileID := prevID
	if hasPrev {
		if _, err := tx.Exec(
			`delete from blocks where rowid in (select rowid from locs where file_id=?)`, fileID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`delete from locs where file_id=?`, fileID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`update files set mod_unix=?, size=? where id=?`, modUnix, size, fileID); err != nil {
			return 0, err
		}
	} else {
		res, err := tx.Exec(`insert into files(path, mod_unix, size) values (?,?,?)`, path, modUnix, size)
		if err != nil {
			return 0, err
		}
		if fileID, err = res.LastInsertId(); err != nil {
			return 0, err
		}
	}

	insBlock, err := tx.Prepare(`insert into blocks(rowid, body) values (?,?)`)
	if err != nil {
		return 0, err
	}
	insLoc, err := tx.Prepare(
		`insert into locs(rowid, file_id, line_off, block_idx, role, tool) values (?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256*1024), 10*1024*1024)

	var lineOff int64
	var count int

	for sc.Scan() {
		line := sc.Bytes()
		off := lineOff
		lineOff += int64(len(line)) + 1 // +1 for the newline the scanner strips

		if len(line) == 0 {
			continue
		}
		entry, err := ParseEntry(string(line))
		if err != nil || entry.IsMeta {
			continue
		}

		for i := range entry.Content {
			block := &entry.Content[i]
			if !indexableBlock(block) {
				continue
			}
			body := blockSearchText(block)
			if body == "" {
				continue
			}

			// rowid is assigned by SQLite; both tables must agree on it, so we
			// take the one FTS picked and reuse it for the location row.
			res, err := insBlock.Exec(nil, body)
			if err != nil {
				return 0, err
			}
			rowid, err := res.LastInsertId()
			if err != nil {
				return 0, err
			}
			if _, err := insLoc.Exec(rowid, fileID, off, i, entry.Role, block.ToolName); err != nil {
				return 0, err
			}
			count++
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// indexableBlock reports whether a block's text goes into the index.
//
// tool_result is the one deliberate omission: it is roughly half of all
// transcript text but is dominated by file dumps and command output, and
// indexing it nearly doubles the index for little search value. Queries that
// need it fall back to the full scan (see IndexCoverage).
//
// Everything else the scan can match must be listed here, or the index silently
// returns fewer results than the scan for the same query. system_tag in
// particular carries command names and system reminders that users do search
// for, at ~0.6% of the corpus.
func indexableBlock(b *ContentBlock) bool {
	// Empty bodies are skipped by the caller, so image blocks and other
	// text-free types drop out on their own.
	return b.Type != "tool_result"
}

// Stats describes the current index contents.
type Stats struct {
	Files  int
	Blocks int
	Bytes  int64
}

func (ix *Index) Stats() (Stats, error) {
	var s Stats
	if err := ix.db.QueryRow(`select count(*) from files`).Scan(&s.Files); err != nil {
		return s, err
	}
	if err := ix.db.QueryRow(`select count(*) from locs`).Scan(&s.Blocks); err != nil {
		return s, err
	}
	if fi, err := os.Stat(ix.path); err == nil {
		s.Bytes = fi.Size()
	}
	return s, nil
}

// Optimize compacts the FTS index. Worth running after a large rebuild; not
// needed on the incremental path.
func (ix *Index) Optimize() error {
	_, err := ix.db.Exec(`insert into blocks(blocks) values('optimize')`)
	return err
}

// --- querying ---------------------------------------------------------------

// IndexCoverage says whether the index can answer a query on its own.
type IndexCoverage int

const (
	// CoverageFull means index results are equivalent to a full scan.
	CoverageFull IndexCoverage = iota
	// CoverageNone means the query must use the full scan; the index cannot
	// answer it (a term shorter than a trigram, or an empty query).
	CoverageNone
	// CoveragePartial means the index answers the query but over a subset of
	// blocks: tool_result content is not indexed, so a full scan would find
	// strictly more.
	CoveragePartial
)

// Coverage reports how well the index can serve q.
func (ix *Index) Coverage(q SearchQuery) IndexCoverage {
	if q.IsEmpty() {
		return CoverageNone
	}
	// Trigram cannot match anything shorter than three characters.
	for _, t := range q.Terms {
		if len([]rune(t)) < minTrigramTerm {
			return CoverageNone
		}
	}
	for _, p := range q.Phrases {
		if len([]rune(p)) < minTrigramTerm {
			return CoverageNone
		}
	}
	// Exclusions are applied after retrieval, so a short one is harmless.

	// A tool: filter restricts to tool_use blocks, which are fully indexed.
	if q.ToolName != "" {
		return CoverageFull
	}
	return CoveragePartial
}

// fts5Quote renders s as an FTS5 string literal. Trigram matching treats the
// content literally, so every term becomes a quoted phrase; the only character
// needing care is the double quote, which doubles.
func fts5Quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// buildMatchExpr renders the positive part of a query as an FTS5 MATCH
// expression. Exclusions are intentionally left out: they are substring
// exclusions over the block text, which is cheaper and more faithful to apply
// after the rows come back.
func buildMatchExpr(q SearchQuery) string {
	var parts []string
	for _, t := range q.Terms {
		parts = append(parts, fts5Quote(t))
	}
	for _, p := range q.Phrases {
		parts = append(parts, fts5Quote(p))
	}
	if q.ToolName != "" && !strings.HasSuffix(q.ToolName, "*") {
		// A concrete tool name is also indexed in the block body (tool_use
		// bodies are "name input"), so it usefully narrows the FTS scan. A
		// prefix filter is left to the post-filter.
		parts = append(parts, fts5Quote(q.ToolName))
	}
	return strings.Join(parts, " AND ")
}

// indexHit is one row from the index: where the block lives, nothing more.
type indexHit struct {
	path     string
	lineOff  int64
	blockIdx int
}

// queryIndex returns matching block locations, newest session first.
func (ix *Index) queryIndex(ctx context.Context, q SearchQuery, allowed map[string]*Session, limit int) ([]indexHit, error) {
	expr := buildMatchExpr(q)
	if expr == "" {
		return nil, nil
	}

	var (
		where []string
		args  []any
	)
	args = append(args, expr)

	if q.Role != "" {
		where = append(where, `locs.role = ?`)
		args = append(args, q.Role)
	}
	if q.ToolName != "" {
		if strings.HasSuffix(q.ToolName, "*") {
			where = append(where, `lower(locs.tool) like ?`)
			args = append(args, strings.ToLower(strings.TrimSuffix(q.ToolName, "*"))+"%")
		} else {
			where = append(where, `lower(locs.tool) = ?`)
			args = append(args, strings.ToLower(q.ToolName))
		}
	}

	sqlText := `select files.path, locs.line_off, locs.block_idx
	            from blocks
	            join locs  on locs.rowid = blocks.rowid
	            join files on files.id   = locs.file_id
	            where blocks match ?`
	if len(where) > 0 {
		sqlText += " and " + strings.Join(where, " and ")
	}
	// Ordering is applied in Go against session mtime; SQL only bounds the work.
	if limit > 0 {
		sqlText += fmt.Sprintf(" limit %d", limit)
	}

	rows, err := ix.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []indexHit
	for rows.Next() {
		var h indexHit
		if err := rows.Scan(&h.path, &h.lineOff, &h.blockIdx); err != nil {
			return nil, err
		}
		if allowed != nil {
			if _, ok := allowed[h.path]; !ok {
				continue
			}
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Newest session first, matching the session browser's ordering; within a
	// session, transcript order.
	sort.SliceStable(hits, func(i, j int) bool {
		si, sj := allowed[hits[i].path], allowed[hits[j].path]
		if si != nil && sj != nil && !si.ModTime.Equal(sj.ModTime) {
			return si.ModTime.After(sj.ModTime)
		}
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].lineOff < hits[j].lineOff
	})
	return hits, nil
}
