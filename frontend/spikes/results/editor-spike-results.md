# Editor spike results

Generated: 2026-08-16T19:31:42.321Z

Decision: **monaco** (only editor without eliminatory gate)

| Editor | Gzip | Workers | Open 1.5 MiB | Typing p95 | Axe | CSP | Gates |
|---|---:|---:|---:|---:|---:|---|---|
| monaco | 2166288 | 6 | 28.7 ms | 2.4 ms | 3 | style-src 'unsafe-inline'; img-src data: | pass |
| codemirror | 158399 | 0 | 7.4 ms | 3.8 ms | 5 | style-src 'unsafe-inline' | accessibility smoke gate exceeded |

Audit vulnerabilities: {"info":0,"low":0,"moderate":0,"high":0,"critical":0,"total":0}

