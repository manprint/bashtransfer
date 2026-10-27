# bashtransfer — Audit register

## Historical reports
| ID | File | Date | Original verdict | Original counts | Auditor |
|----|------|------|------------------|-----------------|---------|
| V001 | verify_001_2026-10-27.md | 2026-10-27 | FAIL | 2 MAJOR, 3 MINOR | agent-1 (claude-haiku-5-5), self-audit |

Verdicts: PASS, PASS WITH FINDINGS, FAIL, INCOMPLETE. Historical counts/verdicts are not recomputed as findings are fixed; current unresolved findings are below.

## Findings
| ID | Severity | Title | Current status | Closed by / date | Evidence |
|----|----------|-------|----------------|------------------|----------|
| V001-F1 | MAJOR | Benchmark D13 fails on rerun (256 KiB 0.896); P3 closed on one run | FIXED | utente 2026-10-27 (criterio mediana delle sessioni); C1 4 sessioni, mediana 256 KiB 0.989; P3 attempt 2 | STATE §7 G-BENCH V001-C1; §8; README Prestazioni; `go test -race` 36 PASS |
| V001-F2 | MAJOR | Completion references ambiguous or missing (P4, 4.1, 4.2) | ACCEPTED (decisione utente 2026-10-27: riferimento `70c85be`; `ae101a1` invariato) | utente, 2026-10-27 | STATE §4/§11; `git log` trailers: 70c85be, ae101a1, e2ccd94 |
| V001-F3 | MINOR | 2.1 has no own completion reference (combined commit d64bffb) | ACCEPTED (decisione utente 2026-10-27: 2.1 riferito a `d64bffb`, deviazione rev 6) | utente, 2026-10-27 | `d64bffb` trailer `PEV-Unit: 2.2`; STATE §11 |
| V001-F4 | MINOR | D12, R2 decision impact, phase_05 4.2 and STATE §5 not updated after rev 8 | FIXED | execute verify 001 (C3), 2026-10-27 | D12 rev 8 text; R2 superseded note; phase_05 S2 `deploy/nginx/nginx.conf`; no `location /` in contracts; `bashtransfer.conf` only in history rows |
| V001-F5 | MINOR | README: bare-host upload is named by local file, not `download` | FIXED | execute verify 001 (C3), 2026-10-27 | README line 24 replaced; live check earlier: `curl -o "myfile.zip"` for bare-host upload |

Statuses: OPEN, FIXED, ACCEPTED (user decision), OBSOLETE (reason recorded). Never silently remove a finding. Update the source report's resolution too.

## Open blockers
Nessuno. V001-F1, F2, F3 chiusi con decisione utente del 2026-10-27.
