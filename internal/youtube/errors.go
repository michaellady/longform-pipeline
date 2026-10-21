package youtube

import (
	"context"
	"errors"
	"net"

	"google.golang.org/api/googleapi"
)

// IsTemporary reports whether an upload error is worth retrying later (exit
// 75): network failures, timeouts, 5xx, 429, and quota exhaustion. Anything
// else (bad request, auth, forbidden) needs a human.
func IsTemporary(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		if ge.Code >= 500 || ge.Code == 429 {
			return true
		}
		if ge.Code == 403 {
			for _, e := range ge.Errors {
				switch e.Reason {
				case "quotaExceeded", "dailyLimitExceeded", "rateLimitExceeded", "userRateLimitExceeded", "uploadLimitExceeded":
					return true
				}
			}
		}
	}
	return false
}
