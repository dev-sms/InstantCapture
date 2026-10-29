# Agent Instructions

## Project boundaries

- `cmd/instantcapture` is the executable entry point.
- `internal/ui` owns Gio window orchestration and user interaction.
- `internal/workflow` owns capture and edit session state.
- `internal/nativeoverlay` is the primary Windows selector; `internal/overlay` is its Gio fallback.
- `internal/platform/windows`, `internal/tray`, and `internal/hotkey` own OS integration.
- `tools/window-capture-helper` is a separately built C++/WinRT helper.

## Development

- Read [`docs/README.md`](docs/README.md) before relying on plans or historical references.
- Run `build.cmd` for final verification and packaging. It runs Go and helper tests and places executable artifacts only in `dist/`.
- Keep the native and Gio selection overlays behaviorally aligned.
- Do not edit generated executables, resource objects, build output, or runtime configuration as source.

## Documentation maintenance

- Update [`docs/architecture.md`](docs/architecture.md) when package ownership, startup, runtime flows, persistence, integrations, distribution, or security boundaries materially change.
- Follow [`docs/adr/README.md`](docs/adr/README.md) for durable decisions with meaningful alternatives. Do not rewrite accepted ADR history; supersede it.
- Preserve dated plans and historical documents. Record current behavior in canonical documents and mark conflicts in the index.
- Register new project-owned documents in `docs/README.md` or the ADR catalog.
- Before completion, check architecture, ADR, documentation-index, and user-documentation impact.
