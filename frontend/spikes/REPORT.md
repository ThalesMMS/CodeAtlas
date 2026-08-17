# Monaco vs CodeMirror 6

Decision: **monaco**

| Criterion | Monaco | CodeMirror |
|---|---:|---:|
| Gzip bytes | 2166288 | 158399 |
| Worker count | 6 | 0 |
| Open 1.5 MiB ms | 28.7 | 7.4 |
| Typing p95 ms | 2.4 | 3.8 |
| Axe violations | 3 | 5 |
| CSP relaxations | style-src 'unsafe-inline'; img-src data: | style-src 'unsafe-inline' |
| Gate result | pass | accessibility smoke gate exceeded |

Raw compact JSON: `frontend/spikes/results/editor-spike-results.json`

Audit vulnerabilities: {"info":0,"low":0,"moderate":0,"high":0,"critical":0,"total":0}

