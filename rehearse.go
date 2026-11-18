package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
)

// facts is everything known about one sign-up address, kept as independent
// flags because one address can break several rules at once.
type facts struct {
	noDomain                            bool // syntax not supported: nothing else was looked at
	badSyntax, disposable, legacy, free bool
	dead                                string // reason_code when the domain cannot receive mail
	api                                 outcome
}

func (f facts) invalid() bool { return f.badSyntax || f.dead != "" }

type rule struct {
	id      string
	mailbox bool // needs answers from the API's mailbox mode
	blocks  func(facts) bool
}

var rules = []rule{
	{"reject_syntax", false, func(f facts) bool { return f.badSyntax }},
	{"require_mx", false, func(f facts) bool { return f.dead != "" }},
	{"reject_invalid", false, facts.invalid},
	{"block_disposable", false, func(f facts) bool { return f.disposable }},
	{"block_legacy", false, func(f facts) bool { return f.legacy }},
	{"block_free", false, func(f facts) bool { return f.free }},
	{"reject_invalid_block_disposable", false, func(f facts) bool { return f.invalid() || f.disposable }},
	{"mailbox_reject_invalid", true, func(f facts) bool { return f.invalid() || f.api.Verdict == "invalid" }},
	{"mailbox_reject_invalid_risky", true, func(f facts) bool {
		return f.invalid() || f.disposable || f.legacy || f.api.Verdict == "invalid" || f.api.Verdict == "risky"
	}},
}

var outcomeWords = map[string]string{
	"paid": "good", "payment": "good", "activated": "good", "activation": "good", "good": "good", "true": "good", "1": "good", "yes": "good",
	"fraud": "junk", "spam": "junk", "junk": "junk", "fake": "junk", "chargeback": "junk", "hard_bounce": "junk",
	"": "", "none": "", "false": "", "0": "", "no": "", "unknown": "",
}

// label merges the sign-up's own column with the history: flagged as fraud
// wins, then paid or activated, then a hard bounce of your own mail.
func label(column string, events []Event) string {
	has := map[string]bool{}
	for _, ev := range events {
		has[ev.Kind] = true
	}
	switch {
	case has["fraud"]:
		return "junk"
	case column != "":
		return column
	case has["payment"] || has["activation"]:
		return "good"
	case has["hard_bounce"]:
		return "junk"
	}
	return ""
}

func rehearse(o options, e env) (int, error) {
	t := translate(o.lang)
	cols, recs, err := readRecords(o.list)
	if err != nil {
		return 1, err
	}
	col, err := pickColumn(o.list, cols, o.emailCol)
	if err != nil {
		return 1, err
	}
	// The default column is optional (the history can carry the outcomes); one the user named must exist.
	outcomeCol, err := pickColumn(o.list, cols, o.outcomeCol)
	if err != nil && o.outcomeCol != "outcome" {
		return 1, err
	}
	history, err := readHistory(o.history)
	if err != nil {
		return 1, err
	}

	rows := make([]string, len(recs))
	labels := make([]string, len(recs))
	known := map[string]*facts{}
	var domains, order []string
	inList := map[string]bool{}
	for i, rec := range recs {
		email, domain, ok := normalize(rec[col])
		rows[i] = email
		word, valid := outcomeWords[strings.ToLower(strings.TrimSpace(rec[outcomeCol]))]
		if !valid {
			return 1, fmt.Errorf("%s row %d: outcome %q not understood (use paid, activated, fraud or leave it empty)", o.list, i+1, rec[outcomeCol])
		}
		labels[i] = label(word, history[email])
		if known[email] != nil {
			continue
		}
		f := &facts{noDomain: !ok, badSyntax: !ok && !nonASCII(email)}
		known[email] = f
		order = append(order, email)
		if !ok {
			continue
		}
		f.disposable, f.legacy, f.free = listed(domain, disposable), listed(domain, legacy), listed(domain, free)
		if !inList[domain] {
			inList[domain] = true
			domains = append(domains, domain)
		}
	}
	status := map[string]string{}
	if !o.noDNS {
		status = lookupDomains(e.resolver, domains)
	}
	// Only addresses nothing local already rejects are worth a mailbox check.
	var candidates []string
	for _, email := range order {
		f := known[email]
		if f.noDomain {
			continue
		}
		f.dead = deadDomain(status[domainOf(email)])
		if f.dead == "" && !f.disposable && !f.legacy {
			candidates = append(candidates, email)
		}
	}
	var w io.Writer
	closeOut := func() error { return nil }
	if o.out != "" {
		if w, closeOut, err = openOut(o.out, e.stdout, o.list, o.history); err != nil {
			return 1, err
		}
	}
	outcomes, rep, probeErr := probe(o, e, candidates)
	checked := 0
	for email, oc := range outcomes {
		known[email].api = oc
		if oc.Verdict != "" {
			checked++
		}
	}

	total := map[string]int{}
	for _, l := range labels {
		total[l]++
	}
	fmt.Fprintf(e.stdout, t("reh_title")+"\n\n", len(rows), total["good"], total["junk"], total[""])
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, t("reh_header"))
	blockedBy := make([][]string, len(rows))
	for _, r := range rules {
		if r.mailbox && checked == 0 {
			continue
		}
		n := map[string]int{}
		for i, email := range rows {
			if r.blocks(*known[email]) {
				n[labels[i]]++
				blockedBy[i] = append(blockedBy[i], r.id)
			}
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%s\n", r.id, n["good"]+n["junk"]+n[""], n["good"], n["junk"], n[""], t("rule_"+r.id))
	}
	tw.Flush()
	fmt.Fprintln(e.stdout)
	if checked > 0 {
		fmt.Fprintf(e.stdout, t("reh_checked")+"\n", checked, len(order))
	}
	fmt.Fprintln(e.stdout, t("reh_caveats"))

	if o.out != "" {
		cw := csv.NewWriter(w)
		cw.Write([]string{"row", "email", "outcome", "api", "reason_codes", "blocked_by"})
		for i, email := range rows {
			f := known[email]
			var codes []string
			add := func(on bool, code string) {
				if on {
					codes = append(codes, code)
				}
			}
			add(f.badSyntax, "syntax_invalid")
			add(f.disposable, "disposable_domain")
			add(f.legacy, "legacy_domain")
			add(f.free, "free_provider")
			add(f.dead != "", f.dead)
			add(f.api.Code != "", f.api.Code)
			api := f.api.API
			if api == "" {
				api = "no"
			}
			cw.Write([]string{strconv.Itoa(i + 1), csvCell(email), labels[i], api, csvCell(strings.Join(codes, ";")), strings.Join(blockedBy[i], ";")})
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return 1, err
		}
		if err := closeOut(); err != nil {
			return 1, err
		}
	}

	p := func(key string, args ...any) { fmt.Fprintf(e.stderr, t(key)+"\n", args...) }
	sent := rep.Sent
	if o.dryRun {
		printPlan(o, e, t, rep)
	} else {
		p("sum_sent", sent, rep.Charged)
		p("sum_private", len(order)-sent, len(order))
	}
	if rep.TestMode {
		p("sum_test")
	}
	return finish(o, e, t, rep, probeErr)
}
