# bashtransfer — Audit register

## Historical reports
| ID | File | Date | Original verdict | Original counts | Auditor |
|----|------|------|------------------|-----------------|---------|
| V001 | verify_001_2026-10-27.md | 2026-10-27 | FAIL | 2 MAJOR, 3 MINOR | agent-1 (claude-haiku-5-5), self-audit |

Verdicts: PASS, PASS WITH FINDINGS, FAIL, INCOMPLETE. Historical counts/verdicts are not recomputed as findings are fixed; current unresolved findings are below.

## Findings
| ID | Severity | Title | Current status | Closed by / date | Evidence |
|----|----------|-------|----------------|------------------|----------|
| V001-F1 | MAJOR | Benchmark D13 fails on rerun (256 KiB 0.896); P3 closed on one run | OPEN | — | verify_001 §V001-F1; bench rerun log |
| V001-F2 | MAJOR | Completion references ambiguous or missing (P4, 4.1, 4.2) | OPEN | — | `git log` trailers: 70c85be, ae101a1, e2ccd94 |
| V001-F3 | MINOR | 2.1 has no own completion reference (combined commit d64bffb) | OPEN | — | `d64bffb` trailer `PEV-Unit: 2.2` |
| V001-F4 | MINOR | D12, R2 decision impact, phase_05 4.2 and STATE §5 not updated after rev 8 | OPEN | — | grep `bashtransfer.conf`, `proxy_pass` in docs |
| V001-F5 | MINOR | README: bare-host upload is named by local file, not `download` | OPEN | — | live check, local binary |

Statuses: OPEN, FIXED, ACCEPTED (user decision), OBSOLETE (reason recorded). Never silently remove a finding. Update the source report's resolution too.

## Open blockers
- V001-F1 → P3 (BLOCKED pending C1; supervisor decision needed on measurement method or criterion handling).
- V001-F2 → 4.1, 4.2, P4 references (C2 READY; ambiguity option needs supervisor choice).
