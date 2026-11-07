package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type AccountState struct {
	LastUID     uint32
	UIDValidity uint32
}

// Pending is a notification whose delivery failed (fully or for some
// notifiers) and which awaits retry with exponential backoff.
type Pending struct {
	Account   string
	UID       uint32
	MessageID string
	From      string
	Subject   string
	Date      time.Time
	Preview   string
	Failed    []string // notifier names still undelivered

	// TitleOverride and TextOverride carry a parsed plugin's rendered output
	// across retries, so a redelivery keeps the plugin's wording instead of
	// falling back to the default template. Fields carries the structured
	// values a template may still want to reference.
	TitleOverride string
	TextOverride  string
	Fields        map[string]string

	Attempts int
	NextTry  time.Time
	Created  time.Time
}

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS account_state (
    account      TEXT    PRIMARY KEY,
    last_uid     INTEGER NOT NULL DEFAULT 0,
    uid_validity INTEGER NOT NULL DEFAULT 0,
    updated_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE TABLE IF NOT EXISTS seen_messages (
    message_id TEXT    NOT NULL,
    account    TEXT    NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY (message_id, account)
);
CREATE INDEX IF NOT EXISTS idx_seen_created ON seen_messages(created_at);
CREATE TABLE IF NOT EXISTS pending_notifications (
    account      TEXT    NOT NULL,
    uid          INTEGER NOT NULL,
    message_id   TEXT    NOT NULL DEFAULT '',
    sender       TEXT    NOT NULL DEFAULT '',
    subject      TEXT    NOT NULL DEFAULT '',
    date_unix    INTEGER NOT NULL DEFAULT 0,
    preview      TEXT    NOT NULL DEFAULT '',
    failed       TEXT    NOT NULL DEFAULT '',
    title_override TEXT  NOT NULL DEFAULT '',
    text_override  TEXT  NOT NULL DEFAULT '',
    fields         TEXT  NOT NULL DEFAULT '',
    attempts     INTEGER NOT NULL DEFAULT 0,
    next_try_unix INTEGER NOT NULL DEFAULT 0,
    created_unix INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY (account, uid)
);
`

func Load(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create state dir: %w", err)
		}
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	// Columns added after the first release. CREATE TABLE IF NOT EXISTS does
	// not touch an existing table, so databases created before these columns
	// existed need an explicit additive migration.
	if err := migratePendingColumns(db); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// pendingMigration adds one column to pending_notifications. Both the probe
// and the DDL are compile-time constants so no identifier ever reaches SQL
// as interpolated data.
type pendingMigration struct {
	column string
	ddl    string
}

var pendingMigrations = []pendingMigration{
	{"title_override", "ALTER TABLE pending_notifications ADD COLUMN title_override TEXT NOT NULL DEFAULT ''"},
	{"text_override", "ALTER TABLE pending_notifications ADD COLUMN text_override TEXT NOT NULL DEFAULT ''"},
	{"fields", "ALTER TABLE pending_notifications ADD COLUMN fields TEXT NOT NULL DEFAULT ''"},
}

// migratePendingColumns applies pendingMigrations to legacy databases. It is
// idempotent: a column that already exists is skipped.
func migratePendingColumns(db *sql.DB) error {
	existing, err := pendingColumns(db)
	if err != nil {
		return err
	}
	for _, m := range pendingMigrations {
		if existing[m.column] {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return fmt.Errorf("add column pending_notifications.%s: %w", m.column, err)
		}
	}
	return nil
}

// pendingColumns returns the set of column names currently on
// pending_notifications.
func pendingColumns(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(pending_notifications)")
	if err != nil {
		return nil, fmt.Errorf("inspect pending_notifications: %w", err)
	}
	defer rows.Close()

	cols := make(map[string]bool, 16)
	for rows.Next() {
		var (
			cid        int
			name, typ  string
			notNull    int
			dflt       sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &primaryKey); err != nil {
			return nil, fmt.Errorf("inspect pending_notifications: %w", err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect pending_notifications: %w", err)
	}
	return cols, nil
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Get returns the stored state for an account. found is false on first run
// (no row). A database error is propagated so callers must not mistake a
// transient failure for a fresh baseline (which would re-notify everything).
func (s *Store) Get(account string) (AccountState, bool, error) {
	var st AccountState
	row := s.db.QueryRow(
		`SELECT last_uid, uid_validity FROM account_state WHERE account = ?`,
		account,
	)
	if err := row.Scan(&st.LastUID, &st.UIDValidity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AccountState{}, false, nil
		}
		return AccountState{}, false, fmt.Errorf("get state %q: %w", account, err)
	}
	return st, true, nil
}

func (s *Store) Set(account string, st AccountState) error {
	_, err := s.db.Exec(`
INSERT INTO account_state (account, last_uid, uid_validity, updated_at)
VALUES (?, ?, ?, strftime('%s','now'))
ON CONFLICT(account) DO UPDATE SET
    last_uid     = excluded.last_uid,
    uid_validity = excluded.uid_validity,
    updated_at   = excluded.updated_at`,
		account, st.LastUID, st.UIDValidity,
	)
	if err != nil {
		return fmt.Errorf("save state for %s: %w", account, err)
	}
	return nil
}

func (s *Store) IsDuplicate(account, messageID string) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM seen_messages WHERE message_id = ? AND account = ?`,
		messageID, account,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check duplicate: %w", err)
	}
	return count > 0, nil
}

func (s *Store) MarkDelivered(account, messageID string) error {
	if messageID == "" {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO seen_messages (message_id, account, created_at) VALUES (?, ?, strftime('%s','now'))`,
		messageID, account,
	)
	if err != nil {
		return fmt.Errorf("mark delivered: %w", err)
	}
	return nil
}

func (s *Store) CleanSeen(before time.Time) error {
	_, err := s.db.Exec(`DELETE FROM seen_messages WHERE created_at < ?`, before.Unix())
	if err != nil {
		return fmt.Errorf("clean seen: %w", err)
	}
	return nil
}

// EnqueuePending inserts or updates a failed-delivery row. When the row
// already exists its failed-notifier set is merged (union) and its original
// created time preserved, so the give-up age counts from the first failure.
// A zero p.Created means "now"; callers may seed an explicit creation time.
func (s *Store) EnqueuePending(p Pending, nextTry time.Time) error {
	existing, found, err := s.getPending(p.Account, p.UID)
	if err != nil {
		return err
	}
	failed := p.Failed
	attempts := p.Attempts
	created := p.Created
	if created.IsZero() {
		created = time.Now()
	}
	if found {
		failed = union(existing.Failed, p.Failed)
		attempts = existing.Attempts
		created = existing.Created
	}
	_, err = s.db.Exec(`
INSERT INTO pending_notifications
    (account, uid, message_id, sender, subject, date_unix, preview, failed, title_override, text_override, fields, attempts, next_try_unix, created_unix)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(account, uid) DO UPDATE SET
    message_id = excluded.message_id,
    sender     = excluded.sender,
    subject    = excluded.subject,
    date_unix  = excluded.date_unix,
    preview    = excluded.preview,
    failed     = excluded.failed,
    title_override = excluded.title_override,
    text_override  = excluded.text_override,
    fields         = excluded.fields,
    next_try_unix = excluded.next_try_unix`,
		p.Account, p.UID, p.MessageID, p.From, p.Subject,
		p.Date.Unix(), p.Preview, strings.Join(failed, ","),
		p.TitleOverride, p.TextOverride, encodeFields(p.Fields),
		attempts, nextTry.Unix(), created.Unix(),
	)
	if err != nil {
		return fmt.Errorf("enqueue pending: %w", err)
	}
	return nil
}

func (s *Store) getPending(account string, uid uint32) (Pending, bool, error) {
	var p Pending
	var dateUnix, nextTry, created int64
	var failed, fieldsJSON string
	err := s.db.QueryRow(`
SELECT account, uid, message_id, sender, subject, date_unix, preview, failed, title_override, text_override, fields, attempts, next_try_unix, created_unix
FROM pending_notifications WHERE account = ? AND uid = ?`,
		account, uid,
	).Scan(&p.Account, &p.UID, &p.MessageID, &p.From, &p.Subject,
		&dateUnix, &p.Preview, &failed, &p.TitleOverride, &p.TextOverride, &fieldsJSON,
		&p.Attempts, &nextTry, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Pending{}, false, nil
	}
	if err != nil {
		return Pending{}, false, fmt.Errorf("get pending: %w", err)
	}
	p.Date = time.Unix(dateUnix, 0)
	p.NextTry = time.Unix(nextTry, 0)
	p.Created = time.Unix(created, 0)
	p.Failed = splitCSV(failed)
	p.Fields = decodeFields(fieldsJSON)
	return p, true, nil
}

// DuePending returns rows for the account whose backoff has elapsed, ordered
// by UID. Rows whose next_try is in the future are skipped entirely (not just
// the loop), so a poll cycle cannot stall behind an unreachable notifier.
func (s *Store) DuePending(account string, now time.Time, limit int) ([]Pending, error) {
	rows, err := s.db.Query(`
SELECT account, uid, message_id, sender, subject, date_unix, preview, failed, title_override, text_override, fields, attempts, next_try_unix, created_unix
FROM pending_notifications
WHERE account = ? AND next_try_unix <= ?
ORDER BY uid LIMIT ?`,
		account, now.Unix(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("due pending: %w", err)
	}
	defer rows.Close()

	var out []Pending
	for rows.Next() {
		var p Pending
		var dateUnix, nextTry, created int64
		var failed, fieldsJSON string
		if err := rows.Scan(&p.Account, &p.UID, &p.MessageID, &p.From, &p.Subject,
			&dateUnix, &p.Preview, &failed, &p.TitleOverride, &p.TextOverride, &fieldsJSON,
			&p.Attempts, &nextTry, &created); err != nil {
			return nil, fmt.Errorf("scan pending: %w", err)
		}
		p.Date = time.Unix(dateUnix, 0)
		p.NextTry = time.Unix(nextTry, 0)
		p.Created = time.Unix(created, 0)
		p.Failed = splitCSV(failed)
		p.Fields = decodeFields(fieldsJSON)
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePendingRetry records a failed retry round: bumps attempts, stores the
// still-failing notifier set and the next attempt time.
func (s *Store) UpdatePendingRetry(account string, uid uint32, failed []string, attempts int, nextTry time.Time) error {
	_, err := s.db.Exec(`
UPDATE pending_notifications
SET failed = ?, attempts = ?, next_try_unix = ?
WHERE account = ? AND uid = ?`,
		strings.Join(failed, ","), attempts, nextTry.Unix(), account, uid,
	)
	if err != nil {
		return fmt.Errorf("update pending retry: %w", err)
	}
	return nil
}

func (s *Store) DeletePending(account string, uid uint32) error {
	_, err := s.db.Exec(`DELETE FROM pending_notifications WHERE account = ? AND uid = ?`, account, uid)
	if err != nil {
		return fmt.Errorf("delete pending: %w", err)
	}
	return nil
}

// PendingCount counts queued undelivered notifications for an account.
func (s *Store) PendingCount(account string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pending_notifications WHERE account = ?`, account).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count pending: %w", err)
	}
	return n, nil
}

// encodeFields serialises plugin-extracted fields for storage. An empty map
// stores as the empty string so legacy rows and plugin-less rows look alike.
func encodeFields(fields map[string]string) string {
	if len(fields) == 0 {
		return ""
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	return string(b)
}

// decodeFields reverses encodeFields. Malformed JSON is treated as absent
// rather than failing the whole retry: a corrupt cache must not block mail.
func decodeFields(s string) map[string]string {
	if s == "" {
		return nil
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte(s), &fields); err != nil {
		return nil
	}
	return fields
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func union(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, list := range [][]string{a, b} {
		for _, v := range list {
			if v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}
