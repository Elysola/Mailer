package core

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Notifier consumes envelopes. Implementations must be safe for concurrent
// Send calls.
type Notifier interface {
	Component
	Send(ctx context.Context, env Envelope) error
	// Capabilities tells the pipeline what the notifier can express, so
	// unsupported features degrade instead of failing.
	Capabilities() Capabilities
}

type Capabilities struct {
	// InteractiveButtons is true when the notifier can render actionable
	// buttons (Telegram inline keyboard, Discord components) and handle
	// their callbacks.
	InteractiveButtons bool
	// MaxBodyRunes is the platform's message length limit. The notifier
	// still truncates defensively; this is for the pipeline's observability.
	MaxBodyRunes int
}

// RateLimitError reports that a platform refused a send because we are going
// too fast, and says how long it wants us to wait.
//
// The distinction matters: an ordinary send failure is worth a quick
// exponential-backoff retry, but retrying a 429 on our own schedule actively
// makes things worse — Telegram answers a too-eager retry with a longer
// Retry-After, and Discord can escalate to a global limit that silences every
// channel. So the platform's number wins over our backoff, and the caller is
// expected to wait at least that long before touching the same notifier again.
type RateLimitError struct {
	// Notifier is the notifier instance name that was throttled.
	Notifier string
	// RetryAfter is how long the platform asked us to wait.
	RetryAfter time.Duration
	// StatusCode is the HTTP status (429 in practice).
	StatusCode int
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s: rate limited (status %d), retry after %s",
		e.Notifier, e.StatusCode, e.RetryAfter)
}

// DefaultRetryAfter is used when a 429 arrives without a usable hint. One
// second is deliberately short: it is only a floor to avoid an immediate
// retry storm, not an attempt to guess the platform's real window.
const DefaultRetryAfter = time.Second

// MaxRetryAfter caps a hostile or buggy hint (a skewed clock could otherwise
// park the queue for years). The pending queue drops items after 7 days, so
// anything beyond an hour is retried by the normal backoff path instead.
const MaxRetryAfter = time.Hour

// NewRateLimitError builds a RateLimitError from an HTTP response. The
// Retry-After header is consulted first (both platforms send it on 429), then
// the body value, then a conservative default. Discord sends fractional
// seconds, so values are parsed as floats.
func NewRateLimitError(notifier string, resp *http.Response, bodySeconds float64) *RateLimitError {
	wait := ParseRetryAfterSeconds(resp.Header.Get("Retry-After"))
	if wait <= 0 && bodySeconds > 0 {
		wait = SecondsToDuration(bodySeconds)
	}
	if wait <= 0 {
		wait = DefaultRetryAfter
	}
	if wait > MaxRetryAfter {
		wait = MaxRetryAfter
	}
	return &RateLimitError{
		Notifier:   notifier,
		RetryAfter: wait,
		StatusCode: resp.StatusCode,
	}
}

// ParseRetryAfterSeconds reads the Retry-After header. The HTTP spec allows
// either delta-seconds or an HTTP-date; both forms appear in the wild.
func ParseRetryAfterSeconds(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return SecondsToDuration(secs)
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

// SecondsToDuration converts a float seconds value, rounding up so we never
// wake up a hair before the window closes. Values beyond MaxRetryAfter are
// clamped rather than converted: secs * 1e9 overflows int64 at roughly 292
// years, and an overflowed (negative) duration would read as "no hint" and
// silently fall back to the default.
func SecondsToDuration(secs float64) time.Duration {
	if secs <= 0 || math.IsNaN(secs) || math.IsInf(secs, 0) {
		return 0
	}
	if secs >= MaxRetryAfter.Seconds() {
		return MaxRetryAfter
	}
	return time.Duration(math.Ceil(secs * float64(time.Second)))
}
