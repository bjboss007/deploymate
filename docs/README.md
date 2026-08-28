# DeployMate Documentation

- **[decisions/](decisions/)** — architecture decision records (ADRs), numbered
  in the order they were made. Every non-obvious choice in the codebase has
  one: read 0001 first.
- **[knowledge/](knowledge/)** — how the system works and how to operate it:
  architecture, deploy flow, database semantics, dev environment quirks,
  security posture, troubleshooting.
- **[improvements.md](improvements.md)** — the backlog: known gaps,
  prioritized improvements, and future ideas.

## Reading order for a new contributor

1. `knowledge/architecture.md` — the whole system on one page
2. `knowledge/dev-environment.md` — get it running locally
3. `knowledge/deploy-flow.md` — the heart of the product
4. ADRs, as questions come up

## Conventions

- Dates in ADRs are decision dates (all initial ones: 2026-08-28).
- ADR statuses: `accepted` (in the code), `superseded` (points at the
  replacement). Rejected options are recorded under *Alternatives* so future
  readers know what was deliberately not chosen.
- **Improvements must live in `improvements.md`** — if you notice a gap
  while working, add it there rather than fixing it silently in scope.
