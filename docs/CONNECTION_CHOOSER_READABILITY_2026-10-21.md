# Connection chooser readability pass — 2026-10-21

This is a user-directed NyaDB usability adjustment following original Navicat
18 runtime analysis. It is **not** a claim of pixel-perfect reproduction.

## Behavior

- Removed the permanent **重置** button from the chooser filter sidebar.
  A user can uncheck the same filter to undo it; search has its own clear
  affordance. Navicat's observed sidebar does not use a permanent Reset button.
- The NyaDB tree list now uses **25-DIP** rows instead of the original-inspired
  19-DIP rows, **18-DIP** icons instead of 16-DIP, and larger horizontal gaps
  and nesting offsets. Section headings gain a little vertical space.
- Grid mode, selection, independent provider expansion, two-pane search,
  unsupported-driver restrictions and the fixed window header/footer remain
  unchanged.

## Validation

- UI-tester regression covers reversible checkbox filters with no Reset
  button, and an explicit minimum comfortable tree-row height.
- `go test ./...`, `go vet ./...`, `go tool mygo vet` and
  `go tool mygo build` all passed.
- Linux 1600×960 Xvfb/Openbox: the native chooser rendered with the enlarged
  rows and no Reset button. Huawei expansion and scrolling kept the search
  header and action footer visible.

Evidence screenshots are ignored research-only files under
`temp/research/gui-chooser-comfort-20261021/`:

- `chooser-grid.png` (captures the **list** state; legacy filename)
- `huawei-expanded.png`
- `list-scrolled.png`

## Evidence categories

**O:** Navicat 18's native list uses compact ~19px rows and no permanent
sidebar Reset button.

**I:** The NyaDB-specific spacing adjustment and button removal were
implemented, tested and checked in an actual native GUI window.

**V:** No pixel parity claim: these larger NyaDB rows intentionally depart
from the original to prioritize readability.
