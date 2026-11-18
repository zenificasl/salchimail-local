# Third-party notices

## `disposable_domains.txt`

The same list the SalchiMail API uses in `basic` mode, so a local answer matches the API's.

- **Source:** <https://github.com/disposable-email-domains/disposable-email-domains>, file `disposable_email_blocklist.conf`.
- **Commit:** `914da7e670fa820c0b08663f6e9922a3b12c1ed4` (2026-11-17).
- **Content:** 10,299 domains, SHA-256 `07a0e7964bc991301a5e3802657cb1ec417c19f22e274cc1d1e6a17b1ebd7f38`.
- **License:** CC0 1.0 Universal (public domain dedication).

The list is embedded at build time: updating it means publishing new binaries, with the commit, count and hash above updated.

There is no other third-party code: the tool uses only the Go standard library.
