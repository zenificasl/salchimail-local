package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// outcome is what happened to one address that only a mailbox probe could resolve.
type outcome struct {
	API      string // no | sent | pending | would_send | would_send_conditional
	Verdict  string // "" while not checked
	Code     string // reason_code of the API, or why it was not checked
	Reason   string // the API's own text, for addresses that were sent
	Inferred bool   // not sent: the answer for another address of the domain applies to it
}

type probeReport struct {
	Wave1, Wave2 []string // addresses sent, or that a dry run would send
	Charged      int
	Pending      bool
	TestMode     bool
}

func domainOf(email string) string {
	return email[strings.LastIndexByte(email, '@')+1:]
}

// planWaves picks one address per domain to go first. What its server answers
// decides whether sending the others would tell us anything.
func planWaves(candidates []string) (scouts []string, rest map[string][]string) {
	rest = map[string][]string{}
	seen := map[string]bool{}
	for _, email := range candidates {
		d := domainOf(email)
		if !seen[d] {
			seen[d] = true
			scouts = append(scouts, email)
		} else {
			rest[d] = append(rest[d], email)
		}
	}
	return scouts, rest
}

// domainWide reports whether an answer holds for every address of the domain,
// so the others need not be sent: an accept-all server, a domain-level fact,
// or a server that did not let us check. A verdict about that one mailbox
// (found, missing, full, suppressed) says the server tells mailboxes apart.
func domainWide(r apiResult) bool {
	switch r.Code {
	case "catch_all", "domain_not_found", "domain_null_mx", "domain_no_mail_host", "disposable_domain", "legacy_domain", "opted_out":
		return true
	case "address_suppressed", "address_unsupported":
		return false
	}
	return r.Verdict == "unknown"
}

// probe resolves the candidates through the API in two waves, within the
// credit budget, or only plans it when o.dryRun is set. Every candidate gets
// an outcome. On an API error the outcomes gathered so far are still returned.
func probe(o options, e env, candidates []string) (map[string]outcome, probeReport, error) {
	out := make(map[string]outcome, len(candidates))
	for _, email := range candidates {
		out[email] = outcome{API: "no", Code: "mailbox_not_checked"}
	}
	var rep probeReport
	scouts, rest := planWaves(candidates)
	overBudget := func(scout string) {
		out[scout] = outcome{API: "no", Code: "budget_exhausted"}
		for _, email := range rest[domainOf(scout)] {
			out[email] = outcome{API: "no", Code: "budget_exhausted"}
		}
	}

	if o.dryRun {
		for i, scout := range scouts {
			if o.budget > 0 && i >= o.budget {
				overBudget(scout)
				continue
			}
			out[scout] = outcome{API: "would_send", Code: "would_send"}
			rep.Wave1 = append(rep.Wave1, scout)
			for _, email := range rest[domainOf(scout)] {
				out[email] = outcome{API: "would_send_conditional", Code: "would_send_conditional"}
				rep.Wave2 = append(rep.Wave2, email)
			}
		}
		return out, rep, nil
	}
	if o.budget <= 0 || len(candidates) == 0 {
		return out, rep, nil
	}

	key := e.getenv("SALCHIMAIL_API_KEY")
	if key == "" {
		return out, rep, errors.New("set the SALCHIMAIL_API_KEY environment variable to use --budget (or drop --budget to stay local)")
	}
	st, err := loadState(o.statePath)
	if err != nil {
		return out, rep, err
	}
	c := &client{base: strings.TrimRight(o.apiURL, "/"), key: key, lang: o.lang, http: &http.Client{Timeout: 60 * time.Second, CheckRedirect: noRedirects}, sleep: e.sleep, poll: o.poll, wait: o.wait, st: st}
	remaining, err := c.preflight()
	if err != nil {
		return out, rep, err
	}
	rep.TestMode = c.testMode
	left := min(o.budget, remaining)
	if c.testMode {
		left = o.budget // test keys are never charged
	}
	// A resumed run keeps the allowance it started with: the balance it reads
	// now already excludes the batch in flight, and a smaller allowance would
	// change the plan, so the batch it paid for would never be collected.
	plan := hashOf(strconv.Itoa(o.budget), strconv.FormatBool(c.testMode), strings.Join(candidates, "\n"))
	if st.Plan == plan {
		left = st.Left
	} else {
		st.Plan, st.Left = plan, left
	}

	// A sent address costs at most one credit, so sending no more than the
	// credits left keeps the run inside the budget whatever the answers are.
	send := func(wave []string) (map[string]apiResult, error) {
		results, pending, err := c.verify(wave)
		rep.Pending = rep.Pending || pending
		for _, email := range wave {
			if r, ok := results[email]; ok {
				charged := min(max(r.Charged, 0), 1) // never trust an answer that would stretch the budget
				out[email] = outcome{API: "sent", Verdict: r.Verdict, Code: r.Code, Reason: r.Reason}
				rep.Charged += charged
				left -= charged
			} else if pending {
				out[email] = outcome{API: "pending", Code: "batch_pending"}
			} // else the request failed or the batch was canceled: it stays mailbox_not_checked
		}
		return results, err
	}

	for i, scout := range scouts {
		if i >= left {
			overBudget(scout)
		} else {
			rep.Wave1 = append(rep.Wave1, scout)
		}
	}
	first, err := send(rep.Wave1)
	if err != nil || rep.Pending {
		for _, scout := range rep.Wave1 {
			for _, email := range rest[domainOf(scout)] {
				if rep.Pending {
					out[email] = outcome{API: "no", Code: "batch_pending"}
				}
			}
		}
		return out, rep, err
	}
	for _, scout := range rep.Wave1 {
		r, answered := first[scout]
		for _, email := range rest[domainOf(scout)] {
			switch {
			case !answered: // no result for the scout: stays not checked
			case domainWide(r):
				out[email] = outcome{API: "no", Verdict: r.Verdict, Code: r.Code, Inferred: true}
			case len(rep.Wave2) < left:
				rep.Wave2 = append(rep.Wave2, email)
			default:
				out[email] = outcome{API: "no", Code: "budget_exhausted"}
			}
		}
	}
	if _, err = send(rep.Wave2); err != nil || rep.Pending {
		return out, rep, err
	}
	return out, rep, st.complete()
}
