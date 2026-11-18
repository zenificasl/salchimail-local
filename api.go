package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

type apiError struct {
	Status    int    `json:"-"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (e *apiError) Error() string {
	return fmt.Sprintf("SalchiMail API answered %d %s: %s (request_id %s)", e.Status, printable(e.Code), printable(e.Message), printable(e.RequestID))
}

// printable drops control characters, so an answer cannot rewrite the terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

// noRedirects keeps the key and the addresses on the URL the user chose: a
// redirect is reported as an error instead of being followed.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func hashOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:16])
}

type apiResult struct {
	Email   string `json:"email"`
	Verdict string `json:"verdict"`
	Code    string `json:"reason_code"`
	Reason  string `json:"reason"`
	Charged int    `json:"credits_charged"`
}

type batch struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Livemode bool   `json:"livemode"`
}

type client struct {
	base, key, lang string
	http            *http.Client
	sleep           func(time.Duration)
	poll, wait      time.Duration
	waited          time.Duration
	maxBatch        int
	testMode        bool
	st              *state
}

// checkAPIURL refuses to send the key in clear text: HTTPS, or HTTP only to
// this machine (tests).
func checkAPIURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("--api-url %q is not a URL", raw)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(raw)) {
		return fmt.Errorf("--api-url must use https (plain http is only accepted for localhost)")
	}
	return nil
}

// isLoopback reports whether the URL points at this machine.
func isLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		return ip.IsLoopback()
	}
	return u.Hostname() == "localhost"
}

// do sends one request and retries what the contract says is retryable: 5xx
// and network errors (POSTs carry an Idempotency-Key), 429 for the request
// rate, and 409 while the first attempt of the same key is still running.
func (c *client) do(method, path string, body any, idempotencyKey string, out any) error {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequest(method, c.base+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Accept-Language", c.lang)
		req.Header.Set("User-Agent", "salchimail-local/"+version)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		pause := time.Duration(attempt) * 2 * time.Second
		resp, err := c.http.Do(req)
		if err == nil {
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
			resp.Body.Close()
			if readErr == nil && resp.StatusCode < 300 {
				if out == nil {
					return nil
				}
				return json.Unmarshal(data, out)
			}
			ae := &apiError{Code: "unreadable_response", Message: http.StatusText(resp.StatusCode)}
			var envelope struct {
				Error *apiError `json:"error"`
			}
			if json.Unmarshal(data, &envelope) == nil && envelope.Error != nil {
				ae = envelope.Error
			}
			ae.Status = resp.StatusCode
			err = ae
			switch {
			case resp.StatusCode == 429 && (ae.Code == "rate_limit_exceeded" || ae.Code == "concurrent_batches_exceeded"):
				seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
				pause = time.Duration(min(max(seconds, 1), 300)) * time.Second
			case resp.StatusCode == 409 && ae.Code == "idempotency_in_progress":
			case resp.StatusCode >= 500:
			default:
				return err // 401, 402, 403, daily budgets: waiting does not help
			}
		}
		if attempt >= 5 {
			return err
		}
		c.sleep(pause)
	}
}

func isStatus(err error, status int) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == status
}

// preflight reads the plan limits and the mailbox balance before anything is sent.
func (c *client) preflight() (remaining int, err error) {
	var account struct {
		Livemode bool `json:"livemode"`
		Limits   struct {
			MaxBatchSize int      `json:"max_batch_size"`
			Modes        []string `json:"modes"`
		} `json:"limits"`
	}
	if err := c.do("GET", "/account", nil, "", &account); err != nil {
		return 0, err
	}
	if !slices.Contains(account.Limits.Modes, "mailbox") {
		return 0, errors.New("this account's plan does not allow mailbox mode")
	}
	c.testMode = !account.Livemode
	c.maxBatch = min(account.Limits.MaxBatchSize, 10000) // 10,000 is the JSON limit of POST /batches
	if c.maxBatch <= 0 {
		c.maxBatch = 100
	}
	var usage struct {
		Credits struct {
			Mailbox struct {
				Remaining int `json:"remaining"`
			} `json:"mailbox"`
		} `json:"credits"`
	}
	if err := c.do("GET", "/usage", nil, "", &usage); err != nil {
		return 0, err
	}
	return usage.Credits.Mailbox.Remaining, nil
}

// verify runs the addresses through mailbox batches of the plan's size, one
// after another. pending is true when the wait ran out before a batch
// finished: the state file lets the next run pick it up.
// shortcut: batches run one at a time, upgrade to max_concurrent_batches if
// lists far above max_batch_size become common.
func (c *client) verify(emails []string) (results map[string]apiResult, pending bool, err error) {
	results = map[string]apiResult{}
	for start := 0; start < len(emails); start += c.maxBatch {
		done, err := c.runBatch(emails[start:min(start+c.maxBatch, len(emails))], results)
		if err != nil || !done {
			return results, err == nil, err
		}
	}
	return results, false, nil
}

func (c *client) runBatch(emails []string, results map[string]apiResult) (done bool, err error) {
	// The mode is part of the hash: the server keeps idempotency keys per
	// account, not per mode, so a live run must not replay a test batch.
	hash := hashOf(strconv.FormatBool(c.testMode), strings.Join(emails, "\n"))
	entry := c.st.entry(hash)
	var b batch
	if entry.Batch != "" {
		err = c.do("GET", "/batches/"+entry.Batch, nil, "", &b)
		if isStatus(err, 404) { // deleted or expired: start over
			entry.Batch, err = "", nil
			entry.Attempt++
		}
		if err != nil {
			return false, err
		}
	}
	if entry.Batch == "" {
		// The key is the content: repeating the command replays the same batch instead of paying twice.
		key := fmt.Sprintf("salchimail-local-%s-%d", hash, entry.Attempt)
		body := map[string]any{"emails": emails, "mode": "mailbox", "name": "salchimail-local"}
		if err = c.do("POST", "/batches", body, key, &b); err != nil {
			return false, err
		}
		entry.Batch = b.ID
		if err = c.st.save(); err != nil {
			return false, err
		}
	}
	for b.Status == "queued" || b.Status == "processing" {
		if c.waited >= c.wait {
			return false, nil
		}
		c.sleep(c.poll)
		c.waited += c.poll
		if err = c.do("GET", "/batches/"+b.ID, nil, "", &b); err != nil {
			return false, err
		}
	}
	if b.Status == "completed" {
		c.st.finished = append(c.st.finished, hash)
	} else {
		// failed (credits returned) or canceled (partial results): the next run needs a new batch.
		entry.Batch = ""
		entry.Attempt++
		if err = c.st.save(); err != nil {
			return false, err
		}
		if b.Status == "failed" {
			return false, fmt.Errorf("batch %s failed on the server and its credits were returned; run the command again", b.ID)
		}
	}
	canceled := b.Status != "completed"
	for cursor := ""; ; {
		var page struct {
			Data       []apiResult `json:"data"`
			HasMore    bool        `json:"has_more"`
			NextCursor *string     `json:"next_cursor"`
		}
		path := "/batches/" + b.ID + "/results?limit=1000"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		if err = c.do("GET", path, nil, "", &page); err != nil {
			return false, err
		}
		for _, r := range page.Data {
			results[r.Email] = r
		}
		if !page.HasMore || page.NextCursor == nil {
			if canceled {
				return true, fmt.Errorf("batch %s was canceled before it finished; run the command again to check the rest", b.ID)
			}
			return true, nil
		}
		cursor = *page.NextCursor
	}
}

// state remembers which batch belongs to which set of addresses while a run
// is unfinished, so an interrupted run resumes instead of paying again. It
// holds content hashes, batch ids and the run's credit allowance, never addresses.
type stateEntry struct {
	Batch   string `json:"batch"`
	Attempt int    `json:"attempt"`
}

type state struct {
	path     string
	Plan     string                 `json:"plan"`    // hash of the run's candidates, budget and mode
	Left     int                    `json:"left"`    // credits the run may spend, fixed when it first started
	Batches  map[string]*stateEntry `json:"batches"` // by content hash
	finished []string
}

func loadState(path string) (*state, error) {
	s := &state{path: path}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(raw, s); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if s.Batches == nil {
		s.Batches = map[string]*stateEntry{}
	}
	return s, nil
}

func (s *state) entry(hash string) *stateEntry {
	if s.Batches[hash] == nil {
		s.Batches[hash] = &stateEntry{}
	}
	return s.Batches[hash]
}

func (s *state) save() error {
	for hash, e := range s.Batches {
		if e.Batch == "" && e.Attempt == 0 {
			delete(s.Batches, hash)
		}
	}
	if len(s.Batches) == 0 {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(s.path, append(raw, '\n'), 0o600)
}

// complete forgets the batches of a run that collected everything.
func (s *state) complete() error {
	for _, hash := range s.finished {
		delete(s.Batches, hash)
	}
	return s.save()
}
