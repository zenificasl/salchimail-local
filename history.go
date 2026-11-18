package main

import (
	"fmt"
	"strings"
	"time"
)

// Event is one thing the customer observed about an address, already
// normalised to email,kind,at by the customer or their agent.
type Event struct {
	Email string
	Kind  string
	At    time.Time
}

// Days each kind of own evidence stays usable. Starting values, not yet
// calibrated against real outcomes.
var maxAgeDays = map[string]int{
	"reply":       180,
	"click":       180,
	"open":        90,
	"delivered":   90,
	"soft_bounce": 90,
	"hard_bounce": 365,
}

// Accepted but never evidence about the mailbox: a policy rejection (5.7.x) or
// an auto-reply does not say whether it exists, and the sign-up outcomes are
// only read by `rehearse`.
var otherKinds = map[string]bool{
	"policy_bounce": true, "auto_reply": true,
	"signup": true, "payment": true, "activation": true, "fraud": true,
}

func knownKind(kind string) bool {
	_, ok := maxAgeDays[kind]
	return ok || otherKinds[kind]
}

func kindList() string {
	kinds := []string{"reply", "click", "open", "delivered", "soft_bounce", "hard_bounce", "policy_bounce", "auto_reply", "signup", "payment", "activation", "fraud"}
	return strings.Join(kinds, ", ")
}

// ownEvidence returns the most recent event that has not expired, or nil.
// Events dated in the future are ignored.
func ownEvidence(events []Event, now time.Time) *Event {
	var best *Event
	for i := range events {
		e := &events[i]
		days, ok := maxAgeDays[e.Kind]
		if !ok || e.At.After(now) || now.Sub(e.At) > time.Duration(days)*24*time.Hour {
			continue
		}
		if best == nil || e.At.After(best.At) {
			best = e
		}
	}
	return best
}

// decideOwn turns the customer's own evidence into a decision. ok is false
// when there is none, or when the latest is a soft bounce: that says the last
// attempt failed for now, which neither proves nor disproves the mailbox.
func decideOwn(events []Event, now time.Time, t translator) (d decision, ok bool) {
	e := ownEvidence(events, now)
	if e == nil || e.Kind == "soft_bounce" {
		return d, false
	}
	action := "send"
	if e.Kind == "hard_bounce" {
		action = "do_not_send"
	}
	code := "own_" + e.Kind
	return decision{Action: action, Source: "own_history", API: "no", Code: code, Reason: fmt.Sprintf(t(code), e.At.Format("2006-01-02"))}, true
}

var timeLayouts = []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}

func parseTime(s string) (time.Time, error) {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date (use 2026-11-17 or 2026-11-17T09:00:00Z)", s)
}

// readHistory loads email,kind,at events grouped by normalised address.
func readHistory(path string) (map[string][]Event, error) {
	byEmail := map[string][]Event{}
	if path == "" {
		return byEmail, nil
	}
	cols, recs, err := readRecords(path)
	if err != nil {
		return nil, err
	}
	for _, want := range []string{"email", "kind", "at"} {
		if _, err := pickColumn(path, cols, want); err != nil {
			return nil, err
		}
	}
	for i, r := range recs {
		kind := strings.ToLower(strings.TrimSpace(r["kind"]))
		if !knownKind(kind) {
			return nil, fmt.Errorf("%s row %d: unknown kind %q (use one of: %s)", path, i+1, kind, kindList())
		}
		at, err := parseTime(strings.TrimSpace(r["at"]))
		if err != nil {
			return nil, fmt.Errorf("%s row %d: %w", path, i+1, err)
		}
		email, _, _ := normalize(r["email"])
		byEmail[email] = append(byEmail[email], Event{Email: email, Kind: kind, At: at})
	}
	return byEmail, nil
}
