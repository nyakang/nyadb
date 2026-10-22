# Local MyGo v0.6.0 snapshot (MIT)

This directory is a source-only subset of the public [egoist/mygo](https://github.com/egoist/mygo) Go module, pinned to v0.6.0. Its upstream license is retained in [LICENSE](LICENSE). It exists so NyaDB can build its **native Table subtitle extension reproducibly** without modifying a read-only global module cache or introducing a separate frontend/table framework.

The files are copied from the Go module's imported package set (the Go `mod vendor` selection), plus the upstream `go.mod` and MIT `LICENSE`. Unused website, examples, tests and platform-independent npm artifacts are not imported. NyaDB's `go.mod` has the explicit relative `replace`; no Git history rewrite and no edits to other repositories.

## Local behavioral patch

Only `ui/table.go` is intentionally modified from the selected v0.6.0 source:

- `ui.TableColumn.Subtitle string` is optional, backwards compatible; an empty subtitle keeps the old single-line sizing.
- If any column has a subtitle, the **same** Table header row grows to fit two lines.
- The title and smaller secondary label live inside the *existing keyed header cell*. There is no overlay or secondary scroll container. The existing `ListState.Columns`, `Sort`, `HeaderMenu`, dragging and resizing continue to operate on that same header node.
- The title remains the accessible label for sort and menu; the subtitle is a visual data type hint, not part of the column identity.
- Third-stage refinement: optional `SubtitleIcon` type glyph and `HeaderBadge` attribute marker are independently drawn text in the same header cell. The header height is `t.Space(15)` after 1280×800 reference calibration; data row height and `TableLayout` are unchanged.
- Both fields default to empty and preserve existing native sort, resize, scroll, menu and column reorder behavior. No proprietary icons are used.

Do not change MyGo's generic table state machinery or duplicate NyaDB's data paging services here. When MyGo upstream supports a suitable public header-subtitle API, migrate to an official released module version and remove this snapshot. For upgrades, compare this file against the new upstream `ui/table.go`, port the minimal patch, and rerun native UI and full Go/MyGo gates.

## Validation

NyaDB's `internal/ui/shell/mysql_data_subtitle_test.go` tests the native rendered header and shared resize/scroll/sort/menu/reorder state. `mysql_data_rule_editor_test.go` covers the real data viewer's header menu after switching to separate `Title` / `Subtitle` fields. These are tests in the main module; upstream tests are intentionally not rewritten or presented as completed.
