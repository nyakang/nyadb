# NyaDB application icon

`icon.svg` is the editable cat-and-database logo supplied by the user on
2026-11-06 as `temp/nyadb-logo-transparent.svg`. Its paths, color `#05E6C1`
and transparent background are retained unchanged. No additional license
declaration is assigned to this user-provided artwork.

`icon.png` is the generated 1024×1024 RGBA application icon. The artwork is
trimmed, scaled proportionally to a longest edge of 960 pixels, and centered
on the transparent canvas. The 32-pixel top and bottom padding keeps the
mark clear of the edges while making it more legible at small display sizes.
It has no added background, outline or shadow.

MyGo packaging reads this PNG via `mygo.json`. The `appicon` package embeds
the same file for native startup, including direct development runs. The
connection editor continues to use the separate database brand icons.

To regenerate with Python 3 and ImageMagick 7:

```sh
python3 tools/generate_icon.py
```

The script resolves paths relative to itself and can run from any working
directory. The initial PNG was rendered with ImageMagick 7.1.2-18. Commit
the SVG and generated PNG together when updating the artwork; normal
Go/MyGo builds require neither Python nor ImageMagick.
