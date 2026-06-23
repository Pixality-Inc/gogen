# AGENTS.md

## Project Context

This repository contains `gogen`, a Go code generator used by Pixality projects.
The main generation logic lives in `internal/gen`, configuration structs live in
`internal/config`, and reusable Go template rendering lives in `internal/template`.

## Generator Rules

- Keep `internal/gen` independent from the protobuf runtime. Use
  `github.com/pixality-inc/golang-core/proto_parser` for proto metadata instead.
- Use `github.com/pixality-inc/golang-core/storage` for filesystem access inside
  generators. Avoid direct `os` reads/writes in generator code.
- Generate code through the local `internal/template` package. Add or update
  embedded `.tmpl` files and typed template data structs; do not assemble generated
  files with string builders or ad hoc concatenation.
- It is acceptable to extend `internal/config` when a generator needs more input,
  but keep defaults conservative and backward-compatible.
- `generateSwagger`, `generateApi`, `generateDao`, `generateEnums`, and
  `generateIds` are the core generator entry points.
- ID generation is configured from a separate YAML file, defaulting to
  `gen/ids.yaml` with `ids.yaml` as a fallback unless config overrides it.
  Generated ID files go to `internal/types/<id>_gen.go`.

## Local Workflow

- Prefer `rg` / `rg --files` for searching.
- Use `apply_patch` for manual file edits.
- Run `gofmt` on touched Go files.
- Do not revert or overwrite unrelated user changes in the worktree.
- Before reporting completion for code changes, run:
  - `make lint`
  - `make test`
- If either command cannot be run, say so explicitly in the final response and
  include the reason.
