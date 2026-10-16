package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/mail"
	"github.com/elysola/mailer/internal/notify"
	"github.com/elysola/mailer/internal/state"
)

// Self-signed cert copied from go-imap's imapclient test suite (CN=Acme Co,
// valid until 2084). Paired with MAILER_TLS_SKIP_VERIFY in dialOptions.
const rsaCertPEM = `-----BEGIN CERTIFICATE-----
MIIDOTCCAiGgAwIBAgIQSRJrEpBGFc7tNb1fb5pKFzANBgkqhkiG9w0BAQsFADAS
MRAwDgYDVQQKEwdBY21lIENvMCAXDTcwMDEwMTAwMDAwMFoYDzIwODQwMTI5MTYw
MDAwWjASMRAwDgYDVQQKEwdBY21lIENvMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A
MIIBCgKCAQEA6Gba5tHV1dAKouAaXO3/ebDUU4rvwCUg/CNaJ2PT5xLD4N1Vcb8r
bFSW2HXKq+MPfVdwIKR/1DczEoAGf/JWQTW7EgzlXrCd3rlajEX2D73faWJekD0U
aUgz5vtrTXZ90BQL7WvRICd7FlEZ6FPOcPlumiyNmzUqtwGhO+9ad1W5BqJaRI6P
YfouNkwR6Na4TzSj5BrqUfP0FwDizKSJ0XXmh8g8G9mtwxOSN3Ru1QFc61Xyeluk
POGKBV/q6RBNklTNe0gI8usUMlYyoC7ytppNMW7X2vodAelSu25jgx2anj9fDVZu
h7AXF5+4nJS4AAt0n1lNY7nGSsdZas8PbQIDAQABo4GIMIGFMA4GA1UdDwEB/wQE
AwICpDATBgNVHSUEDDAKBggrBgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MB0GA1Ud
DgQWBBStsdjh3/JCXXYlQryOrL4Sh7BW5TAuBgNVHREEJzAlggtleGFtcGxlLmNv
bYcEfwAAAYcQAAAAAAAAAAAAAAAAAAAAATANBgkqhkiG9w0BAQsFAAOCAQEAxWGI
5NhpF3nwwy/4yB4i/CwwSpLrWUa70NyhvprUBC50PxiXav1TeDzwzLx/o5HyNwsv
cxv3HdkLW59i/0SlJSrNnWdfZ19oTcS+6PtLoVyISgtyN6DpkKpdG1cOkW3Cy2P2
+tK/tKHRP1Y/Ra0RiDpOAmqn0gCOFGz8+lqDIor/T7MTpibL3IxqWfPrvfVRHL3B
grw/ZQTTIVjjh4JBSW3WyWgNo/ikC1lrVxzl4iPUGptxT36Cr7Zk2Bsg0XqwbOvK
5d+NTDREkSnUbie4GeutujmX3Dsx88UiV6UY/4lHJa6I5leHUNOHahRbpbWeOfs/
WkBKOclmOV2xlTVuPw==
-----END CERTIFICATE-----
`

const rsaKeyPEM = `-----BEGIN RSA PRIVATE KEY-----
MIIEvAIBADANBgkqhkiG9w0BAQEFAASCBKYwggSiAgEAAoIBAQDoZtrm0dXV0Aqi
4Bpc7f95sNRTiu/AJSD8I1onY9PnEsPg3VVxvytsVJbYdcqr4w99V3AgpH/UNzMS
gAZ/8lZBNbsSDOVesJ3euVqMRfYPvd9pYl6QPRRpSDPm+2tNdn3QFAvta9EgJ3sW
URnoU85w+W6aLI2bNSq3AaE771p3VbkGolpEjo9h+i42TBHo1rhPNKPkGupR8/QX
AOLMpInRdeaHyDwb2a3DE5I3dG7VAVzrVfJ6W6Q84YoFX+rpEE2SVM17SAjy6xQy
VjKgLvK2mk0xbtfa+h0B6VK7bmODHZqeP18NVm6HsBcXn7iclLgAC3SfWU1jucZK
x1lqzw9tAgMBAAECggEABWzxS1Y2wckblnXY57Z+sl6YdmLV+gxj2r8Qib7g4ZIk
lIlWR1OJNfw7kU4eryib4fc6nOh6O4AWZyYqAK6tqNQSS/eVG0LQTLTTEldHyVJL
dvBe+MsUQOj4nTndZW+QvFzbcm2D8lY5n2nBSxU5ypVoKZ1EqQzytFcLZpTN7d89
EPj0qDyrV4NZlWAwL1AygCwnlwhMQjXEalVF1ylXwU3QzyZ/6MgvF6d3SSUlh+sq
XefuyigXw484cQQgbzopv6niMOmGP3of+yV4JQqUSb3IDmmT68XjGd2Dkxl4iPki
6ZwXf3CCi+c+i/zVEcufgZ3SLf8D99kUGE7v7fZ6AQKBgQD1ZX3RAla9hIhxCf+O
3D+I1j2LMrdjAh0ZKKqwMR4JnHX3mjQI6LwqIctPWTU8wYFECSh9klEclSdCa64s
uI/GNpcqPXejd0cAAdqHEEeG5sHMDt0oFSurL4lyud0GtZvwlzLuwEweuDtvT9cJ
Wfvl86uyO36IW8JdvUprYDctrQKBgQDycZ697qutBieZlGkHpnYWUAeImVA878sJ
w44NuXHvMxBPz+lbJGAg8Cn8fcxNAPqHIraK+kx3po8cZGQywKHUWsxi23ozHoxo
+bGqeQb9U661TnfdDspIXia+xilZt3mm5BPzOUuRqlh4Y9SOBpSWRmEhyw76w4ZP
OPxjWYAgwQKBgA/FehSYxeJgRjSdo+MWnK66tjHgDJE8bYpUZsP0JC4R9DL5oiaA
brd2fI6Y+SbyeNBallObt8LSgzdtnEAbjIH8uDJqyOmknNePRvAvR6mP4xyuR+Bv
m+Lgp0DMWTw5J9CKpydZDItc49T/mJ5tPhdFVd+am0NAQnmr1MCZ6nHxAoGABS3Y
LkaC9FdFUUqSU8+Chkd/YbOkuyiENdkvl6t2e52jo5DVc1T7mLiIrRQi4SI8N9bN
/3oJWCT+uaSLX2ouCtNFunblzWHBrhxnZzTeqVq4SLc8aESAnbslKL4i8/+vYZlN
s8xtiNcSvL+lMsOBORSXzpj/4Ot8WwTkn1qyGgECgYBKNTypzAHeLE6yVadFp3nQ
Ckq9yzvP/ib05rvgbvrne00YeOxqJ9gtTrzgh7koqJyX1L4NwdkEza4ilDWpucn0
xiUZS4SoaJq6ZvcBYS62Yr1t8n09iG47YL8ibgtmH3L+svaotvpVxVK+d7BLevA/
ZboOWVe3icTy64BT3OQhmg==
-----END RSA PRIVATE KEY-----
`

const (
	testUser = "test-user"
	testPass = "test-password"
	testAcc  = "test"
)

// memEnv is an in-memory IMAP server plus the test account config pointing at
// it and a shared SQLite state file.
type memEnv struct {
	user *imapmemserver.User
	acc  config.Account
}

func startMemEnv(t *testing.T) *memEnv {
	t.Helper()

	// dialOptions verifies TLS unless this escape hatch is set; the cert
	// above is self-signed.
	t.Setenv("MAILER_TLS_SKIP_VERIFY", "1")

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testUser, testPass)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	mem.AddUser(user)

	cert, err := tls.X509KeyPair([]byte(rsaCertPEM), []byte(rsaKeyPEM))
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}

	server := imapserver.New(&imapserver.Options{
		NewSession: func(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		TLSConfig:    &tls.Config{Certificates: []tls.Certificate{cert}},
		InsecureAuth: true,
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {},
			imap.CapIMAP4rev2: {},
		},
	})

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// The imapserver library speaks STARTTLS over a plaintext listener;
	// wrap it in a TLS listener so the account's implicit-TLS dial path
	// (acc.TLS=true, what the deployed config uses) works unmodified.
	tln := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}})
	go func() { _ = server.Serve(tln) }()
	t.Cleanup(func() {
		_ = tln.Close()
		_ = server.Close()
	})

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	return &memEnv{
		user: user,
		acc: config.Account{
			Name:     testAcc,
			Host:     host,
			Port:     port,
			Username: testUser,
			Password: testPass,
			TLS:      true,
			Mailbox:  "INBOX",
		},
	}
}

// appendMail delivers a raw RFC822 message straight into the user's INBOX,
// bypassing the poller's own connection.
func (e *memEnv) appendMail(t *testing.T, raw string) {
	t.Helper()
	r := bytes.NewReader([]byte(raw))
	_, err := e.user.Append("INBOX", literalReader{r}, &imap.AppendOptions{})
	if err != nil {
		t.Fatalf("append mail: %v", err)
	}
}

type literalReader struct{ *bytes.Reader }

func (l literalReader) Size() int64 { return int64(l.Reader.Len()) }

func rawMail(msgID, subject string) string {
	return fmt.Sprintf("From: sender@example.com\r\n"+
		"To: %s@example.com\r\n"+
		"Subject: %s\r\n"+
		"Message-Id: %s\r\n"+
		"Date: Mon, 02 Jan 2026 10:00:00 +0000\r\n"+
		"MIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n"+
		"\r\n"+
		"Body for %s\r\n", testUser, subject, msgID, subject)
}

func openStore(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Load(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// fakeNotifier records deliveries and can be told to fail.
type fakeNotifier struct {
	name string

	mu        sync.Mutex
	fail      bool
	sent      []mail.Message
	sendCalls int
}

var _ notify.Notifier = (*fakeNotifier)(nil)

func newFake(name string, fail bool) *fakeNotifier {
	return &fakeNotifier{name: name, fail: fail}
}

func (f *fakeNotifier) Name() string { return f.name }

func (f *fakeNotifier) Send(ctx context.Context, msg mail.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalls++
	if f.fail {
		return errors.New("fake delivery failure")
	}
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeNotifier) setFail(v bool) {
	f.mu.Lock()
	f.fail = v
	f.mu.Unlock()
}

func (f *fakeNotifier) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeNotifier) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendCalls
}

func (f *fakeNotifier) lastUID() uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return 0
	}
	return f.sent[len(f.sent)-1].UID
}

func (f *fakeNotifier) uidList() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uint32, 0, len(f.sent))
	for _, m := range f.sent {
		out = append(out, m.UID)
	}
	return out
}

func testCfg(acc config.Account, extra ...func(*config.Config)) *config.Config {
	cfg := &config.Config{
		PollInterval:         time.Hour,
		RetryAttempts:        0, // dispatch tries once, no inline sleep in tests
		RetryDelay:           time.Millisecond,
		NoopInterval:         0, // pool: no keep-alive goroutine
		PreviewLen:           400,
		SeenRetention:        90 * 24 * time.Hour,
		MaxPendingPerAccount: 200,
		Accounts:             []config.Account{acc},
	}
	for _, fn := range extra {
		if fn != nil {
			fn(cfg)
		}
	}
	return cfg
}

// newTestPoller wires a Poller structurally (Poller fields are private and
// app.New would create real Telegram/Discord notifiers).
func newTestPoller(t *testing.T, cfg *config.Config, store *state.Store, notifiers ...*fakeNotifier) *Poller {
	t.Helper()
	ns := make([]notify.Notifier, 0, len(notifiers))
	for _, n := range notifiers {
		ns = append(ns, n)
	}
	p := &Poller{
		cfg:       cfg,
		store:     store,
		pool:      mail.NewPool(0),
		notifiers: ns,
		firstRun:  map[string]bool{testAcc: true},
		metrics:   NewMetrics(),
		status:    map[string]*AccountStatus{testAcc: {}},
	}
	t.Cleanup(p.Close)
	return p
}

func pollOnce(t *testing.T, p *Poller) {
	t.Helper()
	p.pollAll(context.Background())
}

func mustGet(t *testing.T, store *state.Store) state.AccountState {
	t.Helper()
	st, found, err := store.Get(testAcc)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if !found {
		t.Fatal("account state not found")
	}
	return st
}

func pendingCount(t *testing.T, store *state.Store) int {
	t.Helper()
	n, err := store.PendingCount(testAcc)
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	return n
}

// makeDue re-arms a pending row so retryPending picks it up immediately.
func makeDue(t *testing.T, store *state.Store, uid uint32, failed []string) {
	t.Helper()
	err := store.UpdatePendingRetry(testAcc, uid, failed, 0, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("make due: %v", err)
	}
}

func hasSeenFlag(t *testing.T, acc config.Account, uid uint32) bool {
	t.Helper()
	client, err := mail.Dial(context.Background(), acc)
	if err != nil {
		t.Fatalf("dial for flag check: %v", err)
	}
	defer client.Close()
	if _, err := client.Select(acc.Mailbox, nil).Wait(); err != nil {
		t.Fatalf("select: %v", err)
	}
	buffers, err := client.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
		UID:   true,
		Flags: true,
	}).Collect()
	if err != nil {
		t.Fatalf("fetch flags: %v", err)
	}
	if len(buffers) != 1 {
		t.Fatalf("expected 1 message, got %d", len(buffers))
	}
	for _, fl := range buffers[0].Flags {
		if fl == imap.FlagSeen {
			return true
		}
	}
	return false
}

// TestMissedNotificationRegression is the headline M1 fix: when every
// notifier fails, the UID must NOT be silently lost. LastUID advances (the
// mailbox watermark is real), the message lands in the retry queue, and a
// later cycle delivers it exactly once.
func TestMissedNotificationRegression(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	fake := newFake("fake", true)
	acc := env.acc
	acc.NotifyExisting = true
	p := newTestPoller(t, testCfg(acc), store, fake)

	env.appendMail(t, rawMail("<m1@x>", "first"))

	pollOnce(t, p)

	if fake.sentCount() != 0 {
		t.Fatalf("failed notifier recorded sends: %d", fake.sentCount())
	}
	if n := pendingCount(t, store); n != 1 {
		t.Fatalf("pending after failed poll = %d, want 1", n)
	}
	// Nothing lost: the watermark advanced, so the queue owns redelivery.
	if st := mustGet(t, store); st.LastUID != 1 {
		t.Errorf("LastUID = %d, want 1 (must advance)", st.LastUID)
	}

	fake.setFail(false)
	makeDue(t, store, 1, []string{"fake"})
	pollOnce(t, p)

	if got := fake.uidList(); len(got) != 1 || got[0] != 1 {
		t.Errorf("retried UIDs = %v, want [1]", got)
	}
	if n := pendingCount(t, store); n != 0 {
		t.Errorf("pending after retry = %d, want 0", n)
	}
	dup, err := store.IsDuplicate(testAcc, "m1@x")
	if err != nil || !dup {
		t.Errorf("IsDuplicate after retry = %v, %v; want true, nil", dup, err)
	}

	st := p.Status()[testAcc]
	if st.LastPollSuccessUnix == 0 {
		t.Error("last poll success unix = 0 after successful poll")
	}
	if st.ConsecutiveFailures != 0 {
		t.Errorf("consecutive failures = %d, want 0", st.ConsecutiveFailures)
	}
}

// TestFirstRunBaselineNoLeak: with notify_existing off, an inbox that
// already has mail must only set the baseline — and later mail still gets
// notified with preview/subject/message-id parsed.
func TestFirstRunBaselineAndNotify(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	fake := newFake("fake", false)
	p := newTestPoller(t, testCfg(env.acc), store, fake)

	env.appendMail(t, rawMail("<old@x>", "old1"))
	env.appendMail(t, rawMail("<old2@x>", "old2"))

	pollOnce(t, p) // first run, notify_existing=false -> baseline only

	if fake.sentCount() != 0 {
		t.Fatalf("first run notified %d messages, want 0", fake.sentCount())
	}
	if st := mustGet(t, store); st.LastUID != 2 {
		t.Fatalf("baseline LastUID = %d, want 2", st.LastUID)
	}

	env.appendMail(t, rawMail("<new@x>", "arrival"))
	pollOnce(t, p)

	if fake.sentCount() != 1 {
		t.Fatalf("sent %d, want 1", fake.sentCount())
	}
	fake.mu.Lock()
	got := fake.sent[0]
	fake.mu.Unlock()
	if got.UID != 3 {
		t.Errorf("UID = %d, want 3", got.UID)
	}
	if got.MessageID != "new@x" {
		t.Errorf("MessageID = %q", got.MessageID)
	}
	if got.Subject != "arrival" {
		t.Errorf("Subject = %q, want arrival", got.Subject)
	}
	if !strings.Contains(got.Preview, "Body for arrival") {
		t.Errorf("Preview = %q, want body text", got.Preview)
	}
	if got.Account != testAcc {
		t.Errorf("Account = %q, want %q", got.Account, testAcc)
	}
	if st := mustGet(t, store); st.LastUID != 3 {
		t.Errorf("LastUID = %d, want 3", st.LastUID)
	}
}

// TestDuplicateMessageIDSkipped: a re-delivered Message-ID (forward loop,
// mailing list copy) must not re-notify, but the UID watermark advances.
func TestDuplicateMessageIDSkipped(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	fake := newFake("fake", false)
	acc := env.acc
	acc.NotifyExisting = true
	p := newTestPoller(t, testCfg(acc, nil), store, fake)

	env.appendMail(t, rawMail("<dup@x>", "one"))
	pollOnce(t, p)
	if fake.sentCount() != 1 {
		t.Fatalf("first delivery sent %d, want 1", fake.sentCount())
	}

	env.appendMail(t, rawMail("<dup@x>", "copy"))
	pollOnce(t, p)

	if fake.sentCount() != 1 {
		t.Errorf("duplicate was notified: %d sends", fake.sentCount())
	}
	if st := mustGet(t, store); st.LastUID != 2 {
		t.Errorf("LastUID = %d, want 2 (must still advance)", st.LastUID)
	}
	if n := pendingCount(t, store); n != 0 {
		t.Errorf("pending = %d, want 0", n)
	}
}

// TestPartialFailureRetriesOnlyFailedNotifier: with two notifiers where one
// fails, only the failing one owes redelivery; retrying must not double-send
// to the healthy notifier.
func TestPartialFailureRetriesOnlyFailedNotifier(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	a := newFake("good", false)
	b := newFake("bad", true)
	acc := env.acc
	acc.NotifyExisting = true
	p := newTestPoller(t, testCfg(acc, nil), store, a, b)

	env.appendMail(t, rawMail("<partial@x>", "mix"))
	pollOnce(t, p)

	if a.sentCount() != 1 {
		t.Fatalf("good notifier sent %d, want 1", a.sentCount())
	}
	if n := pendingCount(t, store); n != 1 {
		t.Fatalf("pending = %d, want 1", n)
	}
	due, err := store.DuePending(testAcc, time.Now().Add(time.Hour), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("DuePending = %v, %v; want one row", due, err)
	}
	if len(due[0].Failed) != 1 || due[0].Failed[0] != "bad" {
		t.Fatalf("failed set = %v, want [bad]", due[0].Failed)
	}

	b.setFail(false)
	makeDue(t, store, due[0].UID, due[0].Failed)
	pollOnce(t, p)

	if a.totalCalls() != 1 {
		t.Errorf("good notifier re-invoked during retry (calls=%d)", a.totalCalls())
	}
	if b.sentCount() != 1 {
		t.Errorf("bad notifier sent %d, want 1", b.sentCount())
	}
	if n := pendingCount(t, store); n != 0 {
		t.Errorf("pending = %d, want 0", n)
	}
}

// TestRetryBackoffGrowthAndCap: attempts double the delay from 30s and are
// clamped at one hour; rows not yet due are skipped entirely.
func TestRetryBackoffGrowthAndCap(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	fake := newFake("fake", true)
	p := newTestPoller(t, testCfg(env.acc), store, fake)

	// Seed one pending row, due immediately, first attempt about to fail.
	if err := store.EnqueuePending(state.Pending{
		Account: testAcc, UID: 7, MessageID: "<back@x>", Failed: []string{"fake"},
	}, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p.retryPending(context.Background(), env.acc)

	due, err := store.DuePending(testAcc, time.Now(), 10)
	if err != nil {
		t.Fatalf("DuePending: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("retry is immediately due again, want backoff")
	}
	due, err = store.DuePending(testAcc, time.Now().Add(pendingInitialDelay+5*time.Second), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("retry not due after initial delay: %v %v", due, err)
	}
	if due[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1", due[0].Attempts)
	}

	// 17th failure: 30s << 16 far exceeds an hour -> capped.
	if err := store.UpdatePendingRetry(testAcc, 7, []string{"fake"}, 17, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("force attempt 17: %v", err)
	}
	fake.setFail(true)
	p.retryPending(context.Background(), env.acc)

	now := time.Now()
	if due, _ := store.DuePending(testAcc, now.Add(30*time.Minute), 10); len(due) != 0 {
		t.Errorf("delay longer than cap 1h (due at +30m)")
	}
	due, err = store.DuePending(testAcc, now.Add(2*time.Hour), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("row should be due by +2h, got %v (%v)", due, err)
	}
	if delta := due[0].NextTry.Sub(now); delta > pendingMaxDelay+5*time.Second {
		t.Errorf("delay %v exceeds cap %v", delta, pendingMaxDelay)
	}
	if due[0].Attempts != 18 {
		t.Errorf("attempts = %d, want 18", due[0].Attempts)
	}
}

// TestPendingDroppedAfterAgeLimit: a row older than the 7-day limit is
// deleted and counted in mailer_notify_dropped_total, never sent.
func TestPendingDroppedAfterAgeLimit(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	fake := newFake("fake", false)
	p := newTestPoller(t, testCfg(env.acc), store, fake)

	err := store.EnqueuePending(state.Pending{
		Account: testAcc, UID: 42, MessageID: "<old@x>",
		Failed:  []string{"fake"},
		Created: time.Now().Add(-pendingAgeLimit - time.Hour),
	}, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	p.retryPending(context.Background(), env.acc)

	if n := pendingCount(t, store); n != 0 {
		t.Errorf("pending = %d, want 0 (expired row dropped)", n)
	}
	if fake.totalCalls() != 0 {
		t.Errorf("expired row was sent: %d calls", fake.totalCalls())
	}
	snap := p.metrics.Snapshot()
	if !strings.Contains(snap, `mailer_notify_dropped_total{account="test"} 1`) {
		t.Errorf("dropped counter missing from metrics:\n%s", snap)
	}
}

// TestPendingDroppedWhenNotifiersGone: if every notifier the row still owes
// has been removed from config, the row leaves the queue without a send.
func TestPendingDroppedWhenNotifiersGone(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	fake := newFake("present", false)
	p := newTestPoller(t, testCfg(env.acc), store, fake)

	err := store.EnqueuePending(state.Pending{
		Account: testAcc, UID: 9, MessageID: "<gone@x>",
		Failed: []string{"removed"},
	}, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	p.retryPending(context.Background(), env.acc)

	if n := pendingCount(t, store); n != 0 {
		t.Errorf("pending = %d, want 0", n)
	}
	if fake.totalCalls() != 0 {
		t.Errorf("unrelated notifier was invoked: %d", fake.totalCalls())
	}
}

// TestMarkSeenOnSuccessfulDelivery: mark_seen stores \Seen on the server
// after delivery succeeds.
func TestMarkSeenOnSuccessfulDelivery(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	fake := newFake("fake", false)
	acc := env.acc
	acc.NotifyExisting = true
	acc.MarkSeen = true
	p := newTestPoller(t, testCfg(acc, nil), store, fake)

	env.appendMail(t, rawMail("<seen@x>", "seen"))
	pollOnce(t, p)

	if fake.sentCount() != 1 {
		t.Fatalf("sent %d, want 1", fake.sentCount())
	}
	if !hasSeenFlag(t, acc, 1) {
		t.Error("message 1 lacks \\Seen after successful delivery")
	}
}

// TestMarkSeenAfterRetry: the queue path marks seen only once the pending
// retry succeeds.
func TestMarkSeenAfterRetry(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	fake := newFake("fake", true)
	acc := env.acc
	acc.NotifyExisting = true
	acc.MarkSeen = true
	p := newTestPoller(t, testCfg(acc, nil), store, fake)

	env.appendMail(t, rawMail("<reseen@x>", "reseen"))
	pollOnce(t, p)

	if hasSeenFlag(t, acc, 1) {
		t.Fatal("message marked seen despite failed delivery")
	}

	fake.setFail(false)
	makeDue(t, store, 1, []string{"fake"})
	pollOnce(t, p)

	if !hasSeenFlag(t, acc, 1) {
		t.Error("message not marked seen after successful retry")
	}
	if n := pendingCount(t, store); n != 0 {
		t.Errorf("pending = %d, want 0", n)
	}
}

// TestUIDValidityReset: deleting and recreating the mailbox changes
// UIDVALIDITY; the poller must reset the baseline without re-notifying.
func TestUIDValidityReset(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	fake := newFake("fake", false)
	p := newTestPoller(t, testCfg(env.acc), store, fake)

	env.appendMail(t, rawMail("<v1@x>", "v1"))
	pollOnce(t, p) // baseline LastUID=1, UIDValidity=1

	// Recreate INBOX: imapmemserver bumps UIDVALIDITY on every Create.
	if err := env.user.Delete("INBOX"); err != nil {
		t.Fatalf("delete inbox: %v", err)
	}
	if err := env.user.Create("INBOX", nil); err != nil {
		t.Fatalf("create inbox: %v", err)
	}
	env.appendMail(t, rawMail("<v2@x>", "v2"))

	pollOnce(t, p) // detects mismatch -> resets baseline, notifies nothing

	if fake.sentCount() != 0 {
		t.Errorf("notified during UIDVALIDITY reset: %d sends", fake.sentCount())
	}
	if st := mustGet(t, store); st.UIDValidity == 1 {
		t.Fatalf("UIDValidity = %d, want new value stored", st.UIDValidity)
	}

	pollOnce(t, p) // now fetches v2 under the new validity

	if fake.sentCount() != 1 {
		t.Errorf("after reset sent %d, want 1", fake.sentCount())
	}
	st := mustGet(t, store)
	if st.LastUID != 1 {
		t.Errorf("LastUID = %d after reset+poll, want 1", st.LastUID)
	}
}

// TestStatusReflectsPendingAndPoll: /status summary is populated from the
// queue and poll bookkeeping.
func TestStatusReflectsPendingAndPoll(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	fake := newFake("fake", true)
	acc := env.acc
	acc.NotifyExisting = true
	p := newTestPoller(t, testCfg(acc, nil), store, fake)

	env.appendMail(t, rawMail("<st@x>", "status"))
	pollOnce(t, p)

	st := p.Status()[testAcc]
	if st.PendingNotifications != 1 {
		t.Errorf("pending = %d, want 1", st.PendingNotifications)
	}
	if st.LastPollDurationSec <= 0 {
		t.Errorf("duration = %v, want > 0", st.LastPollDurationSec)
	}
	// A failed notification is NOT a failed poll (queue owns recovery).
	if st.ConsecutiveFailures != 0 {
		t.Errorf("consecutive failures = %d, want 0", st.ConsecutiveFailures)
	}
}

// TestPoolWorkSerializationAndWatch covers the M1 connection-pool split:
// the work connection is exclusive, and the watch connection is dedicated
// and reused across acquisitions.
func TestPoolWorkSerializationAndWatch(t *testing.T) {
	env := startMemEnv(t)
	acc := env.acc
	pool := mail.NewPool(0)
	defer pool.Close()

	ctx := context.Background()
	wc1, err := pool.AcquireWork(ctx, acc)
	if err != nil {
		t.Fatalf("first AcquireWork: %v", err)
	}
	client1 := wc1.Client() // Release() nils the WorkConn, grab it now

	// While wc1 is held, a borrower must block. With ctx cancelled the
	// wait aborts instead of hanging the poll cycle.
	quick, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, err := pool.AcquireWork(quick, acc); err == nil {
		t.Fatal("AcquireWork succeeded while work conn was held")
	}

	wc1.Release()

	// After release the same client is reused (validated with NOOP).
	wc2, err := pool.AcquireWork(ctx, acc)
	if err != nil {
		t.Fatalf("second AcquireWork after release: %v", err)
	}
	if wc2.Client() != client1 {
		t.Error("work connection was not pooled/reused")
	}
	wc2.Release()

	// Watch is a separate connection and is reused between pings.
	w1 := pool.Watch(acc)
	if w1 == nil {
		t.Fatal("Watch returned nil")
	}
	w2 := pool.Watch(acc)
	if w2 != w1 {
		t.Error("Watch dialled a second connection instead of reusing")
	}
	if w1 == client1 {
		t.Error("Watch returned the work connection")
	}

	// Close is quiet and idempotent (old noise/panic regression).
	pool.Close()
	pool.Close()
}

// TestNoopKeepsWatchAlive pings with a keep-alive ticker and checks the
// watch connection survives (re)dialling transparently.
func TestNoopKeepsWatchAlive(t *testing.T) {
	env := startMemEnv(t)
	acc := env.acc
	pool := mail.NewPool(50 * time.Millisecond)
	defer pool.Close()

	if w := pool.Watch(acc); w == nil {
		t.Fatal("Watch nil")
	}
	time.Sleep(250 * time.Millisecond)
	if w := pool.Watch(acc); w == nil {
		t.Fatal("Watch nil after keep-alive rounds")
	}
}
