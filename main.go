// salchimail-local resolves an email list on the customer's own machine and
// asks the SalchiMail API only about the addresses where a mailbox probe can
// add something. It talks to nobody else and collects no telemetry.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

const version = "0.2.1"

const usage = `salchimail-local %s: verify an email list on your own machine first.

  salchimail-local clean --list list.csv [--history history.csv] [--dry-run] [--budget N] [--out results.csv]
  salchimail-local rehearse --signups signups.csv [--history history.csv] [--dry-run] [--budget N] [--out detail.csv]
  salchimail-local version

Nothing is sent to the API without --budget. The API key is read from the
SALCHIMAIL_API_KEY environment variable. Run a command with -h for its flags.
Guide: https://docs.salchimail.com/local-first
`

type options struct {
	list, history, out   string
	emailCol, outcomeCol string
	lang, apiURL         string
	statePath            string
	budget               int
	dryRun, noDNS        bool
	now                  time.Time
	wait, poll           time.Duration
}

// env is what the tests replace: output, DNS, environment and the clock.
type env struct {
	stdout, stderr io.Writer
	resolver       resolver
	getenv         func(string) string
	sleep          func(time.Duration)
}

func main() {
	os.Exit(run(os.Args[1:], env{stdout: os.Stdout, stderr: os.Stderr, resolver: net.DefaultResolver, getenv: os.Getenv, sleep: time.Sleep}))
}

func run(args []string, e env) int {
	if len(args) == 0 {
		fmt.Fprintf(e.stderr, usage, version)
		return 2
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintf(e.stdout, usage, version)
		return 0
	}
	cmd := args[0]
	if cmd == "version" {
		fmt.Fprintln(e.stdout, version)
		return 0
	}
	if cmd != "clean" && cmd != "rehearse" {
		fmt.Fprintf(e.stderr, "unknown command %q\n\n"+usage, cmd, version)
		return 2
	}

	var o options
	var now string
	fs := flag.NewFlagSet("salchimail-local "+cmd, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	if cmd == "clean" {
		fs.StringVar(&o.list, "list", "", "addresses to clean: CSV with a header row, or JSONL (required)")
	} else {
		fs.StringVar(&o.list, "signups", "", "past sign-ups: CSV with a header row, or JSONL (required)")
		fs.StringVar(&o.outcomeCol, "outcome-column", "outcome", "column that says how the sign-up ended: paid, activated, fraud or empty")
	}
	fs.StringVar(&o.history, "history", "", "your own history: CSV or JSONL with email, kind, at. It never leaves this machine")
	fs.StringVar(&o.emailCol, "email-column", "email", "name of the column that holds the address")
	fs.StringVar(&o.out, "out", "", "write the per-address CSV to this file (clean prints it to standard output by default)")
	fs.IntVar(&o.budget, "budget", 0, "most mailbox credits this run may spend; 0 sends nothing to the API")
	fs.BoolVar(&o.dryRun, "dry-run", false, "show exactly which addresses would be sent to the API, and send nothing")
	fs.StringVar(&o.lang, "lang", "en", "language of reasons and summary: en or es")
	fs.BoolVar(&o.noDNS, "no-dns", false, "skip the DNS lookups (for machines that cannot resolve public names)")
	fs.StringVar(&now, "now", "", "reference date for the age of your history (default: now)")
	fs.StringVar(&o.statePath, "state", ".salchimail-local-state.json", "file that lets an interrupted run resume; it holds batch ids, never addresses")
	fs.DurationVar(&o.wait, "wait", 30*time.Minute, "how long to wait for the batches before stopping; run again to resume")
	fs.DurationVar(&o.poll, "poll-interval", 15*time.Second, "time between status checks of a batch (at least 15s)")
	fs.StringVar(&o.apiURL, "api-url", "https://api.salchimail.com/v1", "API base URL")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	fail := func(err error) int {
		fmt.Fprintln(e.stderr, "error:", err)
		return 1
	}
	if o.list == "" || fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	if o.lang != "en" && o.lang != "es" {
		return fail(errors.New("--lang must be en or es"))
	}
	if o.budget < 0 {
		return fail(errors.New("--budget cannot be negative"))
	}
	if err := checkAPIURL(o.apiURL); err != nil {
		return fail(err)
	}
	if o.poll <= 0 || o.wait < 0 {
		return fail(errors.New("--poll-interval must be positive and --wait cannot be negative"))
	}
	if o.poll < 15*time.Second && !isLoopback(o.apiURL) {
		o.poll = 15 * time.Second // the documented polling floor of the API; shorter only against a local test server
	}
	o.now = time.Now()
	if now != "" {
		t, err := parseTime(now)
		if err != nil {
			return fail(fmt.Errorf("--now: %w", err))
		}
		o.now = t
	}

	command := clean
	if cmd == "rehearse" {
		command = rehearse
	}
	code, err := command(o, e)
	if err != nil {
		return fail(err)
	}
	return code
}
