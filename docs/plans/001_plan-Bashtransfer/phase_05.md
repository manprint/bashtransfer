# Phase 4 — Docker deploy, nginx and documentation

Intent: the service runs with `docker compose` (nginx in front, app behind on an internal network), and the documentation describes installation, use, configuration and limits. Final run of RS-1 through nginx.
Prerequisites: P2 closed or 2.1 SKIPPED with an approved reason (see `phase_03.md`); P3 closed with G-BENCH PASS (`phase_04.md`). Artifacts: `bashtransfer` binary and the e2e script.
Phase closure: P4 and final gate (last phase); review by agent-1 (claude-haiku-5-5, self-review by the same model).

## State and ownership contract
Read STATE.md §0–1 first for scope, ownership, recovery, checks and commit rules.
Open before edits; checkpoint at a recovery boundary; close with evidence. 4.2 depends on R2 (remaining `client_body_timeout`, S1) and on authorized network access for the nginx image: do not assume the nginx configuration is verified until S2 passes.

## Local design context
Plan revision: 2 (RS-2 through nginx; README with two scenarios).
- D12: the server speaks plain HTTP; TLS is handled by nginx. The server uses `X-Forwarded-Proto` and `Host` only for the base URL of the commands, when `BASHT_PUBLIC_URL` is not set.
- D14: multi-stage Dockerfile, static binary `CGO_ENABLED=0`, runtime `distroless/static:nonroot`, image tags pinned to the verified version; `compose.yaml` with `nginx` and `app` on an internal network; `app` publishes no ports.
- R2 (nginx): directives required for unbuffered streaming and unlimited body: `client_max_body_size 0`, `proxy_request_buffering off`, `proxy_buffering off`, `proxy_http_version 1.1`, `proxy_read_timeout` and `proxy_send_timeout` = 3600s (≥ default `BASHT_WAIT_TIMEOUT`; rationale in R2 overview), headers `X-Forwarded-Proto` and `Host`. Directives confirmed from nginx.org (R2 overview); `client_body_timeout` still to be read.
- Auth: as per the outcome of Q1 in `phase_03.md`.

## Sub-phases

### 4.1 Dockerfile, .dockerignore and compose (app service only)
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** write the files; agent-1 review of pinning and absence of secrets in the image.
- **Files:**
  - NEW `Dockerfile`: `builder` stage (Go image pinned to the version in `go.mod` or a later compatible one) with `CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/bashtransfer ./cmd/bashtransfer`; final stage `gcr.io/distroless/static:nonroot` (pinned tag), `COPY --from=builder`, `USER nonroot`, `EXPOSE 8080`, `ENTRYPOINT ["/bashtransfer"]`.
  - NEW `.dockerignore`: `.git`, `docs/`, `bin/`, `scripts/`, `*.test`, `Specifiche.md`.
  - NEW `compose.yaml`: `app` service with `build: .`, `environment` from config variables (no plaintext secrets: `BASHT_AUTH_PASSWORD` from `env_file` or a host variable), `expose: ["8080"]`, `networks: [internal]`, `restart: unless-stopped`. The `nginx` service is in 4.2.
- **Change:**
  Preconditions: P3 closed with G-BENCH PASS; binary compiles (`go build ./cmd/bashtransfer`).
  Contract: the image contains no repository files except the binary; non-root user; no port published by the host for `app`.
  Steps:
  1. S1 — write `Dockerfile` and `.dockerignore`; expected: `docker build` produces an image (**requires local base images or authorized network access**).
  2. S2 — write `compose.yaml` (`app` only); expected: `docker compose -f compose.yaml config` exit 0 and no `ports:` for `app`.
  3. S3 — `docker run --rm` of the binary with `BASHT_LISTEN=:8080` and `GET /` on the test container's internal network; expected: usage text.
  Recovery boundary: after S1.
  Failure handling: if `docker build` fails due to missing network or base images → G-P4 BUILD = blocked, recorded as a blocker, not as a pass. S2 remains runnable offline.
- **Unit tests:** N/A — infrastructure. Discovery check: `docker compose config` shows the `app` service; grep for `ports:` in the `app` service returns nothing.
- **e2e tests:** `docker build` + `docker run` + `scripts/e2e.sh` pointed at the container port; blocked until the network or local images are available. Gate G-D1.
- **Done:** `compose config` pass; `Dockerfile` written; build pass or blocker recorded with reason; agent-1 review recorded; unit closed.

### 4.2 nginx in front of app (R2 largely confirmed; authorized network)
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** write the draft configuration; agent-1 review only after R2 is confirmed.
- **Files:**
  - NEW `deploy/nginx/nginx.conf` (rev 8): `stream` block with `listen 443 ssl` (certificates mounted from the host at `/etc/nginx/certs/`) and `proxy_pass app:8080`, `proxy_timeout 3600s`; `http` block only for the 80→443 redirect. The HTTP proxy is not used for the relay (B-NGX, R2 overview rev 8).
  - WRITE `compose.yaml`: `nginx` service (pinned image), `deploy/nginx` volume and host certificates, ports 80 and 443 published, `internal` network.
- **Change:**
  Preconditions: 4.1 closed; `client_body_timeout` read from nginx.org (S1); pinned nginx image downloadable (authorized network).
  Contract: no unconfirmed directive; every directive in the draft cites its source in STATE §2.
  Steps:
  1. S1 — read `client_body_timeout` from https://nginx.org/en/docs/http/ngx_http_core_module.html and, from the proxy module, verify that with `proxy_request_buffering off` a client `Transfer-Encoding: chunked` body (RS-2) is forwarded to the upstream without buffering; record both in R2 with value, context and version; expected: values and source in the overview.
  2. S2 — write `deploy/nginx/nginx.conf` (stream, rev 8) and the `nginx` service in compose; expected: `nginx -t` pass if the nginx binary is local, otherwise `docker compose config` pass.
  3. S3 — verify RS-1 through nginx (in 4.3 / G-FINAL).
  Failure handling: `client_body_timeout` hinders slow uploads → escalation to agent-1 with tests and numbers; no change to the D7 wait contract.
- **Unit tests:** N/A — configuration. Check: `nginx -t` on the configuration in a container or locally when available.
- **e2e tests:** RS-1 through nginx with a 256 MiB upload without truncation (identical sha256); RS-2 (`cat file | curl -T - .../mydir.tgz`) through nginx with identical sha256; blocked with 4.2.
- **Done:** configuration written with the R2 parameters; `nginx -t` pass (image or local binary); e2e through nginx pass with a 256 MiB upload without truncation (identical sha256); agent-1 review recorded.

### 4.3 Documentation
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** write `README.md` (substantial documentation, separate sub-phase justified). Agent-1 review for accuracy against verified behaviour (`scripts/e2e.sh`, tests).
- **Files:**
  - NEW `README.md` (in Italian, as the project is).
  - READ: `Specifiche.md`, `docs/plans/001_plan-Bashtransfer/overview.md` (decisions), `internal/config/config.go` (variables), `scripts/e2e.sh`.
- **Change:**
  Preconditions: 4.1 closed; 4.2 closed or SKIPPED with reason; 2.1 DONE or SKIPPED (for the auth section).
  README contract: sections in order: what it does; sender and receiver commands for RS-1 (upload from file, URL without a trailing `/` as in Specifiche) and RS-2 (`sudo tar -cpzf - dir | curl -T - https://<host>/dir.tgz`), curl and wget, examples from the real block; `curl -N` only for output on a pipe (R1); configuration (table of variables from `config.go`, with defaults); authentication (scope from Q1); deploy with `docker compose`; nginx (only if 4.2 is closed); limits and security: the slug is the secret, no name in the path → `download` file (R4 case C: `tar ... | curl -T - https://host` without `/name`), no TLS without nginx, anonymous uploads if auth is disabled, one receiver per slug, maximum wait `BASHT_WAIT_TIMEOUT`, inactivity `BASHT_IDLE_TIMEOUT`, no persistence; performance: only numbers from STATE §7 with the measurement machine.
  Steps:
  1. S1 — write the README with the structure above; expected: every variable in the table exists in `config.go`.
  2. S2 — cross-check: every README command is run by the e2e script or marked as unverified.
  Failure handling: an unverified claim must be written as such, not as fact.
- **Unit tests:** N/A — documentation. Check: `grep` of the config variables in the README; no documented variable that does not exist.
- **e2e tests:** N/A — no new behaviour.
- **Done:** README written; cross-check recorded in STATE §7; agent-1 review recorded; unit closed.

## Phase gates and closure
- Required gates: G-D1 (compose config, build if available), G-E2E-FINAL (RS-1 through nginx, blocked with 4.2), G-FINAL (last phase: `go test -race -count=1 ./...`, `scripts/e2e.sh`, `scripts/bench.sh` already PASS in P3, RS-1 through compose+nginx).
- Assertions: RS-1 and RS-2 complete (upload/commands/download identical sha256/410; RS-2 with name `mydir.tgz`). No unconfirmed nginx directive.
- README obligation: 4.3.
- Every sub-phase DONE or SKIPPED with reason. If 4.2 BLOCKED: P4 does not close and `Scope result` stays RUNNING/BLOCKED; never COMPLETE.
- Supervisor agent-1 reviews: nginx directives (4.2), README accuracy (4.3), final closure.
- Closure of P4 and final: unit `P4` in STATE §4; `Scope result: COMPLETE` only if G-FINAL PASS, otherwise `BLOCKED` with the technical blocker recorded in STATE §9.
