package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

const fakeKey = "fake-key-for-tests" // not key-shaped, so secret scanners stay quiet

// Synthetic world of testdata/: every domain is under .example and no address belongs to anyone.
var testDNS = fakeDNS{"discrimina.example": "mx", "aceptatodo.example": "mx", "bloquea.example": "mx", "nulo.example": "null_mx", "gmail.com": "mx", "mailinator.com": "mx", "wanadoo.es": "mx"}

var testAnswers = map[string]apiResult{
	"fede@aceptatodo.example": {Verdict: "risky", Code: "catch_all", Reason: "The mail server also accepts a random recipient."},
	"hugo@bloquea.example":    {Verdict: "unknown", Code: "probe_blocked", Reason: "The mail server refuses our probe."},
	"kira@bloquea.example":    {Verdict: "unknown", Code: "probe_blocked", Reason: "The mail server refuses our probe."},
	"nora@discrimina.example": {Verdict: "invalid", Code: "mailbox_not_found", Reason: "The mail server says the recipient does not exist."},
}

// fakeAPI is a stand-in for api.salchimail.com with the parts of the contract
// the tool uses: account, usage, idempotent batch creation, polling and paged results.
type fakeAPI struct {
	mu         sync.Mutex
	srv        *httptest.Server
	remaining  int
	live       bool // a live key: credits are charged and the balance caps the budget
	maxBatch   int
	pollsFirst int // status checks answered "processing" before "completed"
	answers    map[string]apiResult
	fail       []int             // statuses to answer first, one per request
	created    [][]string        // addresses of every batch really created
	keys       []string          // Idempotency-Key of every POST
	byKey      map[string]string // idempotency key → batch id
	batches    map[string][]string
	polls      map[string]int
	requests   []string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{remaining: 1000, maxBatch: 100, answers: testAnswers, byKey: map[string]string{}, batches: map[string][]string{}, polls: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) url() string { return f.srv.URL + "/v1" }

func (f *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	reply := func(status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}
	problem := func(status int, code string) {
		if status == 429 {
			w.Header().Set("Retry-After", "7")
		}
		reply(status, map[string]any{"error": map[string]string{"type": "x", "code": code, "message": "fake " + code, "request_id": "req_fake"}})
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeKey {
		problem(401, "invalid_api_key")
		return
	}
	if len(f.fail) > 0 {
		status := f.fail[0]
		f.fail = f.fail[1:]
		problem(status, map[int]string{429: "rate_limit_exceeded", 503: "engine_unavailable", 402: "insufficient_credits"}[status])
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	batchJSON := func(id, status string) map[string]any {
		return map[string]any{"id": id, "object": "batch", "livemode": f.live, "status": status}
	}
	switch {
	case r.Method == "GET" && path == "/account":
		reply(200, map[string]any{"livemode": f.live, "limits": map[string]any{"max_batch_size": f.maxBatch, "modes": []string{"basic", "mailbox"}}})
	case r.Method == "GET" && path == "/usage":
		reply(200, map[string]any{"credits": map[string]any{"mailbox": map[string]any{"remaining": f.remaining}}})
	case r.Method == "POST" && path == "/batches":
		var body struct {
			Emails []string `json:"emails"`
			Mode   string   `json:"mode"`
		}
		key := r.Header.Get("Idempotency-Key")
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Mode != "mailbox" || key == "" || len(body.Emails) == 0 {
			problem(400, "invalid_parameter")
			return
		}
		if len(body.Emails) > f.maxBatch {
			problem(400, "batch_too_large")
			return
		}
		f.keys = append(f.keys, key)
		id, replay := f.byKey[key]
		if !replay {
			id = fmt.Sprintf("bat_%032x", len(f.batches)+1)
			f.byKey[key] = id
			f.batches[id] = body.Emails
			f.created = append(f.created, body.Emails)
		}
		reply(202, batchJSON(id, "queued"))
	case r.Method == "GET" && strings.HasSuffix(path, "/results"):
		emails := f.batches[strings.Split(path, "/")[2]]
		start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		end := min(start+2, len(emails)) // tiny pages, to exercise the cursor
		data := []apiResult{}
		for _, email := range emails[start:end] {
			res, ok := f.answers[email]
			if !ok {
				res = apiResult{Verdict: "valid", Code: "mailbox_exists", Reason: "The mail server accepts this recipient and rejects a random one."}
			}
			res.Email = email
			if res.Verdict != "unknown" {
				res.Charged = 1
			}
			data = append(data, res)
		}
		page := map[string]any{"object": "list", "data": data, "has_more": end < len(emails), "next_cursor": nil}
		if end < len(emails) {
			page["next_cursor"] = strconv.Itoa(end)
		}
		reply(200, page)
	case r.Method == "GET" && strings.HasPrefix(path, "/batches/"):
		id := strings.Split(path, "/")[2]
		if f.batches[id] == nil {
			problem(404, "not_found")
			return
		}
		f.polls[id]++
		if f.polls[id] <= f.pollsFirst {
			reply(200, batchJSON(id, "processing"))
		} else {
			reply(200, batchJSON(id, "completed"))
		}
	default:
		problem(404, "not_found")
	}
}

type result struct {
	code           int
	stdout, stderr string
	slept          []time.Duration
	rows           map[string][]string // email → output row
}

// cli runs the tool as a user would, with DNS, clock and environment replaced.
func cli(t *testing.T, key string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	var r result
	e := env{stdout: &stdout, stderr: &stderr, resolver: testDNS,
		getenv: func(name string) string {
			if name == "SALCHIMAIL_API_KEY" {
				return key
			}
			return ""
		},
		sleep: func(d time.Duration) { r.slept = append(r.slept, d) },
	}
	r.code = run(args, e)
	r.stdout, r.stderr = stdout.String(), stderr.String()
	if strings.Contains(r.stdout+r.stderr, fakeKey) {
		t.Error("the API key was printed")
	}
	if rows, err := csv.NewReader(strings.NewReader(r.stdout)).ReadAll(); err == nil && len(rows) > 0 && rows[0][0] == "row" {
		r.rows = map[string][]string{}
		for _, row := range rows[1:] {
			r.rows[row[1]] = row
		}
	}
	return r
}

func cleanArgs(t *testing.T, api *fakeAPI, extra ...string) []string {
	args := []string{"clean", "--list", "testdata/list.csv", "--history", "testdata/history.csv", "--now", "2026-11-17T12:00:00Z",
		"--state", filepath.Join(t.TempDir(), "state.json"), "--api-url", api.url(), "--poll-interval", "1s"}
	return append(args, extra...)
}

// want checks columns action, source, api and reason_code of one address.
func (r result) want(t *testing.T, email, action, source, api, code string) {
	t.Helper()
	row := r.rows[email]
	if row == nil {
		t.Errorf("%s: no output row", email)
		return
	}
	if got := strings.Join(row[2:6], " "); got != strings.Join([]string{action, source, api, code}, " ") {
		t.Errorf("%s: got %q, want %q %q %q %q", email, got, action, source, api, code)
	}
	if row[6] == "" {
		t.Errorf("%s: decision without a readable reason", email)
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the output (go test -update rewrites it):\n%s", name, got)
	}
}

var localRows = [][5]string{
	{"ana@discrimina.example", "send", "own_history", "no", "own_reply"},
	{"bruno@discrimina.example", "do_not_send", "own_history", "no", "own_hard_bounce"},
	{"dario@aceptatodo.example", "send", "own_history", "no", "own_reply"},
	{"elena@aceptatodo.example", "do_not_send", "own_history", "no", "own_hard_bounce"},
	{"gema@bloquea.example", "send", "own_history", "no", "own_delivered"},
	{"luis@discrimina.example", "send", "own_history", "no", "own_reply"},
	{"quique@aceptatodo.example", "send", "own_history", "no", "own_click"},
	{"juan@muerto.example", "do_not_send", "local_dns", "no", "domain_no_mail_host"},
	{"kira@muerto.example", "do_not_send", "local_dns", "no", "domain_no_mail_host"},
	{"olga@nulo.example", "do_not_send", "local_dns", "no", "domain_null_mx"},
	{"marta@mailinator.com", "review", "local_rules", "no", "disposable_domain"},
	{"not-an-email", "do_not_send", "local_rules", "no", "syntax_invalid"},
}

var candidates = []string{"carla@discrimina.example", "fede@aceptatodo.example", "hugo@bloquea.example", "ines@bloquea.example", "nora@discrimina.example", "pablo@discrimina.example", "rosa@aceptatodo.example"}

func TestCleanWithoutBudgetStaysLocal(t *testing.T) {
	api := newFakeAPI(t)
	r := cli(t, fakeKey, cleanArgs(t, api)...)
	if r.code != 0 || len(api.requests) != 0 {
		t.Fatalf("exit %d, API requests %v\n%s", r.code, api.requests, r.stderr)
	}
	if len(r.rows) != 19 || strings.Count(r.stdout, "\n") != 21 {
		t.Errorf("want 20 rows for 19 unique addresses, got %d unique", len(r.rows))
	}
	for _, w := range localRows {
		r.want(t, w[0], w[1], w[2], w[3], w[4])
	}
	for _, email := range candidates {
		r.want(t, email, "not_checked", "none", "no", "mailbox_not_checked")
	}
	if !strings.Contains(r.stderr, "7 addresses can only be resolved by a mailbox check") {
		t.Errorf("no hint about the budget:\n%s", r.stderr)
	}
}

func TestCleanDryRunSendsNothing(t *testing.T) {
	api := newFakeAPI(t)
	r := cli(t, fakeKey, cleanArgs(t, api, "--dry-run", "--budget", "10")...)
	if r.code != 0 || len(api.requests) != 0 {
		t.Fatalf("exit %d, API requests %v\n%s", r.code, api.requests, r.stderr)
	}
	for _, email := range []string{"carla@discrimina.example", "fede@aceptatodo.example", "hugo@bloquea.example"} {
		r.want(t, email, "not_checked", "none", "would_send", "would_send")
	}
	for _, email := range []string{"ines@bloquea.example", "nora@discrimina.example", "pablo@discrimina.example", "rosa@aceptatodo.example"} {
		r.want(t, email, "not_checked", "none", "would_send_conditional", "would_send_conditional")
	}
	// The plan names every address that could leave, and none that was resolved locally.
	plan := r.stderr[strings.Index(r.stderr, "DRY RUN"):]
	for _, email := range candidates {
		if !strings.Contains(plan, "    "+email+"\n") {
			t.Errorf("the plan does not list %s", email)
		}
	}
	for _, w := range localRows {
		if strings.Contains(plan, w[0]) {
			t.Errorf("the plan lists %s, which was resolved locally", w[0])
		}
	}
	golden(t, "clean-dry-run.stderr.golden", strings.ReplaceAll(r.stderr, api.url(), "https://api.salchimail.com/v1"))

	// Without a key and without a budget a dry run still works, and still sends nothing.
	r = cli(t, "", cleanArgs(t, api, "--dry-run", "--lang", "es")...)
	if r.code != 0 || len(api.requests) != 0 || !strings.Contains(r.stderr, "Ola 1, una dirección por dominio (3)") {
		t.Errorf("dry run without key: exit %d, requests %v\n%s", r.code, api.requests, r.stderr)
	}
	r = cli(t, "", cleanArgs(t, api, "--dry-run", "--budget", "10", "--lang", "es")...)
	golden(t, "clean-dry-run.stderr.es.golden", strings.ReplaceAll(r.stderr, api.url(), "https://api.salchimail.com/v1"))
}

func TestCleanSendsOnlyWhatAProbeCanResolve(t *testing.T) {
	api := newFakeAPI(t)
	args := cleanArgs(t, api, "--budget", "10")
	r := cli(t, fakeKey, args...)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	// Wave 1: one address per domain. Wave 2: only the domain whose server tells mailboxes apart.
	want := [][]string{{"carla@discrimina.example", "fede@aceptatodo.example", "hugo@bloquea.example"}, {"nora@discrimina.example", "pablo@discrimina.example"}}
	if !slices.EqualFunc(api.created, want, slices.Equal[[]string]) {
		t.Fatalf("batches sent: %v\nwant: %v", api.created, want)
	}
	for _, w := range localRows {
		r.want(t, w[0], w[1], w[2], w[3], w[4])
	}
	r.want(t, "carla@discrimina.example", "send", "api", "sent", "mailbox_exists")
	r.want(t, "fede@aceptatodo.example", "review", "api", "sent", "catch_all")
	r.want(t, "hugo@bloquea.example", "review", "api", "sent", "probe_blocked")
	r.want(t, "nora@discrimina.example", "do_not_send", "api", "sent", "mailbox_not_found")
	r.want(t, "pablo@discrimina.example", "send", "api", "sent", "mailbox_exists")
	r.want(t, "rosa@aceptatodo.example", "review", "api_domain", "no", "catch_all")
	r.want(t, "ines@bloquea.example", "review", "api_domain", "no", "probe_blocked")
	for _, key := range api.keys {
		if !strings.HasPrefix(key, "salchimail-local-") {
			t.Errorf("POST without a content-based Idempotency-Key: %q", key)
		}
	}
	if _, err := os.Stat(args[8]); !os.IsNotExist(err) {
		t.Error("the state file must be removed once every result is collected")
	}
	golden(t, "clean.csv.golden", r.stdout)
	golden(t, "clean.stderr.golden", r.stderr)

	// The same command again replays the same batches: nothing new is created or charged twice.
	cli(t, fakeKey, args...)
	if len(api.created) != 2 || len(api.keys) != 4 || api.keys[0] != api.keys[2] || api.keys[1] != api.keys[3] {
		t.Errorf("a repeated run must reuse its idempotency keys: created %d, keys %v", len(api.created), api.keys)
	}

	es := cli(t, fakeKey, append(args, "--lang", "es")...)
	if !strings.Contains(es.rows["ana@discrimina.example"][6], "Te escribió el 2026-10-20") || !strings.Contains(es.stderr, "Nunca salieron de esta máquina: 14 de 19") {
		t.Errorf("Spanish output:\n%s\n%s", es.stdout, es.stderr)
	}
	golden(t, "clean.stderr.es.golden", es.stderr)
}

func sentAddresses(api *fakeAPI) []string {
	var all []string
	for _, b := range api.created {
		all = append(all, b...)
	}
	return all
}

func TestCleanRespectsTheBudget(t *testing.T) {
	api := newFakeAPI(t)
	r := cli(t, fakeKey, cleanArgs(t, api, "--budget", "2")...)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	// Two credits: two scouts go, both are charged, nothing else may be sent.
	if got := sentAddresses(api); !slices.Equal(got, []string{"carla@discrimina.example", "fede@aceptatodo.example"}) {
		t.Fatalf("sent %v", got)
	}
	for _, email := range []string{"hugo@bloquea.example", "ines@bloquea.example", "nora@discrimina.example", "pablo@discrimina.example"} {
		r.want(t, email, "not_checked", "none", "no", "budget_exhausted")
	}
	r.want(t, "rosa@aceptatodo.example", "review", "api_domain", "no", "catch_all")
	if !strings.Contains(r.stderr, "Sent to the SalchiMail API: 2 (mailbox credits charged: 2)") {
		t.Errorf("summary:\n%s", r.stderr)
	}

	// A wave-1 answer that was not charged (unknown) leaves its credit for wave 2.
	api = newFakeAPI(t)
	cli(t, fakeKey, cleanArgs(t, api, "--budget", "3")...)
	if got := sentAddresses(api); !slices.Equal(got, []string{"carla@discrimina.example", "fede@aceptatodo.example", "hugo@bloquea.example", "nora@discrimina.example"}) {
		t.Errorf("budget 3 sent %v", got)
	}

	// The balance of the account caps the budget of a live key; a test key is never charged.
	api = newFakeAPI(t)
	api.live, api.remaining = true, 1
	cli(t, fakeKey, cleanArgs(t, api, "--budget", "10")...)
	if got := sentAddresses(api); !slices.Equal(got, []string{"carla@discrimina.example"}) {
		t.Errorf("with one credit left, sent %v", got)
	}
}

// A resumed run keeps its first allowance: the balance it reads again already
// excludes the batch in flight, and must not change the plan.
func TestResumeKeepsTheBudgetItStartedWith(t *testing.T) {
	api := newFakeAPI(t)
	api.live, api.remaining, api.pollsFirst = true, 3, 3
	args := cleanArgs(t, api, "--budget", "10", "--wait", "2s")
	if r := cli(t, fakeKey, args...); r.code != 3 {
		t.Fatalf("first run: exit %d, want 3 (batch pending)\n%s", r.code, r.stderr)
	}
	api.remaining = 0 // the server reserved the credits of the batch in flight
	var r result
	for i := 0; i < 5; i++ {
		if r = cli(t, fakeKey, args...); r.code != 3 {
			break
		}
	}
	if r.code != 0 {
		t.Fatalf("resumed run: exit %d\n%s", r.code, r.stderr)
	}
	if got := sentAddresses(api); !slices.Equal(got, []string{"carla@discrimina.example", "fede@aceptatodo.example", "hugo@bloquea.example", "nora@discrimina.example"}) {
		t.Errorf("sent %v, want the same plan as an uninterrupted run with 3 credits", got)
	}
	r.want(t, "carla@discrimina.example", "send", "api", "sent", "mailbox_exists")
}

func TestOutIsCheckedBeforeAnythingIsSent(t *testing.T) {
	api := newFakeAPI(t)
	r := cli(t, fakeKey, cleanArgs(t, api, "--budget", "10", "--out", filepath.Join(t.TempDir(), "missing", "out.csv"))...)
	if r.code != 1 || len(api.created) != 0 {
		t.Errorf("exit %d, %d batches created; want 1 and none", r.code, len(api.created))
	}
	list := filepath.Join(t.TempDir(), "list.csv")
	if err := os.WriteFile(list, []byte("email\nana@discrimina.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = cli(t, fakeKey, "clean", "--list", list, "--out", list, "--no-dns")
	if data, _ := os.ReadFile(list); r.code != 1 || !strings.Contains(string(data), "ana@") {
		t.Errorf("--out equal to --list: exit %d, list now %q", r.code, data)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	api := newFakeAPI(t)
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Errorf("followed a redirect to %s", r.URL) }))
	t.Cleanup(elsewhere.Close)
	moved := httptest.NewServer(http.RedirectHandler(elsewhere.URL+"/v1/account", http.StatusTemporaryRedirect))
	t.Cleanup(moved.Close)
	if r := cli(t, fakeKey, cleanArgs(t, api, "--budget", "10", "--api-url", moved.URL+"/v1")...); r.code != 1 {
		t.Errorf("exit %d, want 1", r.code)
	}
}

func TestCleanSplitsBatchesAndResumes(t *testing.T) {
	api := newFakeAPI(t)
	api.maxBatch = 2
	api.pollsFirst = 2
	args := cleanArgs(t, api, "--budget", "10", "--wait", "1s")
	r := cli(t, fakeKey, args...)
	if r.code != 3 || !strings.Contains(r.stderr, "Run the same command again") {
		t.Fatalf("a run that outlives --wait must exit 3, got %d\n%s", r.code, r.stderr)
	}
	r.want(t, "carla@discrimina.example", "not_checked", "none", "pending", "batch_pending")
	r.want(t, "nora@discrimina.example", "not_checked", "none", "no", "batch_pending")
	state, err := os.ReadFile(args[8])
	if err != nil || strings.Contains(string(state), "@") || !strings.Contains(string(state), "bat_") {
		t.Fatalf("the state file must hold batch ids and no addresses: %s %v", state, err)
	}
	// Second run: picks the batch up from the state file and finishes.
	api.pollsFirst = 0
	r = cli(t, fakeKey, append(args, "--wait", "10m")...)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	want := [][]string{{"carla@discrimina.example", "fede@aceptatodo.example"}, {"hugo@bloquea.example"}, {"nora@discrimina.example", "pablo@discrimina.example"}}
	if !slices.EqualFunc(api.created, want, slices.Equal[[]string]) {
		t.Errorf("batches of at most 2, none created twice: %v", api.created)
	}
	r.want(t, "pablo@discrimina.example", "send", "api", "sent", "mailbox_exists")
}

func TestCleanAPIErrors(t *testing.T) {
	api := newFakeAPI(t)
	api.fail = []int{429, 503}
	r := cli(t, fakeKey, cleanArgs(t, api, "--budget", "10")...)
	if r.code != 0 || len(r.slept) < 2 || r.slept[0] != 7*time.Second || r.slept[1] != 4*time.Second {
		t.Errorf("429 and 503 must be retried, waiting Retry-After: exit %d, slept %v\n%s", r.code, r.slept, r.stderr)
	}

	api = newFakeAPI(t)
	api.fail = []int{402}
	r = cli(t, fakeKey, cleanArgs(t, api, "--budget", "10")...)
	if r.code != 1 || len(r.slept) != 0 || !strings.Contains(r.stderr, "insufficient_credits") || !strings.Contains(r.stderr, "req_fake") || len(api.created) != 0 {
		t.Errorf("402 must stop without retrying: exit %d\n%s", r.code, r.stderr)
	}
	// What was resolved locally is still written.
	r.want(t, "ana@discrimina.example", "send", "own_history", "no", "own_reply")

	r = cli(t, "sk_live_wrong", cleanArgs(t, api, "--budget", "10")...)
	if r.code != 1 || !strings.Contains(r.stderr, "invalid_api_key") || strings.Contains(r.stderr, "sk_live_wrong") {
		t.Errorf("401: exit %d\n%s", r.code, r.stderr)
	}

	requests := len(api.requests)
	r = cli(t, "", cleanArgs(t, api, "--budget", "10")...)
	if r.code != 1 || len(api.requests) != requests || !strings.Contains(r.stderr, "SALCHIMAIL_API_KEY") {
		t.Errorf("no key: exit %d, requests %d\n%s", r.code, len(api.requests)-requests, r.stderr)
	}

	r = cli(t, fakeKey, "clean", "--list", "testdata/list.csv", "--budget", "1", "--api-url", "http://api.example.com/v1")
	if r.code != 1 || !strings.Contains(r.stderr, "https") {
		t.Errorf("a plain-http API URL must be refused: exit %d\n%s", r.code, r.stderr)
	}
}

func rehearseArgs(t *testing.T, api *fakeAPI, extra ...string) []string {
	args := []string{"rehearse", "--signups", "testdata/signups.csv", "--state", filepath.Join(t.TempDir(), "state.json"), "--api-url", api.url(), "--poll-interval", "1s"}
	return append(args, extra...)
}

// tableRow returns the numbers of one rule in the rehearsal table.
func tableRow(t *testing.T, stdout, rule string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if fields := strings.Fields(line); len(fields) > 5 && fields[0] == rule {
			return strings.Join(fields[1:5], " ")
		}
	}
	t.Errorf("no row for %s in:\n%s", rule, stdout)
	return ""
}

func TestRehearseLocal(t *testing.T) {
	api := newFakeAPI(t)
	r := cli(t, fakeKey, rehearseArgs(t, api)...)
	if r.code != 0 || len(api.requests) != 0 {
		t.Fatalf("exit %d, API requests %v\n%s", r.code, api.requests, r.stderr)
	}
	if !strings.Contains(r.stdout, "13 sign-ups (5 good: paid or activated; 2 junk: flagged or hard-bounced; 6 with no outcome)") {
		t.Errorf("header:\n%s", r.stdout)
	}
	// rule → would block, good lost, junk stopped, no outcome
	for rule, want := range map[string]string{
		"reject_syntax": "1 0 0 1", "require_mx": "3 0 1 2", "reject_invalid": "4 0 1 3",
		"block_disposable": "2 1 1 0", "block_legacy": "1 1 0 0", "block_free": "0 0 0 0",
		"reject_invalid_block_disposable": "6 1 2 3",
	} {
		if got := tableRow(t, r.stdout, rule); got != want {
			t.Errorf("%s: got %q, want %q", rule, got, want)
		}
	}
	if strings.Contains(r.stdout, "mailbox_reject") {
		t.Error("mailbox rules must not appear when nothing was checked through the API")
	}
	golden(t, "rehearse.stdout.golden", r.stdout)
	golden(t, "rehearse.stdout.es.golden", cli(t, "", rehearseArgs(t, api, "--lang", "es")...).stdout)

	dry := cli(t, "", rehearseArgs(t, api, "--dry-run")...)
	if dry.code != 0 || len(api.requests) != 0 || !strings.Contains(dry.stderr, "    ana@discrimina.example\n") || strings.Contains(dry.stderr, "mailinator") {
		t.Errorf("dry run: exit %d, requests %v\n%s", dry.code, api.requests, dry.stderr)
	}
}

func TestRehearseWithMailboxChecks(t *testing.T) {
	api := newFakeAPI(t)
	detail := filepath.Join(t.TempDir(), "detail.csv")
	r := cli(t, fakeKey, rehearseArgs(t, api, "--budget", "10", "--out", detail, "--lang", "es")...)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	// Addresses that a local rule already rejects are never sent; one scout per domain goes first.
	want := [][]string{{"ana@discrimina.example", "fede@aceptatodo.example", "kira@bloquea.example"}, {"carla@discrimina.example", "nora@discrimina.example"}}
	if !slices.EqualFunc(api.created, want, slices.Equal[[]string]) {
		t.Fatalf("batches sent: %v", api.created)
	}
	if got := tableRow(t, r.stdout, "mailbox_reject_invalid"); got != "5 0 1 4" {
		t.Errorf("mailbox_reject_invalid: %q", got)
	}
	if got := tableRow(t, r.stdout, "mailbox_reject_invalid_risky"); got != "10 3 2 5" {
		t.Errorf("mailbox_reject_invalid_risky: %q", got)
	}
	golden(t, "rehearse-mailbox.stdout.es.golden", r.stdout)
	// Same inputs, so the batches are replayed, not created again.
	golden(t, "rehearse-mailbox.stdout.golden", cli(t, fakeKey, rehearseArgs(t, api, "--budget", "10")...).stdout)
	if len(api.created) != 2 {
		t.Errorf("the second run created new batches: %v", api.created)
	}
	raw, err := os.ReadFile(detail)
	if err != nil || !strings.Contains(string(raw), "7,eva@mailinator.com,good,no,disposable_domain,block_disposable;reject_invalid_block_disposable;mailbox_reject_invalid_risky\n") {
		t.Errorf("detail file:\n%s %v", raw, err)
	}
}

func TestRehearseOutcomes(t *testing.T) {
	dir := t.TempDir()
	signups := filepath.Join(dir, "signups.jsonl")
	history := filepath.Join(dir, "history.csv")
	os.WriteFile(signups, []byte(`{"email": "laura@gmail.com", "paid": true}
{"email": "mario@gmail.com", "paid": false}
{"email": "nuria@gmail.com", "paid": false}
{"email": "oscar@gmail.com", "paid": true}
`), 0o600)
	os.WriteFile(history, []byte("email,kind,at\nmario@gmail.com,payment,2026-09-01\nnuria@gmail.com,hard_bounce,2026-09-02\noscar@gmail.com,fraud,2026-09-03\n"), 0o600)
	r := cli(t, "", "rehearse", "--signups", signups, "--history", history, "--outcome-column", "paid")
	if r.code != 0 || !strings.Contains(r.stdout, "4 sign-ups (2 good") || tableRow(t, r.stdout, "block_free") != "4 2 2 0" {
		t.Errorf("column and history must merge (fraud wins, then paid, then hard bounce): exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if r = cli(t, "", "rehearse", "--signups", signups, "--outcome-column", "converted"); r.code != 1 || !strings.Contains(r.stderr, `no "converted" column`) {
		t.Errorf("a named outcome column that does not exist must be refused: exit %d\n%s", r.code, r.stderr)
	}
	os.WriteFile(signups, []byte("{\"email\": \"laura@gmail.com\", \"paid\": \"maybe\"}\n"), 0o600)
	if r = cli(t, "", "rehearse", "--signups", signups, "--outcome-column", "paid"); r.code != 1 || !strings.Contains(r.stderr, "row 1") {
		t.Errorf("an outcome that is not understood must be refused: exit %d\n%s", r.code, r.stderr)
	}
}

func TestUsage(t *testing.T) {
	if r := cli(t, ""); r.code != 2 || !strings.Contains(r.stderr, "salchimail-local clean") {
		t.Errorf("no arguments: exit %d\n%s", r.code, r.stderr)
	}
	if r := cli(t, "", "version"); r.code != 0 || strings.TrimSpace(r.stdout) != version {
		t.Errorf("version: %q", r.stdout)
	}
	if r := cli(t, "", "clean"); r.code != 2 {
		t.Errorf("clean without --list: exit %d", r.code)
	}
}
