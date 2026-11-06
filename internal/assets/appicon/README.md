# NyaDB application icon

`icon.svg` is the editable cat-and-database logo supplied by the user on
2026-11-06 as `temp/nyadb-logo-transparent.svg`, originally colored `#05E6C1`.
At the user's request, the application icon now uses fixed blue `#009DFF`.
Its paths and transparent background are retained unchanged. No additional
license declaration is assigned to this user-provided artwork.

`icon.png` is the generated 1024×1024 RGBA application icon. The artwork is
trimmed, scaled proportionally to a longest edge of 960 pixels, and centered
on the transparent canvas. The 32-pixel top and bottom padding keeps the
mark clear of the edges while making it more legible at small display sizes.
It has no added background, outline or shadow.

MyGo packaging reads this PNG via `mygo.json`. The `appicon` package embeds
the same file for native startup, including direct development runs. The
connection editor continues to use the separate database brand icons.

## Color policy

The application icon uses fixed `#009DFF` across development runs, native
startup, desktop and taskbar icons, and installation packages. All use
the same PNG source; packaging may convert platform sizes and icon formats.
Color schemes, light/dark mode, the system accent and high-contrast
preferences do not recolor this asset. Keep the original shape, transparent
background and 960-pixel artwork size.

In-app operation icons continue to use the current theme's semantic colors
and existing normal, disabled, selected and dangerous-action states.
Database and provider logos retain their own brand colors; shared bitmap
caches must not be recolored. No extra color setting or runtime recoloring
is introduced. See the [theme specification](../../../docs/THEME_SYSTEM.md#图标颜色职责).

## Regeneration

To regenerate with Python 3 and ImageMagick 7:

```sh
python3 tools/generate_icon.py
```

The script resolves paths relative to itself and can run from any working
directory. The initial PNG was rendered with ImageMagick 7.1.2-18. Commit
the SVG and generated PNG together when updating the artwork; normal
Go/MyGo builds require neither Python nor ImageMagick.
