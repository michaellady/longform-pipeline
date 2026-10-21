package opusclip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// UploadLink is the response to POST /upload-links.
type UploadLink struct {
	URL         string          `json:"url"`
	UploadID    string          `json:"uploadId"`
	DNSURL      string          `json:"dnsUrl,omitempty"`
	UseAmount   json.Number     `json:"useAmount,omitempty"`
	TotalAmount json.Number     `json:"totalAmount,omitempty"`
	Raw         json.RawMessage `json:"-"`
}

// CreateUploadLink is step 1 of the 4-step upload.
func (c *Client) CreateUploadLink(ctx context.Context) (*UploadLink, error) {
	raw, err := c.post(ctx, "/upload-links", map[string]any{"video": map[string]any{"usecase": "LocalUpload"}})
	if err != nil {
		return nil, err
	}
	var ul UploadLink
	if err := json.Unmarshal(unwrapData(raw), &ul); err != nil {
		return nil, fmt.Errorf("opusclip: decode upload link: %w", err)
	}
	ul.Raw = raw
	if ul.URL == "" || ul.UploadID == "" {
		return nil, fmt.Errorf("opusclip: upload link response missing url/uploadId: %s", snippet(raw, 256))
	}
	return &ul, nil
}

// StartResumableSession is step 2: POST to the signed GCS url with
// x-goog-resumable: start and an empty body; the session URL comes back in
// Location. No Authorization header is sent to the storage host.
func (c *Client) StartResumableSession(ctx context.Context, signedURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, signedURL, http.NoBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("x-goog-resumable", "start")
	req.Header.Set("Content-Length", "0")
	req.ContentLength = 0
	start := c.now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("opusclip: start resumable session: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	c.logf("opusclip: POST <upload-url> (resumable start) -> %d (%s)", resp.StatusCode, c.now().Sub(start).Round(time.Millisecond))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", &APIError{Status: resp.StatusCode, Method: "POST", Path: "<upload-url>", Body: snippet(body, 512)}
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("opusclip: resumable session response had no Location header")
	}
	return loc, nil
}

// UploadStatus asks the session how many bytes it has persisted
// (Content-Range: bytes */size). committed is the next byte offset to send
// (0 when nothing persisted); done is true when the object is complete.
func (c *Client) UploadStatus(ctx context.Context, sessionURL string, size int64) (committed int64, done bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, http.NoBody)
	if err != nil {
		return 0, false, err
	}
	req.ContentLength = 0
	req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("opusclip: query upload status: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	c.logf("opusclip: PUT <session-url> (status query) -> %d range=%q", resp.StatusCode, resp.Header.Get("Range"))
	switch {
	case resp.StatusCode == 200 || resp.StatusCode == 201:
		return size, true, nil
	case resp.StatusCode == 308:
		r := resp.Header.Get("Range") // "bytes=0-N"
		if r == "" {
			return 0, false, nil
		}
		r = strings.TrimPrefix(r, "bytes=")
		if i := strings.LastIndex(r, "-"); i >= 0 {
			n, perr := strconv.ParseInt(r[i+1:], 10, 64)
			if perr != nil {
				return 0, false, fmt.Errorf("opusclip: bad Range header %q", resp.Header.Get("Range"))
			}
			return n + 1, false, nil
		}
		return 0, false, fmt.Errorf("opusclip: bad Range header %q", resp.Header.Get("Range"))
	default:
		return 0, false, &APIError{Status: resp.StatusCode, Method: "PUT", Path: "<session-url> (status)"}
	}
}

// PutFile is step 3: stream the file from disk into the resumable session.
// The whole file is sent first; if the connection drops or the host answers
// 5xx, the session is queried and the remaining bytes are sent from the
// committed offset, up to MaxRetries times. Nothing is read into memory.
func (c *Client) PutFile(ctx context.Context, sessionURL, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	size := fi.Size()
	offset := int64(0)
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			d := c.backoff(attempt)
			c.logf("opusclip: upload retry %d/%d after %s (%v)", attempt, c.MaxRetries, d, lastErr)
			if err := c.sleep(ctx, d); err != nil {
				return err
			}
			committed, done, qerr := c.UploadStatus(ctx, sessionURL, size)
			if qerr != nil {
				lastErr = qerr
				var ae *APIError
				if errors.As(qerr, &ae) && !ae.Temporary() {
					return qerr
				}
				continue
			}
			if done {
				return nil
			}
			offset = committed
		}
		err := c.putRange(ctx, sessionURL, path, offset, size)
		if err == nil {
			return nil
		}
		lastErr = err
		var ae *APIError
		if errors.As(err, &ae) && !ae.Temporary() {
			return err
		}
		if ctx.Err() != nil {
			return err
		}
	}
	return lastErr
}

func (c *Client) putRange(ctx context.Context, sessionURL, path string, offset, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, f)
	if err != nil {
		return err
	}
	req.ContentLength = size - offset
	req.Header.Set("Content-Type", "application/octet-stream")
	if offset > 0 {
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, size-1, size))
	}
	// Uploads can legitimately take longer than the API timeout.
	h := *c.HTTP
	h.Timeout = 0
	start := c.now()
	resp, err := h.Do(req)
	if err != nil {
		c.logf("opusclip: PUT <session-url> (%d bytes from %d) -> transport error after %s: %v", size-offset, offset, c.now().Sub(start).Round(time.Millisecond), err)
		return fmt.Errorf("opusclip: upload bytes: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	c.logf("opusclip: PUT <session-url> (%d bytes from %d) -> %d (%s)", size-offset, offset, resp.StatusCode, c.now().Sub(start).Round(time.Millisecond))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Method: "PUT", Path: "<session-url>", Body: snippet(body, 512)}
	}
	return nil
}

// UploadFile runs steps 1-3 and returns the uploadId to use as videoUrl in
// CreateProject. Callers that need to persist the uploadId between steps
// (the longform upload command does) call the steps individually.
func (c *Client) UploadFile(ctx context.Context, path string) (string, error) {
	ul, err := c.CreateUploadLink(ctx)
	if err != nil {
		return "", err
	}
	sess, err := c.StartResumableSession(ctx, ul.URL)
	if err != nil {
		return "", err
	}
	if err := c.PutFile(ctx, sess, path); err != nil {
		return "", err
	}
	return ul.UploadID, nil
}
