---
paths:
  - "cmd/**/*.go"
  - "internal/**/*.go"
  - "configs/*.go"
  - "tools/*.go"
  - "go.mod"
  - ".go-arch-lint.yml"
  - ".golangci.yml"
  - "web/**/*.ts"
  - "web/.dependency-cruiser.json"
  - "Makefile"
---

# The dependency rule

Source-code dependencies point **inward only**:

```
cmd  →  adapters  →  core
```

Inner layers must never import outer ones. Concretely:

- The core is grouped **by area** (`internal/core/session`, `gate`, `review`, `push`, `workflow`, `level`, `agent`): business rules and the ports (interfaces) the outside world implements. **No** framework, library, or transport imports — the standard library and other `internal/core/...` packages only — and **no serialization tags** (`json:"..."`): wire formatting is a transport concern the adapters own.
- `internal/core/shared/` — cross-cutting contracts (the logger) used across areas.
- `internal/adapters/` — the only place third-party libraries live: agent protocol clients, the sandbox, git reading and pushing, the page server, agent-host, the egress proxy, and loggers that implement the ports. An adapter depends on the core, never on another adapter.
- `cmd/` — the composition root: it constructs concrete adapters, injects them into the core, and starts the process.

Never import an `internal/adapters/...` package from `internal/core/...`.

## This rule is enforced

The dependency rule above is checked mechanically by `make arch` ([go-arch-lint](https://github.com/fe3dback/go-arch-lint), config in `.go-arch-lint.yml`). A violation **fails the build**. Fix it by moving the offending code to the correct layer — **never** by widening a `mayDependOn` list or adding an `anyVendorDeps` grant to the core. Loosening `.go-arch-lint.yml` to turn a red build green defeats the purpose of the architecture.

If go-arch-lint ever reports a dependency you can't find in your imports, it's almost certainly `deepScan` tracing a dependency-injection call in `cmd` — leave `deepScan: false` (the config enforces the import graph, not data-flow).
The generated CI pipeline (`.github/workflows/ci.yml`) already runs `make arch-check`, so a boundary violation blocks the build — `arch-check` exits non-zero on a violation and also fails if the linter has been silently disabled. Add it to a pre-push hook too if you want the feedback before you push.

The page's TypeScript has module rules of its own, checked by `make web-arch` ([dependency-cruiser](https://github.com/sverweij/dependency-cruiser), config in `web/.dependency-cruiser.json`), which `make web-check` runs, so CI does too: `web/lib/` imports only `lib/` and `types.ts`; `web/components/` only components, `lib/`, `types.ts` and `api.ts`; `types.ts` imports nothing and `api.ts` only `types.ts`; `app-state.ts` only components, `lib/`, `types.ts` and `api.ts`; `composer.ts`, `layout.ts`, `navigation.ts` and `session-actions.ts` only those and `app-state.ts`; `stream.ts` only those, `app-state.ts` and `navigation.ts`; `palette-items.ts` only those, `app-state.ts`, `layout.ts`, `navigation.ts` and `session-actions.ts`; nothing imports `app.ts`; there are no cycles; every import names an existing file by its exact path, in a served folder and with a served name (see [Page modules and stylesheets](web-page.md)); and every module but `types.ts` is reachable from `app.ts`. dependency-cruiser reads TypeScript only through the compiler API of TypeScript before 7 or from 7.1, and the page uses 7.0.2, so the check compiles the page into a temporary folder and checks that JavaScript. `import type` lines compile away, so type-only imports aren't checked (none breaks a rule today), and `types.ts` is exempt from reachability, since most modules import it only for types. It also holds `visible()` and the engine answer's type, the one place both `api.ts` and `lib/` may import, so `request()` escapes the engine's message the way the rest of the page does. Once the page is on TypeScript 7.1 or later, cruise `web/` itself with `tsPreCompilationDeps` on and drop the exemption, and type-only imports are checked too. It refuses to pass when it counts fewer than 80 modules or 250 imports (`WEB_ARCH_MIN_MODULES` and `WEB_ARCH_MIN_IMPORTS` in the `Makefile`; its module count lists each imported module twice, 153 for 85 files today, because the config resolves only exact paths), so a parser that silently finds nothing can't pass. dependency-cruiser runs only on Node 22, 24 or 26 and later. As with `.go-arch-lint.yml`, fix a violation by moving code, never by widening a rule.

## This project's stack
- HTTP: the standard library's `net/http` — the page server in `internal/adapters/http/page`
- Datastore: none
- Logger: `slog` — adapter in `internal/adapters/loggers/slog`

## Adding a feature

1. Put the rule in the core area it belongs to, and the port (interface) it needs next to it.
2. Implement the adapter under the matching `internal/adapters/...` package.
3. Wire the concrete adapter into the composition root in `cmd/main.go`.
4. A new package under `internal/core/`, or an adapter folder outside the globbed categories (`engines/*`, `sandbox/*`, `install/*`, `git/*`, `http/*`, `loggers/*`), attaches to no component in `.go-arch-lint.yml` until you add one, so `make arch` fails until you do (deny-by-default).

## Conventions
- Keep everything under `internal/core/**` free of framework, database, and HTTP imports (`make arch` enforces this).
- Adapters translate between the outside world and domain types; they hold no business rules.
- Run `gofmt`, `go vet ./...`, `go fix ./...` (Go 1.27 modernizers; preview with `make fix-check`), `make lint` (golangci-lint), and `make arch` before committing.
