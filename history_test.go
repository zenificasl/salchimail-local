package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func ev(kind string, daysAgo int) Event {
	return Event{Email: "a@x.example", Kind: kind, At: now.AddDate(0, 0, -daysAgo)}
}

func TestDecideOwn(t *testing.T) {
	cases := []struct {
		name   string
		events []Event
		action string // "" = own history decides nothing
		code   string
	}{
		{"a recent reply", []Event{ev("reply", 30)}, "send", "own_reply"},
		{"a recent hard bounce", []Event{ev("hard_bounce", 100)}, "do_not_send", "own_hard_bounce"},
		{"a reply after the bounce wins", []Event{ev("hard_bounce", 200), ev("reply", 60)}, "send", "own_reply"},
		{"a bounce after the delivery wins", []Event{ev("delivered", 60), ev("hard_bounce", 10)}, "do_not_send", "own_hard_bounce"},
		{"a click counts", []Event{ev("click", 100)}, "send", "own_click"},
		{"an open counts for 90 days", []Event{ev("open", 80)}, "send", "own_open"},
		{"an old open does not", []Event{ev("open", 100)}, "", ""},
		{"an expired delivery does not count", []Event{ev("delivered", 400)}, "", ""},
		{"an expired hard bounce does not count", []Event{ev("hard_bounce", 400)}, "", ""},
		{"a policy bounce proves nothing", []Event{ev("policy_bounce", 5)}, "", ""},
		{"an auto-reply proves nothing", []Event{ev("auto_reply", 5)}, "", ""},
		{"an older delivery survives a policy bounce", []Event{ev("delivered", 30), ev("policy_bounce", 5)}, "send", "own_delivered"},
		{"a later soft bounce cancels the delivery", []Event{ev("delivered", 60), ev("soft_bounce", 10)}, "", ""},
		{"a delivery after the soft bounce wins", []Event{ev("soft_bounce", 10), ev("delivered", 5)}, "send", "own_delivered"},
		{"a future date is ignored", []Event{ev("reply", -3)}, "", ""},
		{"sign-up outcomes are not mailbox evidence", []Event{ev("payment", 5), ev("signup", 9)}, "", ""},
		{"no history", nil, "", ""},
	}
	for _, c := range cases {
		d, ok := decideOwn(c.events, now, translate("en"))
		if ok != (c.action != "") || d.Action != c.action || d.Code != c.code {
			t.Errorf("%s: got %q/%q (decided %v), want %q/%q", c.name, d.Action, d.Code, ok, c.action, c.code)
		}
		if ok && (d.Source != "own_history" || d.API != "no" || !strings.Contains(d.Reason, "2026-")) {
			t.Errorf("%s: wrong provenance or reason without a date: %+v", c.name, d)
		}
	}
}

func TestNormalizeAndLists(t *testing.T) {
	for input, want := range map[string]bool{
		" Ana@Example.COM ": true, "a.b+tag@sub.example.org": true,
		"not-an-email": false, "a@b": false, "a@-x.com": false, "Ana <ana@example.com>": false, "a@exa mple.com": false, "": false,
	} {
		email, domain, ok := normalize(input)
		if ok != want {
			t.Errorf("normalize(%q) ok = %v, want %v", input, ok, want)
		}
		if ok && (email != strings.ToLower(strings.TrimSpace(input)) || !strings.HasSuffix(email, "@"+domain)) {
			t.Errorf("normalize(%q) = %q, %q", input, email, domain)
		}
	}
	if !nonASCII("josé@example.com") || nonASCII("jose@example.com") {
		t.Error("nonASCII")
	}
	if len(disposable) < 10000 {
		t.Errorf("embedded disposable list has %d domains", len(disposable))
	}
	if !listed("mailinator.com", disposable) || !listed("inbox.mailinator.com", disposable) || listed("example.com", disposable) {
		t.Error("disposable list lookup")
	}
	if !listed("wanadoo.es", legacy) || !listed("gmail.com", free) || listed("example.com", free) {
		t.Error("legacy or free list lookup")
	}
}

// The embedded list must be the engine's, so a local answer equals the API's.
func TestDisposableListMatchesTheEngine(t *testing.T) {
	engine, err := os.ReadFile(filepath.Join("..", "..", "internal", "validator", "disposable_domains.txt"))
	if err != nil {
		t.Skip("engine sources not present:", err)
	}
	if string(engine) != disposableList {
		t.Error("disposable_domains.txt differs from internal/validator: copy it again and update THIRD_PARTY_NOTICES.md")
	}
}

// fakeDNS answers from a table: domain → mx | implicit_mx | null_mx | temporary. Anything else does not exist.
type fakeDNS map[string]string

func (f fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	switch f[name] {
	case "mx":
		return []*net.MX{{Host: "mx." + name + ".", Pref: 10}}, nil
	case "null_mx":
		return []*net.MX{{Host: "."}}, nil
	case "temporary":
		return nil, &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f fakeDNS) LookupHost(_ context.Context, name string) ([]string, error) {
	if f[name] == "implicit_mx" {
		return []string{"192.0.2.1"}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func TestLookupDomain(t *testing.T) {
	dns := fakeDNS{"a.example": "mx", "b.example": "implicit_mx", "c.example": "null_mx", "d.example": "temporary"}
	want := map[string]string{"a.example": "mx", "b.example": "implicit_mx", "c.example": "null_mx", "d.example": "temporary", "e.example": "no_mail_host"}
	got := lookupDomains(dns, []string{"a.example", "b.example", "c.example", "d.example", "e.example"})
	for domain, status := range want {
		if got[domain] != status {
			t.Errorf("%s: got %q, want %q", domain, got[domain], status)
		}
	}
	// A resolver that fails must never turn a domain into "cannot receive mail".
	if deadDomain("temporary") != "" || deadDomain("mx") != "" || deadDomain("null_mx") != "domain_null_mx" || deadDomain("no_mail_host") != "domain_no_mail_host" {
		t.Error("deadDomain")
	}
}

func TestReadRecords(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cols, recs, err := readRecords(write("semi.csv", "\xef\xbb\xbfId;Email\n1;ana@example.com\n2;\"luis@example.org\"\n"))
	if err != nil || len(recs) != 2 || cols[1] != "email" || recs[1]["email"] != "luis@example.org" {
		t.Errorf("semicolon CSV with BOM: %v %v %v", cols, recs, err)
	}
	_, recs, err = readRecords(write("list.jsonl", "{\"Email\": \"ana@example.com\", \"paid\": true}\n\n\"luis@example.org\"\n"))
	if err != nil || len(recs) != 2 || recs[0]["email"] != "ana@example.com" || recs[0]["paid"] != "true" || recs[1]["email"] != "luis@example.org" {
		t.Errorf("JSONL: %v %v", recs, err)
	}
	if _, _, err = readRecords(write("nohead.csv", "ana@example.com\nluis@example.org\n")); err == nil || !strings.Contains(err.Error(), "header") {
		t.Errorf("a CSV without a header must be refused, got %v", err)
	}
	if _, err = readHistory(write("badkind.csv", "email,kind,at\nana@example.com,bounced,2026-01-01\n")); err == nil || !strings.Contains(err.Error(), "row 1") || !strings.Contains(err.Error(), "hard_bounce") {
		t.Errorf("an unknown kind must name the row and the valid kinds, got %v", err)
	}
	if _, err = readHistory(write("baddate.csv", "email,kind,at\nana@example.com,reply,yesterday\n")); err == nil {
		t.Error("an unreadable date must be refused")
	}
	if _, err = readHistory(write("nocol.csv", "email,type,at\nana@example.com,reply,2026-01-01\n")); err == nil || !strings.Contains(err.Error(), `"kind"`) {
		t.Errorf("a missing column must be named, got %v", err)
	}
	if csvCell("=1+1@example.com") != "'=1+1@example.com" || csvCell("ana@example.com") != "ana@example.com" {
		t.Error("csvCell")
	}
}
