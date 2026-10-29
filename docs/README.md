# InstantCapture documentation

This is the canonical documentation index. When information conflicts, use current code and tests first, then current architecture, accepted ADRs, and finally dated plans or references.

## Current authoritative documentation

- [`architecture.md`](architecture.md) — current working-tree architecture and runtime boundaries.
- [`../README.md`](../README.md) — user-facing behavior, settings, and development commands.
- [`../AGENTS.md`](../AGENTS.md) — recurring rules for contributors and coding agents.

## Architecture decisions

- [`adr/README.md`](adr/README.md) — ADR policy and catalog, including the accepted image-transform composition decision.

## Current guides and references

- [`../tools/window-capture-helper/README.md`](../tools/window-capture-helper/README.md) — native WGC/scroll helper build, protocol, and limitations.

## Plans and historical references

- [`window-capture-wgc-printwindow-plan.md`](window-capture-wgc-printwindow-plan.md) — dated 2026-06-05 design plan. Its statement that true window capture is not implemented is stale: the current working tree integrates a WGC helper, while PrintWindow remains unimplemented.

## Inventory scope and exclusions

The inventory covers tracked and non-ignored project-owned Markdown and text references across the repository, including root and tool directories.

Excluded as generated, dependency, tool, runtime, or build state:

- `.git/`, `build/`, `dist/`, `release/`, `_references/`, and `.local/` (git-ignored local notes and third-party reference checkouts).
- Generated executables, object/resource files, icons derived by build tools, and executable-adjacent runtime `config.json`.
- Temporary helper PNG files, caches, logs, and local session artifacts.

Untracked generated outputs should not be treated as canonical documentation or source merely because they are currently present in the working tree.
