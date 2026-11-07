package notify

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
	// Notifier is the notifier name that was throttled.
	Notifier string
	// RetryAfter is how long the platform asked us to wait. It is always
	// positive; when the response carried no usable hint, defaultRetryAfter is
	// used rather than no delay at all.
	RetryAfter time.Duration
	// StatusCode is the HTTP status (429 in practice).
	StatusCode int
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s: rate limited (status %d), retry after %s",
		e.Notifier, e.StatusCode, e.RetryAfter)
}

// defaultRetryAfter is used when a 429 arrives without a usable hint. One
// second is deliberately short: it is only a floor to avoid an immediate
// retry storm, not an attempt to guess the platform's real window.
const defaultRetryAfter = time.Second

// maxRetryAfter caps a hostile or buggy hint (a skewed clock could otherwise
// park the queue for years). The pending queue drops items after 7 days, so
// anything beyond an hour is retried by the normal backoff path instead.
const maxRetryAfter = time.Hour

// newRateLimitError builds a RateLimitError from an HTTP response. header is
// consulted first (both platforms send Retry-After on 429), then the body
// value, then a conservative default. Discord sends fractional seconds, so
// values are parsed as floats.
func newRateLimitError(notifier string, resp *http.Response, bodySeconds float64) *RateLimitError {
	wait := parseRetryAfterSeconds(resp.Header.Get("Retry-After"))
	if wait <= 0 && bodySeconds > 0 {
		wait = secondsToDuration(bodySeconds)
	}
	if wait <= 0 {
		wait = defaultRetryAfter
	}
	if wait > maxRetryAfter {
		wait = maxRetryAfter
	}
	return &RateLimitError{
		Notifier:   notifier,
		RetryAfter: wait,
		StatusCode: resp.StatusCode,
	}
}

// parseRetryAfterSeconds reads the Retry-After header. The HTTP spec allows
// either delta-seconds or an HTTP-date; both forms appear in the wild.
func parseRetryAfterSeconds(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return secondsToDuration(secs)
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

// secondsToDuration converts a float seconds value, rounding up so we never
// wake up a hair before the window closes. Values beyond maxRetryAfter are
// clamped rather than converted: secs * 1e9 overflows int64 at roughly 292
// years, and an overflowed (negative) duration would read as "no hint" and
// silently fall back to the default.
func secondsToDuration(secs float64) time.Duration {
	if secs <= 0 || math.IsNaN(secs) || math.IsInf(secs, 0) {
		return 0
	}
	if secs >= maxRetryAfter.Seconds() {
		return maxRetryAfter
	}
	return time.Duration(math.Ceil(secs * float64(time.Second)))
}
