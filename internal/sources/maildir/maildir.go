// Package maildir is the maildir source component. It watches the new/
// directory of a standard maildir — the integration point for local mail
// stacks: postfix/dovecot delivery, fetchmail/getmail retrieval, or any tool
// that drops RFC 5322 files. Acking a message moves it from new/ to cur/,
// which is exactly what "handled" means in maildir semantics, so other mail
// clients see the same state mailer does.
package maildir

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterSource("maildir", New)
}

type Config struct {
	// Dir is the maildir root (the directory containing new/cur/tmp).
	Dir string `yaml:"dir"`
	// PollInterval overrides the daemon-wide default for this mailbox.
	PollInterval time.Duration `yaml:"poll_interval"`
	// NotifyExisting delivers mail already sitting in new/ at startup;
	// the default false treats it as the baseline.
	NotifyExisting bool `yaml:"notify_existing"`
	// PreviewLen caps the extracted preview in runes.
	PreviewLen int `yaml:"preview_len"`
	// MarkRead moves acked messages from new/ to cur/. Disable only when
	// another tool owns maildir state transitions.
	MarkRead bool `yaml:"mark_read"`
}

// cursorData is the on-disk cursor format: the set of filenames already
// emitted, so restarts do not re-notify. Baseline files are recorded the
// same way; the dedup store is the second line of defence.
type cursorData struct {
	Seen []string `json:"seen"`
}

type Source struct {
	name   string
	cfg    Config
	store  core.Store
	events chan core.Event

	mu   sync.Mutex
	seen map[string]bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New is the component factory for `type: maildir`.
func New(name string, raw map[string]any, deps component.Deps) (core.Source, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("maildir source %q: %w", name, err)
	}
	if cfg.Dir == "" {
		return nil, fmt.Errorf("maildir source %q: dir is required", name)
	}
	st, err := os.Stat(filepath.Join(cfg.Dir, "new"))
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("maildir source %q: %s is not a maildir (missing new/): %v", name, cfg.Dir, err)
	}
	if cfg.PreviewLen <= 0 {
		cfg.PreviewLen = 200
	}
	if deps.Store == nil {
		return nil, fmt.Errorf("maildir source %q: a state store is required", name)
	}
	return &Source{
		name:   name,
		cfg:    cfg,
		store:  deps.Store,
		events: make(chan core.Event, 16),
		seen:   map[string]bool{},
	}, nil
}

func (s *Source) Name() string { return s.name }
func (s *Source) Type() string { return "maildir" }

func (s *Source) Events() <-chan core.Event { return s.events }

func (s *Source) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	if err := s.loadCursor(); err != nil {
		log.Printf("[%s] cursor unreadable, rescanning mailbox: %v", s.name, err)
	}
	s.wg.Add(1)
	go s.loop()
	return nil
}

func (s *Source) Stop(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

func (s *Source) loadCursor() error {
	data, found, err := s.store.GetCursor(s.name)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	var cur cursorData
	if err := json.Unmarshal(data, &cur); err != nil {
		return err
	}
	for _, f := range cur.Seen {
		s.seen[f] = true
	}
	return nil
}

func (s *Source) saveCursorLocked() {
	// Cap the cursor: maildirs with heavy traffic would otherwise grow the
	// blob without bound. 10k filenames is far beyond any reasonable new/
	// backlog, and the dedup window covers eviction overlap.
	const maxSeen = 10000
	names := make([]string, 0, len(s.seen))
	for f := range s.seen {
		names = append(names, f)
	}
	sort.Strings(names)
	if len(names) > maxSeen {
		names = names[len(names)-maxSeen:]
		s.seen = map[string]bool{}
		for _, n := range names {
			s.seen[n] = true
		}
	}
	data, err := json.Marshal(cursorData{Seen: names})
	if err != nil {
		return
	}
	if err := s.store.SetCursor(s.name, data); err != nil {
		log.Printf("[%s] cursor save failed: %v", s.name, err)
	}
}

func (s *Source) loop() {
	defer s.wg.Done()
	defer close(s.events)
	interval := s.cfg.PollInterval
	if interval <= 0 {
		interval = time.Minute
	}
	first := true
	timer := time.NewTimer(0) // fire immediately for the baseline scan
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			s.scan(first)
			first = false
			timer.Reset(interval)
		}
	}
}

func (s *Source) scan(firstRun bool) {
	newDir := filepath.Join(s.cfg.Dir, "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		log.Printf("[%s] scan %s: %v", s.name, newDir, err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fname := e.Name()
		s.mu.Lock()
		already := s.seen[fname]
		s.mu.Unlock()
		if already {
			continue
		}
		msg, err := s.parseFile(filepath.Join(newDir, fname), fname)
		if err != nil {
			// A partial write is the common cause: mail delivery writes into
			// tmp/ and then link()s into new/, so a broken file usually means
			// something unusual. Log once per scan rather than failing the scan.
			log.Printf("[%s] parse %s: %v", s.name, fname, err)
			continue
		}
		s.mu.Lock()
		s.seen[fname] = true
		s.saveCursorLocked()
		s.mu.Unlock()

		if firstRun && !s.cfg.NotifyExisting {
			// Baseline scan: record but do not emit — matches the IMAP source's
			// notify_existing semantics.
			continue
		}
		select {
		case s.events <- core.Event{Message: msg, FirstRun: firstRun}:
		case <-s.ctx.Done():
			return
		}
	}
}

// parseFile reads one maildir message file into a core.Message. Maildir
// filenames are unique by construction; the name doubles as the ack key.
func (s *Source) parseFile(path, fname string) (core.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return core.Message{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 32<<20))
	if err != nil {
		return core.Message{}, err
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		return core.Message{}, fmt.Errorf("parse rfc5322: %w", err)
	}

	m := core.Message{
		Source:  s.name,
		Account: s.name,
		Subject: decodeHeader(msg.Header.Get("Subject")),
		From:    decodeHeader(msg.Header.Get("From")),
		Headers: map[string]string{},
		Ref: map[string]string{
			// "uid" carries the maildir filename so notifier read-buttons
			// address the message through the generic uid contract. "file"
			// mirrors it for operators reading queue rows.
			"uid":  fname,
			"file": fname,
		},
	}
	if id := msg.Header.Get("Message-Id"); id != "" {
		m.ID = id
	} else {
		// No Message-ID — dedup falls back to the unique maildir filename,
		// which is stable for the lifetime of the file.
		m.ID = "maildir:" + fname
	}
	if d := msg.Header.Get("Date"); d != "" {
		if t, err := mail.ParseDate(d); err == nil {
			m.Date = t
		}
	}
	if m.Date.IsZero() {
		if st, err := os.Stat(path); err == nil {
			m.Date = st.ModTime()
		} else {
			m.Date = time.Now()
		}
	}
	if to := msg.Header.Get("To"); to != "" {
		m.To = splitAddrs(to)
	}
	if cc := msg.Header.Get("Cc"); cc != "" {
		m.Cc = splitAddrs(cc)
	}
	for _, h := range []string{"Message-Id", "List-Id", "X-Spam-Flag", "X-Mailer", "Reply-To", "References"} {
		if v := msg.Header.Get(h); v != "" {
			m.Headers[h] = decodeHeader(v)
		}
	}

	body, err := io.ReadAll(io.LimitReader(msg.Body, 8<<20))
	if err != nil {
		return m, nil // headers are still useful without a body
	}
	ctype := msg.Header.Get("Content-Type")
	mediaType, params, _ := mime.ParseMediaType(ctype)
	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		m.Text, m.HTML, m.Attachments = walkMultipartBytes(body, params["boundary"])
	case strings.HasPrefix(mediaType, "text/html"):
		m.HTML = string(body)
	default:
		m.Text = decodeTransfer(string(body), msg.Header.Get("Content-Transfer-Encoding"))
	}
	m.Preview = preview(m.Text, m.HTML, s.cfg.PreviewLen)
	return m, nil
}

// decodeHeader decodes RFC 2047 encoded words, falling back to the raw
// value when decoding fails — a malformed header is still better shown
// raw than dropped.
func decodeHeader(v string) string {
	if d, err := new(mime.WordDecoder).DecodeHeader(v); err == nil {
		return d
	}
	return v
}

func splitAddrs(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func walkMultipartBytes(body []byte, boundary string) (plain, html string, atts []core.Attachment) {
	if boundary == "" {
		return "", "", nil
	}
	mr := multipart.NewReader(strings.NewReader(string(body)), boundary)
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		data, err := io.ReadAll(io.LimitReader(part, 8<<20))
		if err != nil {
			continue
		}
		pct, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		fname := decodeHeader(part.FileName())
		disp := part.Header.Get("Content-Disposition")
		isAttach := fname != "" || strings.HasPrefix(disp, "attachment")
		switch {
		case isAttach:
			atts = append(atts, core.Attachment{
				Filename:    fname,
				ContentType: strings.ToLower(pct),
				Size:        int64(len(data)),
			})
		case strings.HasPrefix(pct, "text/html") && html == "":
			html = decodeTransfer(string(data), part.Header.Get("Content-Transfer-Encoding"))
		case (pct == "" || strings.HasPrefix(pct, "text/plain")) && plain == "":
			plain = decodeTransfer(string(data), part.Header.Get("Content-Transfer-Encoding"))
		case strings.HasPrefix(pct, "multipart/"):
			// Nested multipart (alternative inside mixed): recurse one level
			// via the part's own boundary.
			_, nestedParams, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			if nb := nestedParams["boundary"]; nb != "" {
				np, nh, na := walkMultipartBytes(data, nb)
				if plain == "" {
					plain = np
				}
				if html == "" {
					html = nh
				}
				atts = append(atts, na...)
			}
		}
	}
	return plain, html, atts
}

// decodeTransfer handles the common transfer encodings. base64 and
// quoted-printable both appear constantly in real mail; anything else is
// passed through as-is since misdecoding is worse than noise.
func decodeTransfer(s, encoding string) string {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		// net/mail does not expose QP decoding directly in older Go versions;
		// mime/multipart handles it automatically, but single-part messages
		// arrive here. A minimal QP decoder covers the common cases.
		return decodeQP(s)
	default:
		return s
	}
}

func decodeQP(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '=' && i+2 < len(s) {
			if s[i+1] == '\r' || s[i+1] == '\n' {
				// soft line break: skip = and the newline
				if s[i+1] == '\r' && i+3 < len(s) && s[i+2] == '\n' {
					i += 2
				} else {
					i++
				}
				continue
			}
			hex := s[i+1 : i+3]
			if v, ok := fromHex(hex); ok {
				b.WriteByte(v)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func fromHex(s string) (byte, bool) {
	if len(s) != 2 {
		return 0, false
	}
	var v byte
	for _, c := range s {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v += byte(c - '0')
		case c >= 'A' && c <= 'F':
			v += byte(c-'A') + 10
		case c >= 'a' && c <= 'f':
			v += byte(c-'a') + 10
		default:
			return 0, false
		}
	}
	return v, true
}

// preview prefers the plain text body; HTML is stripped crudely (tags
// removed) since the render package owns the polished conversion.
func preview(text, html string, maxRunes int) string {
	src := text
	if src == "" {
		src = stripTags(html)
	}
	src = strings.Join(strings.Fields(src), " ")
	r := []rune(src)
	if len(r) > maxRunes {
		return string(r[:maxRunes-1]) + "…"
	}
	return src
}

func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Ack moves the message file from new/ to cur/, the maildir-native way of
// saying "this has been handled". It is idempotent: an already-moved or
// externally-deleted file is not an error.
func (s *Source) Ack(ctx context.Context, msg core.Message) error {
	if !s.cfg.MarkRead {
		return nil
	}
	return s.moveToCur(msg.Ref["file"])
}

// moveToCur transitions a message file from new/ to cur/ with the \Seen
// flag (:2,S). It is idempotent: an already-moved or externally-deleted
// file is the desired end state, not an error.
func (s *Source) moveToCur(fname string) error {
	if fname == "" {
		return nil
	}
	// Defence in depth: filenames come from the filesystem, but a hostile
	// delivery tool could still craft pathological names. Maildir base
	// names never contain a path separator.
	if strings.ContainsAny(fname, "/\x00") {
		return fmt.Errorf("maildir: refusing pathological filename %q", fname)
	}
	src := filepath.Join(s.cfg.Dir, "new", fname)
	dst := filepath.Join(s.cfg.Dir, "cur", fname+":2,S")
	if err := os.Rename(src, dst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // already moved by another client — desired end state
		}
		return fmt.Errorf("move %s to cur: %w", fname, err)
	}
	return nil
}

// Command honours mark_read (the notifier read-button) by moving the file
// to cur/, identical to what Ack does. Args["uid"] carries the filename —
// see the Ref construction in parseFile.
func (s *Source) Command(ctx context.Context, cmd core.Command) error {
	fname := cmd.Args["uid"]
	if fname == "" {
		return fmt.Errorf("maildir command %s: missing uid", cmd.Kind)
	}
	switch cmd.Kind {
	case "mark_read", "delivered":
		return s.moveToCur(fname)
	default:
		return core.ErrUnsupportedCommandf(cmd.Kind)
	}
}
