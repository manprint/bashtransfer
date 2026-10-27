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
| V001-F2 | MAJOR | Completion references ambiguous or missing (P4, 4.1, 4.2) | OPEN (C2 applied: references recorded; ambiguity `70c85be`/`ae101a1` awaits user decision) | — | STATE §4/§11 reconciliation row; `git log` trailers: 70c85be, ae101a1, e2ccd94 |
| V001-F3 | MINOR | 2.1 has no own completion reference (combined commit d64bffb) | OPEN (C2 applied: mapping `d64bffb` recorded; ACCEPTED needs user decision) | — | `d64bffb` trailer `PEV-Unit: 2.2`; STATE §11 |
| V001-F4 | MINOR | D12, R2 decision impact, phase_05 4.2 and STATE §5 not updated after rev 8 | FIXED | execute verify 001 (C3), 2026-10-27 | D12 rev 8 text; R2 superseded note; phase_05 S2 `deploy/nginx/nginx.conf`; no `location /` in contracts; `bashtransfer.conf` only in history rows |
| V001-F5 | MINOR | README: bare-host upload is named by local file, not `download` | FIXED | execute verify 001 (C3), 2026-10-27 | README line 24 replaced; live check earlier: `curl -o "myfile.zip"` for bare-host upload |

Statuses: OPEN, FIXED, ACCEPTED (user decision), OBSOLETE (reason recorded). Never silently remove a finding. Update the source report's resolution too.

## Open blockers
- V001-F1 → P3 (C1 BLOCKED: decision needed on measurement method or criterion handling; not run).
- V001-F2 → 4.1, 4.2, P4 references (C2 applied; user decision needed: ACCEPTED for the recorded ambiguity, or local amend of `ae101a1`).
- V001-F3 → 2.1 mapping (C2 applied; user decision needed: ACCEPTED or not).
