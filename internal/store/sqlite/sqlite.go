// Package sqlite implements core.Store on a single-file SQLite database.
//
// Schema v2 generalises v1: cursors are opaque per-source blobs, queue rows
// carry the whole envelope as JSON (so new envelope fields never need a
// migration again), and a namespaced KV table backs stateful processors and
// the LLM result cache.
//
// Loading a v1 database migrates it in place: account_state rows become
// cursor blobs in the shape the imap source understands, legacy pending rows
// are re-encoded as envelopes and moved to the new queue table, and the
// legacy tables are dropped. The migration runs inside one transaction; a
// failure leaves the old tables untouched.
package sqlite

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

	"github.com/elysola/mailer/internal/core"
)

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS cursors (
    source     TEXT  PRIMARY KEY,
    data       BLOB  NOT NULL,
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE TABLE IF NOT EXISTS seen_messages (
    message_id TEXT    NOT NULL,
    source     TEXT    NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY (message_id, source)
);
CREATE INDEX IF NOT EXISTS idx_seen_created ON seen_messages(created_at);
CREATE TABLE IF NOT EXISTS queue (
    source       TEXT    NOT NULL,
    key          TEXT    NOT NULL,
    kind         TEXT    NOT NULL DEFAULT 'retry',
    payload      BLOB    NOT NULL,
    failed       TEXT    NOT NULL DEFAULT '',
    attempts     INTEGER NOT NULL DEFAULT 0,
    next_try_unix INTEGER NOT NULL DEFAULT 0,
    created_unix INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY (source, key)
);
CREATE TABLE IF NOT EXISTS kv (
    ns         TEXT   NOT NULL,
    key        TEXT   NOT NULL,
    value      BLOB   NOT NULL,
    expires_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (ns, key)
);
`

// Load opens (creating if necessary) the database at path and migrates it to
// the current schema.
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
	if err := migrateV1(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate v1 state: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// ---- cursors ---------------------------------------------------------------

func (s *Store) GetCursor(source string) ([]byte, bool, error) {
	var data []byte
	row := s.db.QueryRow(`SELECT data FROM cursors WHERE source = ?`, source)
	if err := row.Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get cursor %q: %w", source, err)
	}
	return data, true, nil
}

func (s *Store) SetCursor(source string, data []byte) error {
	_, err := s.db.Exec(`
INSERT INTO cursors (source, data, updated_at) VALUES (?, ?, strftime('%s','now'))
ON CONFLICT(source) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		source, data)
	if err != nil {
		return fmt.Errorf("set cursor %q: %w", source, err)
	}
	return nil
}

// ---- dedup -----------------------------------------------------------------

func (s *Store) IsDuplicate(source, messageID string) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM seen_messages WHERE message_id = ? AND source = ?`,
		messageID, source,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check duplicate: %w", err)
	}
	return count > 0, nil
}

func (s *Store) MarkDelivered(source, messageID string) error {
	if messageID == "" {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO seen_messages (message_id, source, created_at) VALUES (?, ?, strftime('%s','now'))`,
		messageID, source,
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

func (s *Store) SeenCount(source string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM seen_messages WHERE source = ?`, source).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count seen: %w", err)
	}
	return n, nil
}

// ---- queue -----------------------------------------------------------------

// Enqueue inserts or updates a queue row. When the row already exists its
// failed-notifier set is merged (union) and its original creation time
// preserved, so the give-up age counts from the first failure.
func (s *Store) Enqueue(item core.QueuedItem) error {
	payload, err := core.EncodeEnvelope(item.Envelope)
	if err != nil {
		return fmt.Errorf("encode envelope: %w", err)
	}

	failed := item.Failed
	attempts := item.Attempts
	created := item.Created
	if created.IsZero() {
		created = time.Now()
	}
	if existing, found, err := s.row(item.Source, item.Key); err != nil {
		return err
	} else if found {
		failed = union(existing.Failed, item.Failed)
		if attempts == 0 {
			attempts = existing.Attempts
		}
		created = existing.Created
	}
	if item.Kind == "" {
		item.Kind = core.QueueKindRetry
	}

	_, err = s.db.Exec(`
INSERT INTO queue (source, key, kind, payload, failed, attempts, next_try_unix, created_unix)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source, key) DO UPDATE SET
    kind          = excluded.kind,
    payload       = excluded.payload,
    failed        = excluded.failed,
    next_try_unix = excluded.next_try_unix`,
		item.Source, item.Key, item.Kind, payload, strings.Join(failed, ","),
		attempts, item.NextTry.Unix(), created.Unix(),
	)
	if err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	return nil
}

func (s *Store) row(source, key string) (core.QueuedItem, bool, error) {
	rows, err := s.db.Query(`
SELECT source, key, kind, payload, failed, attempts, next_try_unix, created_unix
FROM queue WHERE source = ? AND key = ?`, source, key)
	if err != nil {
		return core.QueuedItem{}, false, fmt.Errorf("get queue row: %w", err)
	}
	defer rows.Close()
	items, err := scanQueue(rows)
	if err != nil || len(items) == 0 {
		return core.QueuedItem{}, false, err
	}
	return items[0], true, nil
}

func (s *Store) Due(source string, now time.Time, limit int) ([]core.QueuedItem, error) {
	if limit <= 0 {
		limit = 200
	}
	return s.query(`
SELECT source, key, kind, payload, failed, attempts, next_try_unix, created_unix
FROM queue WHERE source = ? AND next_try_unix <= ?
ORDER BY next_try_unix LIMIT ?`, source, now.Unix(), limit)
}

func (s *Store) AllDue(now time.Time, limit int) ([]core.QueuedItem, error) {
	if limit <= 0 {
		limit = 500
	}
	return s.query(`
SELECT source, key, kind, payload, failed, attempts, next_try_unix, created_unix
FROM queue WHERE next_try_unix <= ?
ORDER BY next_try_unix LIMIT ?`, now.Unix(), limit)
}

func (s *Store) Rows(source string, limit int) ([]core.QueuedItem, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.query(`
SELECT source, key, kind, payload, failed, attempts, next_try_unix, created_unix
FROM queue WHERE source = ?
ORDER BY created_unix, key LIMIT ?`, source, limit)
}

func (s *Store) AllRows(limit int) ([]core.QueuedItem, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.query(`
SELECT source, key, kind, payload, failed, attempts, next_try_unix, created_unix
FROM queue ORDER BY created_unix, key LIMIT ?`, limit)
}

func (s *Store) query(q string, args ...any) ([]core.QueuedItem, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query queue: %w", err)
	}
	defer rows.Close()
	return scanQueue(rows)
}

func scanQueue(rows *sql.Rows) ([]core.QueuedItem, error) {
	var out []core.QueuedItem
	for rows.Next() {
		var item core.QueuedItem
		var payload []byte
		var failed string
		var nextTry, created int64
		if err := rows.Scan(&item.Source, &item.Key, &item.Kind, &payload, &failed,
			&item.Attempts, &nextTry, &created); err != nil {
			return nil, fmt.Errorf("scan queue row: %w", err)
		}
		env, err := core.DecodeEnvelope(payload)
		if err != nil {
			// A corrupt payload must not block the whole queue; report the row
			// with an empty envelope so the operator can still drop it.
			item.Envelope = core.Envelope{Meta: map[string]string{"decode_error": err.Error()}}
		} else {
			item.Envelope = env
		}
		item.Failed = splitCSV(failed)
		item.NextTry = time.Unix(nextTry, 0)
		item.Created = time.Unix(created, 0)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UpdateRetry(source, key string, failed []string, attempts int, nextTry time.Time) error {
	_, err := s.db.Exec(`
UPDATE queue SET failed = ?, attempts = ?, next_try_unix = ?
WHERE source = ? AND key = ?`,
		strings.Join(failed, ","), attempts, nextTry.Unix(), source, key)
	if err != nil {
		return fmt.Errorf("update retry: %w", err)
	}
	return nil
}

func (s *Store) Delete(source, key string) error {
	_, err := s.db.Exec(`DELETE FROM queue WHERE source = ? AND key = ?`, source, key)
	if err != nil {
		return fmt.Errorf("delete queue row: %w", err)
	}
	return nil
}

func (s *Store) Count(source string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM queue WHERE source = ?`, source).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count queue: %w", err)
	}
	return n, nil
}

func (s *Store) DeleteForSource(source string) (int, error) {
	res, err := s.db.Exec(`DELETE FROM queue WHERE source = ?`, source)
	if err != nil {
		return 0, fmt.Errorf("delete queue rows: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return int(n), nil
}

// ---- operator inspection ---------------------------------------------------

// QueueRow is the flattened shape the queue CLI renders. Subject is extracted
// from the envelope for readability; everything else is the stored row.
type QueueRow struct {
	Source   string    `json:"source"`
	Key      string    `json:"key"`
	Kind     string    `json:"kind"`
	Failed   []string  `json:"failed"`
	Attempts int       `json:"attempts"`
	NextTry  time.Time `json:"next_try"`
	Created  time.Time `json:"created"`
	Subject  string    `json:"subject"`
	From     string    `json:"from"`
	Date     time.Time `json:"date"`
}

// QueueRows lists queue rows for one source (or all, when source is empty),
// oldest first, for the `mailer queue` commands.
func (s *Store) QueueRows(source string, limit int) ([]QueueRow, error) {
	var items []core.QueuedItem
	var err error
	if source == "" {
		items, err = s.AllRows(limit)
	} else {
		items, err = s.Rows(source, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]QueueRow, 0, len(items))
	for _, item := range items {
		out = append(out, QueueRow{
			Source:   item.Source,
			Key:      item.Key,
			Kind:     item.Kind,
			Failed:   item.Failed,
			Attempts: item.Attempts,
			NextTry:  item.NextTry,
			Created:  item.Created,
			Subject:  item.Envelope.Message.Subject,
			From:     item.Envelope.Message.From,
			Date:     item.Envelope.Message.Date,
		})
	}
	return out, nil
}

// DeleteQueued drops queued rows for one source, or all rows when source is
// empty. Returns the number of rows removed.
func (s *Store) DeleteQueued(source string) (int, error) {
	if source != "" {
		return s.DeleteForSource(source)
	}
	res, err := s.db.Exec(`DELETE FROM queue`)
	if err != nil {
		return 0, fmt.Errorf("delete all queue rows: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return int(n), nil
}

// ---- kv --------------------------------------------------------------------

func (s *Store) KVGet(ns, key string) ([]byte, bool, error) {
	var value []byte
	var expiresAt int64
	row := s.db.QueryRow(`SELECT value, expires_at FROM kv WHERE ns = ? AND key = ?`, ns, key)
	if err := row.Scan(&value, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("kv get %s/%s: %w", ns, key, err)
	}
	if expiresAt != 0 && time.Now().Unix() > expiresAt {
		// Expired: treat as absent. The periodic CleanKV reclaims the row;
		// deleting it here would turn every read into a write.
		return nil, false, nil
	}
	return value, true, nil
}

func (s *Store) KVSet(ns, key string, value []byte, ttl time.Duration) error {
	var expiresAt int64
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl).Unix()
	}
	_, err := s.db.Exec(`
INSERT INTO kv (ns, key, value, expires_at) VALUES (?, ?, ?, ?)
ON CONFLICT(ns, key) DO UPDATE SET value = excluded.value, expires_at = excluded.expires_at`,
		ns, key, value, expiresAt)
	if err != nil {
		return fmt.Errorf("kv set %s/%s: %w", ns, key, err)
	}
	return nil
}

func (s *Store) KVDelete(ns, key string) error {
	_, err := s.db.Exec(`DELETE FROM kv WHERE ns = ? AND key = ?`, ns, key)
	if err != nil {
		return fmt.Errorf("kv delete %s/%s: %w", ns, key, err)
	}
	return nil
}

func (s *Store) CleanKV(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM kv WHERE expires_at != 0 AND expires_at < ?`, now.Unix())
	if err != nil {
		return fmt.Errorf("clean kv: %w", err)
	}
	return nil
}

// ---- migration -------------------------------------------------------------

// migrateV1 upgrades a v1 database in place. Detection: the legacy
// account_state table exists. Everything runs in one transaction so a crash
// mid-migration cannot leave half-converted state.
func migrateV1(db *sql.DB) error {
	hasAccountState, err := tableExists(db, "account_state")
	if err != nil {
		return err
	}
	hasSeenV1, err := columnIsNamed(db, "seen_messages", "account")
	if err != nil {
		return err
	}
	hasPending, err := tableExists(db, "pending_notifications")
	if err != nil {
		return err
	}
	if !hasAccountState && !hasSeenV1 && !hasPending {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()

	if hasAccountState {
		if err := migrateCursors(tx); err != nil {
			return err
		}
		if _, err := tx.Exec(`DROP TABLE account_state`); err != nil {
			return fmt.Errorf("drop account_state: %w", err)
		}
	}
	if hasSeenV1 {
		// seen_messages is structurally identical in v2 except the column
		// name; copy into the fresh table rather than renaming, because the
		// v2 CREATE may already have run on a half-migrated database.
		if err := migrateSeen(tx); err != nil {
			return err
		}
	}
	if hasPending {
		if err := migratePending(tx); err != nil {
			return err
		}
		if _, err := tx.Exec(`DROP TABLE pending_notifications`); err != nil {
			return fmt.Errorf("drop pending_notifications: %w", err)
		}
	}
	return tx.Commit()
}

func tableExists(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("inspect schema: %w", err)
	}
	return n > 0, nil
}

func columnIsNamed(db *sql.DB, table, column string) (bool, error) {
	exists, err := tableExists(db, table)
	if err != nil || !exists {
		return false, err
	}
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name, typ  string
			notNull    int
			dflt       sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &primaryKey); err != nil {
			return false, fmt.Errorf("inspect %s: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// imapCursorV1 mirrors the JSON shape the imap source uses for its cursor
// blob. Keeping the literal here (rather than importing the source package)
// is what lets the store stay component-agnostic; the imap source documents
// the same shape as its stable cursor format.
type imapCursorV1 struct {
	LastUID     uint32 `json:"last_uid"`
	UIDValidity uint32 `json:"uid_validity"`
}

func migrateCursors(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT account, last_uid, uid_validity FROM account_state`)
	if err != nil {
		return fmt.Errorf("read legacy cursors: %w", err)
	}
	type legacyCursor struct {
		account string
		data    []byte
	}
	var legacy []legacyCursor
	for rows.Next() {
		var account string
		var cur imapCursorV1
		if err := rows.Scan(&account, &cur.LastUID, &cur.UIDValidity); err != nil {
			rows.Close()
			return fmt.Errorf("read legacy cursors: %w", err)
		}
		data, err := json.Marshal(cur)
		if err != nil {
			rows.Close()
			return fmt.Errorf("encode cursor: %w", err)
		}
		legacy = append(legacy, legacyCursor{account: account, data: data})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range legacy {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO cursors (source, data) VALUES (?, ?)`, l.account, l.data); err != nil {
			return fmt.Errorf("write cursor %q: %w", l.account, err)
		}
	}
	return nil
}

func migrateSeen(tx *sql.Tx) error {
	// The v1 table was created by the v1 binary. v2's CREATE IF NOT EXISTS
	// will have created a fresh `source`-column table only when the v1 table
	// never existed — but a v1 table shadows it since names collide. Detect
	// which shape exists by probing for the legacy column: if we got here,
	// the table IS the v1 shape, so rename it aside, create the new one,
	// copy, and drop.
	if _, err := tx.Exec(`ALTER TABLE seen_messages RENAME TO seen_messages_v1`); err != nil {
		return fmt.Errorf("rename seen_messages: %w", err)
	}
	if _, err := tx.Exec(`
CREATE TABLE seen_messages (
    message_id TEXT    NOT NULL,
    source     TEXT    NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY (message_id, source)
)`); err != nil {
		return fmt.Errorf("recreate seen_messages: %w", err)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO seen_messages (message_id, source, created_at)
SELECT message_id, account, created_at FROM seen_messages_v1`); err != nil {
		return fmt.Errorf("copy seen_messages: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE seen_messages_v1`); err != nil {
		return fmt.Errorf("drop seen_messages_v1: %w", err)
	}
	return nil
}

// migratePending re-encodes v1 pending rows as envelopes in the new queue
// table. Legacy columns map onto the message and the override fields, so a
// row waiting for retry survives the upgrade with its plugin-rendered output,
// attachments, attempts count and original creation time intact.
func migratePending(tx *sql.Tx) error {
	// Older v1 databases may lack the columns added later; probe first and
	// select with defaults for missing ones.
	cols, err := pendingColumns(tx)
	if err != nil {
		return err
	}
	selectCol := func(name string) string {
		if cols[name] {
			return name
		}
		return "'' AS " + name
	}
	query := fmt.Sprintf(`
SELECT account, uid, message_id, sender, subject, date_unix, preview, failed,
       %s, %s, %s, %s, attempts, next_try_unix, created_unix
FROM pending_notifications`,
		selectCol("title_override"), selectCol("text_override"),
		selectCol("fields"), selectCol("attachments"))

	rows, err := tx.Query(query)
	if err != nil {
		return fmt.Errorf("read legacy pending: %w", err)
	}
	type legacyRow struct {
		item core.QueuedItem
	}
	var legacy []legacyRow
	for rows.Next() {
		var (
			account, messageID, sender, subject, preview, failed     string
			titleOverride, textOverride, fieldsJSON, attachmentsJSON string
			uid, attempts                                            int64
			dateUnix, nextTry, created                               int64
		)
		if err := rows.Scan(&account, &uid, &messageID, &sender, &subject, &dateUnix,
			&preview, &failed, &titleOverride, &textOverride, &fieldsJSON, &attachmentsJSON,
			&attempts, &nextTry, &created); err != nil {
			rows.Close()
			return fmt.Errorf("read legacy pending: %w", err)
		}
		env := core.Envelope{
			Message: core.Message{
				ID:          messageID,
				Source:      account,
				Account:     account,
				From:        sender,
				Subject:     subject,
				Date:        time.Unix(dateUnix, 0),
				Preview:     preview,
				Ref:         map[string]string{"uid": fmt.Sprintf("%d", uid)},
				Attachments: decodeLegacyAttachments(attachmentsJSON),
			},
			Title:  titleOverride,
			Body:   textOverride,
			Fields: decodeLegacyFields(fieldsJSON),
		}
		legacy = append(legacy, legacyRow{item: core.QueuedItem{
			Source:   account,
			Key:      fmt.Sprintf("%d", uid),
			Kind:     core.QueueKindRetry,
			Envelope: env,
			Failed:   splitCSV(failed),
			Attempts: int(attempts),
			NextTry:  time.Unix(nextTry, 0),
			Created:  time.Unix(created, 0),
		}})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, l := range legacy {
		payload, err := core.EncodeEnvelope(l.item.Envelope)
		if err != nil {
			return fmt.Errorf("encode migrated envelope: %w", err)
		}
		if _, err := tx.Exec(`
INSERT OR REPLACE INTO queue (source, key, kind, payload, failed, attempts, next_try_unix, created_unix)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			l.item.Source, l.item.Key, l.item.Kind, payload,
			strings.Join(l.item.Failed, ","), l.item.Attempts,
			l.item.NextTry.Unix(), l.item.Created.Unix()); err != nil {
			return fmt.Errorf("write migrated queue row: %w", err)
		}
	}
	return nil
}

func pendingColumns(tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.Query(`PRAGMA table_info(pending_notifications)`)
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
	return cols, rows.Err()
}

func decodeLegacyFields(s string) map[string]string {
	if s == "" {
		return nil
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte(s), &fields); err != nil {
		return nil
	}
	return fields
}

func decodeLegacyAttachments(s string) []core.Attachment {
	if s == "" {
		return nil
	}
	var atts []core.Attachment
	if err := json.Unmarshal([]byte(s), &atts); err != nil {
		return nil
	}
	return atts
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
