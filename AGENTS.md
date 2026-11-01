# AGENTS.md

These instructions apply to the whole repository.

## Project and structure

Bashtransfer uses Go and the standard library for a streaming HTTP relay, usable from a terminal or a browser. The frontend uses vanilla HTML, CSS and JavaScript, rendered with `html/template` and embedded in the binary with `go:embed`.

- `cmd/bashtransfer/`: startup, server configuration, signals and healthcheck.
- `internal/config/`: `BASHT_*` variables and validation.
- `internal/relay/`: sessions, concurrency, presence, readiness and stream copying.
- `internal/server/`: HTTP protocol, authentication, commands and rendering.
- `internal/server/pages/`: `layout.html`, `home.html`, `download.html`, `message.html`.
- `scripts/`: e2e tests, source invariant check and measurements; `scripts/ui/` contains Playwright.
- `deploy/`, `compose*.yaml`, `Dockerfile`: deployment and proxy.
- `docs/`: architecture and operations; `docs/plans/` contains planning artifacts, not to be rewritten for ordinary changes.

## Invariants to preserve

- No file storage on the server, not even in RAM: only session metadata and bounded buffers in transit. The sender's body is read for the relay, not for a preliminary upload.
- One receiver per link. Landing pages, previews and status checks must not claim the session; the actual claim remains atomic.
- No byte resumption or segmented parallel download. Grace period and reconnection before the relay preserve the session, not the file.
- Session and progress stream remain distinct. Before the relay, the body is replaced under lock; after `BeginRelay` only control is recovered, without repeating the file.
- The registry's mutable state remains protected by its mutex. Only the sender's goroutine writes to the receiver; interruptions, expirations and failed flushes must propagate an error outcome.
- READY derives from the server's confirmation and expires; do not infer it from the initial HTML, `navigator.onLine`, bytes accepted by the browser or upload callbacks. Share remains available before pairing; Download requires confirmation and must not restart after submit.
- Preserve older clients: presence and readiness are negotiated. Do not add NDJSON events or change the semantics of the legacy check for clients that do not request them.
- Do not alter the source: preserve quoting, sanitization, download commands and the `Content-Disposition` header. Do not replace the commands with outputs that overwrite the original file.
- Do not log slugs, file names, tokens or credentials; use the session's random ID instead. Preserve tokens in headers, Basic auth on upload, and a CSP with no external resources.

## Frontend

- Modify the existing templates and reuse the shared styles and scripts in `layout.html`. Do not introduce frameworks, bundlers, CDNs or production dependencies without an explicit requirement.
- Keep the interface in English, the shared colors (`--send`, `--recv`, `--err`), the light/dark theme, and the consistency of Sender/Receiver and READY/UNREADY.
- Preserve desktop and mobile layouts at 1280, 390 and 320 px, no horizontal scrolling, and touch controls of at least 44 px on mobile.
- Handle Web Share/Clipboard fallbacks, visibility, network, timeouts and stale asynchronous responses. Sharing sends only the URL, never files or tokens.
- A template change requires rebuilding and restarting the binary. Node.js and Playwright are only needed for tests; there are no npm builds or servers for the app.

## Verification

Use the versions and dependencies specified in the manifests and CI. Prefer checks relevant to the change; do not install or update tools unless necessary.

```bash
gofmt -l .                    # empty output; format only the modified Go files
go vet ./...
go test -race -count=1 ./...
bash scripts/e2e.sh           # includes the source invariant check locally
node --check scripts/ui/ui.mjs
bash scripts/ui/run.sh direct
bash scripts/ui/run.sh apache
```

To prepare the browser tests, if the dependencies are missing:

```bash
(cd scripts/ui && npm ci && npx playwright-core install --no-shell chromium)
```

Add `--with-deps` only if Chromium's system libraries are missing. The runner also requires Bash, Python 3 and curl; Apache requires Docker. Before the Apache mode, verify that the `bt-ui-*` containers and the `bt-ui-net` network are not used by other tests: the runner recreates and removes them.

For targeted tests, use `ONLY`, for example:

```bash
ONLY='peer readiness|sender grace' bash scripts/ui/run.sh direct
```

Use `BASHT_UI_OUT` to keep screenshots and diagnostics outside the tracked files. Update existing regression tests for new or corrected behavior; also verify Apache when the protocol, presence or lifecycle changes. For documentation-only changes, do not run the application suite.

## Documentation and collaboration

Read [README.md](README.md), [docs/architettura.md](docs/architettura.md) and [docs/operazioni.md](docs/operazioni.md) for usage, protocol and deployment. Keep the active documents aligned when behavior, configuration or commands change; distinguish automated tests from checks on real phones.

Make targeted changes, preserve existing work, and do not restore files deleted by the user. Do not add secrets, temporary notes or test artifacts to the repository. Follow the user's instructions on commits and pushes; do not publish or modify infrastructure without a request.
