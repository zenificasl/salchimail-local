package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
)

// decision is one output row of `clean`.
type decision struct {
	Action string // send | do_not_send | review | not_checked
	Source string // own_history | local_rules | local_dns | api | api_domain | none
	API    string // no | sent | pending | would_send | would_send_conditional
	Code   string
	Reason string
}

func actionFor(verdict string) string {
	switch verdict {
	case "valid":
		return "send"
	case "invalid":
		return "do_not_send"
	}
	return "review" // risky and unknown: never treat unknown as invalid
}

func fromOutcome(oc outcome, t translator) decision {
	d := decision{API: oc.API, Code: oc.Code}
	switch {
	case oc.Verdict == "":
		d.Action, d.Source, d.Reason = "not_checked", "none", t(oc.Code)
	case !oc.Inferred:
		d.Action, d.Source, d.Reason = actionFor(oc.Verdict), "api", oc.Reason
	case oc.Code == "catch_all":
		d.Action, d.Source, d.Reason = "review", "api_domain", t("inferred_catch_all")
	case oc.Verdict == "unknown":
		d.Action, d.Source, d.Reason = "review", "api_domain", fmt.Sprintf(t("inferred_blocked"), oc.Code)
	default:
		d.Action, d.Source, d.Reason = actionFor(oc.Verdict), "api_domain", fmt.Sprintf(t("inferred_domain"), oc.Code)
	}
	return d
}

func local(action, source, code string, t translator) decision {
	return decision{Action: action, Source: source, API: "no", Code: code, Reason: t(code)}
}

// localDecision resolves what syntax and the domain lists can, without DNS.
func localDecision(email, domain string, ok bool, t translator) (decision, bool) {
	switch {
	case !ok && nonASCII(email):
		return local("review", "local_rules", "address_unsupported", t), true
	case !ok:
		return local("do_not_send", "local_rules", "syntax_invalid", t), true
	case listed(domain, disposable):
		return local("review", "local_rules", "disposable_domain", t), true
	case listed(domain, legacy):
		return local("review", "local_rules", "legacy_domain", t), true
	}
	return decision{}, false
}

// openOut opens the results file before anything is sent, so a bad path costs
// no credits. It refuses to overwrite an input and keeps the file private.
func openOut(path string, stdout io.Writer, inputs ...string) (io.Writer, func() error, error) {
	if path == "" {
		return stdout, func() error { return nil }, nil
	}
	if out, err := os.Stat(path); err == nil {
		for _, in := range inputs {
			if info, err := os.Stat(in); in != "" && err == nil && os.SameFile(out, info) {
				return nil, nil, fmt.Errorf("--out %s is one of the input files; choose another name", path)
			}
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

func clean(o options, e env) (int, error) {
	t := translate(o.lang)
	cols, recs, err := readRecords(o.list)
	if err != nil {
		return 1, err
	}
	col, err := pickColumn(o.list, cols, o.emailCol)
	if err != nil {
		return 1, err
	}
	history, err := readHistory(o.history)
	if err != nil {
		return 1, err
	}

	rows := make([]string, len(recs))
	decided := map[string]decision{}
	var pending []string // unique addresses nothing local has resolved yet
	seen := map[string]bool{}
	for i, rec := range recs {
		email, domain, ok := normalize(rec[col])
		rows[i] = email
		if seen[email] {
			continue
		}
		seen[email] = true
		// Syntax first; then what you saw yourself beats any list.
		if d, done := localDecision(email, "", ok, t); done {
			decided[email] = d
		} else if d, done := decideOwn(history[email], o.now, t); done {
			decided[email] = d
		} else if d, done := localDecision(email, domain, ok, t); done {
			decided[email] = d
		} else {
			pending = append(pending, email)
		}
	}

	var candidates []string
	if o.noDNS {
		candidates = pending
	} else {
		var domains []string
		inList := map[string]bool{}
		for _, email := range pending {
			if d := domainOf(email); !inList[d] {
				inList[d] = true
				domains = append(domains, d)
			}
		}
		status := lookupDomains(e.resolver, domains)
		for _, email := range pending {
			if code := deadDomain(status[domainOf(email)]); code != "" {
				decided[email] = local("do_not_send", "local_dns", code, t)
			} else {
				candidates = append(candidates, email)
			}
		}
	}

	w, closeOut, err := openOut(o.out, e.stdout, o.list, o.history)
	if err != nil {
		return 1, err
	}
	outcomes, rep, probeErr := probe(o, e, candidates)
	for email, oc := range outcomes {
		decided[email] = fromOutcome(oc, t)
	}

	cw := csv.NewWriter(w)
	cw.Write([]string{"row", "email", "action", "source", "api", "reason_code", "reason"})
	for i, email := range rows {
		d := decided[email]
		cw.Write([]string{strconv.Itoa(i + 1), csvCell(email), d.Action, d.Source, d.API, csvCell(d.Code), csvCell(d.Reason)})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return 1, err
	}
	if err := closeOut(); err != nil {
		return 1, err
	}

	bySource, byAction := map[string]int{}, map[string]int{}
	for _, d := range decided {
		bySource[d.Source]++
		byAction[d.Action]++
	}
	sent := rep.Sent
	p := func(key string, args ...any) { fmt.Fprintf(e.stderr, t(key)+"\n", args...) }
	p("sum_title", len(rows), len(decided))
	p("sum_local", bySource["own_history"]+bySource["local_rules"]+bySource["local_dns"])
	p("sum_own", bySource["own_history"])
	p("sum_rules", bySource["local_rules"])
	p("sum_dns", bySource["local_dns"])
	if o.dryRun {
		printPlan(o, e, t, rep)
	} else {
		p("sum_sent", sent, rep.Charged)
		p("sum_inferred", bySource["api_domain"])
		p("sum_private", len(decided)-sent, len(decided))
	}
	p("sum_not_checked", byAction["not_checked"])
	p("sum_actions", byAction["send"], byAction["do_not_send"], byAction["review"], byAction["not_checked"])
	if rep.TestMode {
		p("sum_test")
	}
	if !o.dryRun && o.budget <= 0 && len(candidates) > 0 {
		p("sum_hint_budget", len(candidates))
	}
	return finish(o, e, t, rep, probeErr)
}

// printPlan shows a dry run: the exact addresses of the first wave and the
// ones that may follow, and nothing else.
func printPlan(o options, e env, t translator, rep probeReport) {
	p := func(key string, args ...any) { fmt.Fprintf(e.stderr, t(key)+"\n", args...) }
	p("dry_title", o.apiURL)
	p("dry_wave1", len(rep.Wave1))
	for _, email := range rep.Wave1 {
		fmt.Fprintln(e.stderr, "    "+email)
	}
	p("dry_wave2", len(rep.Wave2))
	for _, email := range rep.Wave2 {
		fmt.Fprintln(e.stderr, "    "+email)
	}
	if o.budget > 0 {
		p("dry_budget", o.budget)
	} else {
		p("dry_no_budget")
	}
	p("dry_private")
}

func finish(o options, e env, t translator, rep probeReport, probeErr error) (int, error) {
	if probeErr != nil {
		return 1, probeErr
	}
	if rep.Pending {
		fmt.Fprintf(e.stderr, t("pending")+"\n", o.statePath)
		return 3, nil
	}
	return 0, nil
}
