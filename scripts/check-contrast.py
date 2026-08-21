#!/usr/bin/env python3
"""Every colour pair in the UI, against WCAG AA, in both themes.

    make contrast          # or: python3 scripts/check-contrast.py

**It reads web/app/globals.css rather than holding its own copy of the palette.**
A transliterated set of hexes would pass while the shipped one drifted, which is
the only failure mode worth designing against here — the same argument
web/scripts/check-lists.mjs makes for importing the modules it checks.

What it cannot do is find the pairs. Which foreground lands on which background
is a fact about the markup, so PAIRS below is written by hand and is the part to
extend when a new surface appears. A token that no pair names is not checked.

This is NOT in `make check`, deliberately. The gate already requires Go and
node; python3 is a third language to install, and this guards a file that
changes rarely. Run it when you touch a colour — several pairs clear their floor
by under 0.3, so "it still looks fine" is not evidence.

Exit 1 if any pair fails.
"""

import pathlib
import re
import sys

CSS = pathlib.Path(__file__).resolve().parent.parent / "web" / "app" / "globals.css"

# (foreground, background, floor, what it is)
#
# 4.5:1 is AA for body text. 3:1 is WCAG 1.4.11, and it applies only to a
# boundary that IDENTIFIES a control — an input's outline is the whole
# affordance, a table rule and a panel edge are decoration and owe nothing.
PAIRS = [
    ("text", "bg", 4.5, "body text on the page"),
    ("text", "panel", 4.5, "body text in a panel"),
    ("text", "bg-sunk", 4.5, "text in a well or input"),
    ("text-2", "panel", 4.5, "secondary text in a panel"),
    ("text-2", "bg", 4.5, "secondary text on the page"),
    ("muted", "bg", 4.5, "muted text on the page"),
    ("muted", "panel", 4.5, "muted text in a panel"),
    ("muted", "bg-sunk", 4.5, "muted text in a well"),
    ("accent", "panel", 4.5, "accent link in a panel"),
    ("accent", "bg", 4.5, "accent link on the page"),
    ("accent", "accent-soft", 4.5, "active nav pill, ok badge"),
    ("accent-hover", "panel", 4.5, "accent link, hovered"),
    ("warn", "warn-soft", 4.5, "warn badge and banner"),
    ("warn", "panel", 4.5, "WARN in the log"),
    ("error", "error-soft", 4.5, "error badge and banner"),
    ("error", "panel", 4.5, "ERROR in the log"),
    ("accent-ink", "accent", 4.5, "the primary button's label"),
    ("border-control", "panel", 3.0, "input outline in a panel"),
    ("border-control", "bg", 3.0, "input outline on the page"),
    ("border-control", "bg-sunk", 3.0, "input outline in a well"),
    ("accent", "bg", 3.0, "focus ring on the page"),
    ("accent", "panel", 3.0, "focus ring in a panel"),
    ("accent", "bg-sunk", 3.0, "focus ring on a well"),
]


def channel(value: int) -> float:
    c = value / 255
    return c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4


def luminance(hex_colour: str) -> float:
    h = hex_colour.lstrip("#")
    if len(h) == 3:
        h = "".join(c * 2 for c in h)
    r, g, b = (int(h[i:i + 2], 16) for i in (0, 2, 4))
    return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)


def ratio(a: str, b: str) -> float:
    la, lb = luminance(a), luminance(b)
    return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)


def palettes(css: str) -> dict[str, dict[str, str]]:
    """The light and dark token tables, read out of the stylesheet.

    Light is every `--name: #hex` before the dark media query; dark is the
    subset the media query redefines, layered on top of light — which is exactly
    how the cascade resolves them, and why a token defined only in :root is
    correctly shared by both.
    """
    marker = css.find("@media (prefers-color-scheme: dark)")
    if marker == -1:
        sys.exit("no dark-mode block in globals.css — has the theming changed?")

    def tokens(chunk: str) -> dict[str, str]:
        return dict(re.findall(r"--([a-z0-9-]+):\s*(#[0-9a-fA-F]{3,8})\s*;", chunk))

    light = tokens(css[:marker])
    dark = {**light, **tokens(css[marker:])}
    return {"LIGHT": light, "DARK": dark}


def main() -> int:
    css = CSS.read_text()
    failed = 0

    for theme, palette in palettes(css).items():
        print(f"\n{theme}")
        for fg, bg, floor, what in PAIRS:
            missing = [n for n in (fg, bg) if n not in palette]
            if missing:
                print(f"  MISSING  --{' --'.join(missing)}  ({what})")
                failed += 1
                continue

            got = ratio(palette[fg], palette[bg])
            ok = got >= floor
            failed += not ok
            print(
                f"  {'ok  ' if ok else 'FAIL'} {got:5.2f} / {floor:.1f}  "
                f"{fg} on {bg}  — {what}"
            )

    if failed:
        print(f"\n{failed} pair(s) below the floor.")
        return 1
    print(f"\nall {len(PAIRS) * 2} pairs clear WCAG AA in both themes.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
