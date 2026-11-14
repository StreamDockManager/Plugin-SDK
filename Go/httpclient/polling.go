package httpclient

import (
	"crypto/rand"
	"encoding/binary"
	"github.com/StreamDockManager/Plugin-SDK/Go/polling"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type PollingFeedback struct {
	Stale     bool   `json:"stale,omitempty"`
	NextRetry string `json:"next_retry,omitempty"`
	LastValue string `json:"last_successful_value,omitempty"`
	Failure   string `json:"failure,omitempty"`
}

// Parse bounded Retry-After seconds or a supported HTTP date
func ParseRetryAfter(value string, now time.Time) time.Time {
	value = strings.Trim(value, " \t\r\n")
	if value == "" {
		return time.Time{}
	}
	digits := true
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			digits = false
			break
		}
	}
	if digits {
		digits := strings.TrimLeft(value, "0")
		if digits == "" {
			digits = "0"
		}
		seconds, err := strconv.ParseUint(digits, 10, 64)
		if err != nil || seconds > uint64(maximumRetryAfter/time.Second) {
			return now.Add(maximumRetryAfter)
		}
		return now.Add(time.Duration(seconds) * time.Second)
	}
	canonical := canonicalHTTPDate(value)
	if at, err := http.ParseTime(canonical); err == nil && at.Format(http.TimeFormat) == canonical {
		return now.Add(min(maximumRetryAfter, max(time.Duration(0), at.Sub(now))))
	}
	return time.Time{}
}

// The date contract follows the strict HTTP parser shared with Rust: exact tokens,
// matching weekday, years 1970..9999, and RFC850's 00..69/70..99 century split
var httpDatePattern = regexp.MustCompile(`^((?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)), ([0-9]{2}) ((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)) ([0-9]{4}) ([0-9]{2}:[0-9]{2}:[0-9]{2}) GMT$`)
var rfc850DatePattern = regexp.MustCompile(`^((?:Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday)), ([0-9]{2})-((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec))-([0-9]{2}) ([0-9]{2}:[0-9]{2}:[0-9]{2}) GMT$`)
var asctimeDatePattern = regexp.MustCompile(`^((?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)) ((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)) ([ 0-9][0-9]) ([0-9]{2}:[0-9]{2}:[0-9]{2}) ([0-9]{4})$`)

// Normalize a supported HTTP date spelling for strict parsing
func canonicalHTTPDate(value string) string {
	parts := httpDatePattern.FindStringSubmatch(value)
	if parts == nil {
		parts = rfc850DatePattern.FindStringSubmatch(value)
		if parts != nil {
			year, _ := strconv.Atoi(parts[4])
			if year < 70 {
				year += 2000
			} else {
				year += 1900
			}
			parts[4] = strconv.Itoa(year)
		} else if old := asctimeDatePattern.FindStringSubmatch(value); old != nil {
			day := strings.TrimSpace(old[3])
			if len(day) == 1 {
				day = "0" + day
			}
			parts = []string{"", old[1], day, old[2], old[5], old[4]}
		}
	}
	if parts == nil || parts[4] < "1970" {
		return ""
	}
	return parts[1][:3] + ", " + parts[2] + " " + parts[3] + " " + parts[4] + " " + parts[5] + " GMT"
}

// Largest whole-second duration representable by Go; shared by all SDKs
const maximumRetryAfter = 9223372036 * time.Second

// Choose a jitter factor for polling backoff
func PollJitter() float64 {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return 0.5
	}
	return float64(binary.LittleEndian.Uint64(bytes[:])>>11) / float64(uint64(1)<<53)
}

// Interval enables automatic polling only for read-only methods
func Interval(method string, seconds float64) time.Duration {
	method = strings.ToUpper(method)
	if (method != "GET" && method != "HEAD") || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 1 || seconds > 86400 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// Compute bounded failure backoff with caller-supplied jitter
func RetryDelay(interval time.Duration, failures int, jitter float64) time.Duration {
	return polling.RetryDelay(interval, failures, jitter)
}

// Format the remaining retry delay for display
func RetryLabel(next, now time.Time) string {
	next = next.UTC()
	now = now.UTC()
	if next.Year() != now.Year() {
		return "Retry " + next.Format("2006-01")
	}
	if next.YearDay() != now.YearDay() {
		return "Retry " + next.Format("01-02")
	}
	return "Retry " + next.Format("15:04Z")
}

const MaximumPollBackoff = 15 * time.Minute
