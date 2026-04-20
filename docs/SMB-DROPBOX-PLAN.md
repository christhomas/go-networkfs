# SMB and Dropbox — implementation plan

Both are P1 items from the main roadmap. This doc grades the specific
difficulty of each and sequences the work so the low-hanging fruit
lands first.

## TL;DR

Dropbox before SMB. Dropbox is HTTP-based, so we can mock the whole
server side with `httptest` and get real CI coverage. SMB has no
embeddable Go server and needs either a Docker sidecar or a skipped-by-
default integration test behind a build tag.

| Driver  | Library                                     | Test strategy                      | Grade  |
|---------|---------------------------------------------|------------------------------------|--------|
| Dropbox | `dropbox/dropbox-sdk-go-unofficial/v6`      | httptest + injected http.Client    | **M**  |
| SMB     | `hirochachacha/go-smb2`                     | unit tests + build-tagged live IT  | **M-H**|

## Dropbox — Medium

### Why it's doable

The Dropbox SDK's `dropbox.Config` exposes a `Client *http.Client`. We
plug in a custom `http.Client` whose `Transport` routes every request
to an `httptest.Server` that speaks enough Dropbox JSON to exercise
the driver. No real Dropbox account or token required.

### Scope for this pass

Implement and test these operations against the httptest mock:

- `Mount` — config: `token` (required), `root` (optional), `endpoint`
  (optional, for tests; overrides the production URLs)
- `Stat` — `/2/files/get_metadata`
- `ListDir` — `/2/files/list_folder`
- `Mkdir` — `/2/files/create_folder_v2`
- `Remove` — `/2/files/delete_v2`
- `Rename` — `/2/files/move_v2`
- `OpenFile` — `/2/files/download` (content endpoint)
- `CreateFile` — `/2/files/upload` (content endpoint)

All of the above are single-call operations. Chunked uploads
(`upload_session_*`) for files > 150 MB are not in this pass — they
add a state-machine without much payoff for MVP.

### Test surface

- Unit: `abs`, `joinPath`, `boolConfig`, `mapDropboxError`, config
  validation.
- Integration vs. httptest: every op listed above, plus auth failure
  (401 from the mock), not-found, and streaming upload/download with a
  payload ≥ 64 KiB.

Aim for ≥ 80 % package coverage.

## SMB — Medium-Hard

### Why it's hard

There is no maintained embeddable SMB server in pure Go. The options:

1. **Docker sidecar (`samba` image)** in CI — works, but adds a ~200
   MB image pull and a Docker-in-CI requirement. Heavy for a unit
   test suite.
2. **Build-tagged integration test** requiring `SMB_HOST` / `SMB_USER`
   / `SMB_PASS` / `SMB_SHARE` env vars — run locally or against a
   long-running test share. Skipped by default.
3. **Mock at the `go-smb2` interface level** — but the adapter logic
   is so thin that mocking proves almost nothing. Not worth it.

For this pass: take option 2. Ship full driver code, unit-test the
helpers, and guard the real-share integration tests with
`//go:build smb_integration`. Document how to run them in
`docs/DRIVERS.md` and the CHANGELOG.

### Scope

- Dial + SMB2 session negotiation
- Share mount via `Session.Mount(share)`
- Standard file ops via `smb2.Share`
- Config: `host`, `port` (default 445), `share` (required), `user`,
  `pass`, `domain`, `dial_timeout`

### Test surface

- Unit: `abs`, `joinPath`, `boolConfig`, path validation, config
  validation, not-connected guards, error mapping.
- Build-tagged integration: the same checklist as FTP/SFTP/WebDAV.

Aim for package coverage on the testable parts only — integration
tests do not count toward the CI baseline because they don't run by
default.

## Execution order

1. Write this plan (done).
2. **Dropbox driver**: add dep, implement, httptest-backed tests,
   commit.
3. **SMB driver**: add dep, implement, unit tests, build-tagged
   integration, commit.
4. Rebuild all five c-archives to confirm no cgo regressions.
5. Update TUI driver schemas if any field changed (Dropbox adds
   optional `endpoint`; SMB fine as-is).
6. CHANGELOG.md entries for both.
7. README table flips both rows to "Implemented".

Each of (2) and (3) is its own commit. If either blocks on a problem I
can't resolve autonomously, I stop at the last green state — the user
said so explicitly.
