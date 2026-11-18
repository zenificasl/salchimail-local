# salchimail-local

Verify an email list **on your own machine first**, and pay only for the addresses where a mailbox check can still tell you something.

`salchimail-local` is a single static binary. It resolves what it can locally — syntax, your own sending history, disposable and legacy domains, DNS — and asks the [SalchiMail](https://salchimail.com) API only about the rest. Your history never leaves your machine.

Guide: [English](https://docs.salchimail.com/local-first) · [Español](https://docs.salchimail.com/es/verificacion-local-primero)

## Install

Download the binary for your platform from the [releases page](https://github.com/zenificasl/salchimail-local/releases) (or from <https://docs.salchimail.com/tools/>) and check it against `SHA256SUMS`:

```sh
sha256sum --check --ignore-missing SHA256SUMS   # macOS: shasum -a 256 --check --ignore-missing SHA256SUMS
chmod +x salchimail-local-linux-amd64
```

Or build it with Go 1.24 or later:

```sh
go install github.com/zenificasl/salchimail-local@latest
```

The macOS and Windows binaries are not code-signed yet, so the browser or the OS may warn when you download or open them.

## Use

```sh
# See what would be sent to the API, without sending anything
salchimail-local clean --list list.csv --history history.csv --dry-run

# Spend at most 500 mailbox credits
export SALCHIMAIL_API_KEY=sk_live_...
salchimail-local clean --list list.csv --history history.csv --budget 500 --out results.csv

# Replay your past sign-ups against each sign-up rule before turning it on
salchimail-local rehearse --signups signups.csv --history history.csv
```

Run any command with `-h` for all its flags. `--lang es` prints reasons and the summary in Spanish.

Exit codes: `0` success, `1` error, `2` bad usage, `3` a batch is still running (run the same command again to collect it).

## What it does

| Step | Where | What it resolves |
| --- | --- | --- |
| 1. Syntax | Your machine | `syntax_invalid`, `address_unsupported` |
| 2. Your history (`--history`) | Your machine | The newest unexpired event wins: `reply`, `click` (180 days), `open`, `delivered` (90 days) → `send`; `hard_bounce` (365 days) → `do_not_send`; `soft_bounce` (90 days) decides nothing |
| 3. Lists | Your machine | `disposable_domain`, `legacy_domain` |
| 4. DNS | Your machine's resolver (only the domain name travels) | `domain_null_mx`, `domain_no_mail_host` |
| 5. Wave 1 | API, `mailbox` mode | One address per domain |
| 6. Wave 2 | API, `mailbox` mode | The rest, only for domains whose mail server tells mailboxes apart |

If the first address of a domain comes back `catch_all`, with a domain-wide fact, or as `unknown` from the server, the other addresses of that domain are not sent: they inherit that answer, marked with source `api_domain`.

Every output row says what to do (`send`, `do_not_send`, `review`, `not_checked`), where the answer came from (`own_history`, `local_rules`, `local_dns`, `api`, `api_domain`) and why. `unknown` is never treated as invalid.

`rehearse` applies steps 1, 3 and 4 (and, with `--budget`, the waves) to your past sign-ups, and counts for each rule how many sign-ups it would have blocked, how many of them were good (paid or activated) and how many were junk (marked as fraud or hard-bounced in your own history).

## Guarantees

- **Nothing is sent to the API without `--budget`.** `--dry-run` never makes a request and lists the exact addresses that would be sent.
- **Budget:** each address sent costs at most one credit, and no more addresses are sent than credits remain. With a live key the account balance (`GET /usage`) caps the budget; test keys are never charged. `unknown` results are not charged.
- **Idempotency:** each batch's `Idempotency-Key` is a hash of its content and of the key's mode (test or live), so repeating the command repeats the same batch. The state file (`--state`, mode 0600) holds batch ids, hashes of their contents and the run's credit allowance, never addresses in clear. It lets an interrupted run resume with the allowance it started with, and is deleted once every result is in. The hash of a one-address batch can be checked against a guessed address, so treat the file as you treat the list.
- **Files:** `--out` is opened before anything is sent, is created with mode 0600, and cannot be one of the input files. Cells that would start a spreadsheet formula are prefixed with `'`.
- **No redirects:** the client never follows an HTTP redirect, so the key and the addresses only go to the URL you chose.
- **Rate limits:** batches of `limits.max_batch_size`, one after another; status polled at most every 15 s (shorter only against a server on `localhost`); `429` with `Retry-After` and `5xx` retried up to 5 times; `401`, `402`, `403` and daily caps stop the run without losing what was resolved locally.
- **API key:** read only from `SALCHIMAIL_API_KEY`, sent only over HTTPS (plain HTTP only to `localhost`, for tests), never printed.
- **No telemetry.** The only connections are to your machine's DNS resolver and to the API.

## Why Go

One static binary per platform, nothing to install: your stack may be PHP, Python or nothing at all, and an agent only has to download one file. Standard library only, so there are no dependencies to audit or to be compromised, and the disposable-domain list is built in. It is the language of the SalchiMail engine, so syntax rules and lists are the ones the API applies in `basic` mode.

## Known limits

- The history expiry times are starting values, not yet calibrated against real outcomes.
- `delivered` means "accepted without a bounce", not "reached the inbox". Opens and clicks can be automatic (security scanners, Apple Mail Privacy Protection).
- One address decides for its whole domain. A passing `unknown` on that address leaves the rest of the domain unchecked until the next run.
- Local DNS cannot tell "the domain does not exist" from "it has no records": both are reported as `domain_no_mail_host`.
- Aliases are not normalised (`+tag`, Gmail dots): addresses are compared lower-cased.
- Batches in the same wave are processed one at a time.
- Input files are read whole into memory; split very large lists.
- `row` in the output counts data rows; blank lines in the input are skipped and not counted.
- If you cancel a batch through the API, the next run sends its addresses again in a new batch.
- A DNS filter that answers NXDOMAIN for domains it blocks (Pi-hole and the like) makes those domains look dead; use `--no-dns` on such networks.

## Development

```sh
go test ./...                # unit tests and integration tests against a fake API and DNS
go build -o salchimail-local .
./release.sh                 # binaries for every platform in dist/, with SHA256SUMS
```

`testdata/` is **synthetic**: made-up people, on `.example` domains except a few well-known public ones (`gmail.com`, `mailinator.com`, `wanadoo.es`) that exercise the built-in lists. Tests never send them anywhere. The `*.golden` files are the tool's output on that data with a fake DNS and a fake API; they measure nothing real. Regenerate them with `go test -update ./...`.

Releases are built by `.github/workflows/release.yml` when a `v*` tag is pushed. Builds are reproducible: no cgo, `-trimpath`, no VCS stamp.

Issues and pull requests are welcome. Report security problems privately, as described in [SECURITY.md](SECURITY.md).

## License

[Apache License 2.0](LICENSE). Copyright 2026 ZENIFICA S.L. The built-in disposable-domain list is CC0 1.0; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

The license does not grant permission to use the SalchiMail name or logo (Apache-2.0, section 6).
