# Frontend third-party notices

Pinned dependency metadata is in `package-lock.json`. Run
`npm run audit:high` for the high/critical vulnerability visibility gate.

Direct development dependencies (none are imported as application runtime
dependencies):

| Package | Version | License | Purpose |
|---|---:|---|---|
| `vite` | 8.1.0 | MIT | frontend build tool and dev server |
| `typescript` | 6.0.3 | Apache-2.0 | typechecking |
| `@types/node` | 26.0.1 | MIT | Node typings for build/test scripts |

The isolated `frontend/spikes/` package carries its own notices and Monaco
dependency. ADR-010 still requires a fresh audit and explicit runtime
integration review before Monaco can enter the production package.
