"""Capture the screenshots embedded in docs/users/ from the docs-demo instance.

Run `scripts/docs-demo/seed.py` first; see docs/dev/docs-screenshots.md for the
servers both scripts expect. Pass image names to re-capture only some of them.
(Versions pinned like every other install step -- see docs/dev/supply-chain.md.)

    uv run --isolated --with playwright==1.55.0 --with pillow==11.3.0 scripts/docs-demo/shots.py
    uv run --isolated --with playwright==1.55.0 --with pillow==11.3.0 scripts/docs-demo/shots.py home
"""

from __future__ import annotations

import os
import pathlib
import sys

from PIL import Image
from playwright.sync_api import sync_playwright

WEB = os.environ.get("DOCS_DEMO_WEB", "http://localhost:3120")
OUT = pathlib.Path(__file__).resolve().parents[2] / "docs" / "users" / "images"
OUT.mkdir(parents=True, exist_ok=True)

# name -> (path, optional scroll-y before the shot, optional wait selector)
SHOTS = [
    ("home", "/", 0, None),
    ("models-list", "/models", 0, None),
    ("dataset-overview", "/datasets/admin/imdb-reviews", 0, None),
    ("file-tree", "/datasets/admin/imdb-reviews/tree/main/data", 0, None),
    ("file-edit", "/datasets/admin/imdb-reviews/edit/main/README.md", 0, None),
    ("commit-history", "/datasets/admin/imdb-reviews/commits/main", 0, None),
    (
        "dataset-viewer",
        "/datasets/admin/imdb-reviews/viewer/main/data/train.parquet",
        0,
        None,
    ),
    (
        "model-metadata",
        "/models/acme/sentiment-base/blob/main/model.safetensors",
        0,
        None,
    ),
    (
        "experiment-charts",
        "/experiments/acme/training-runs/sentiment-finetune",
        0,
        None,
    ),
    (
        "experiment-runs",
        "/experiments/acme/training-runs/sentiment-finetune",
        1150,
        None,
    ),
    ("org-members", "/orgs/acme/settings/members", 0, None),
    ("settings-tokens", "/settings/tokens", 0, None),
]

only = sys.argv[1:] or None

with sync_playwright() as pw:
    browser = pw.chromium.launch()
    ctx = browser.new_context(
        viewport={"width": 1440, "height": 900},
        device_scale_factor=2,
        color_scheme="light",
        locale="en-US",
    )
    ctx.add_cookies([{"name": "tf_locale", "value": "en", "url": WEB}])
    page = ctx.new_page()
    page.add_init_script(
        """
        const css = `nextjs-portal, [data-nextjs-dev-tools-button],
                     #__next-dev-overlay, [data-nextjs-toast] { display: none !important; }`;
        document.addEventListener('DOMContentLoaded', () => {
            const el = document.createElement('style');
            el.textContent = css;
            document.head.appendChild(el);
        });
        """
    )

    # Log in so authenticated pages render.
    page.goto(f"{WEB}/login", wait_until="networkidle")
    # The form fields carry no name/id; autocomplete is what identifies them.
    page.fill('input[autocomplete="username"]', "admin")
    page.fill('input[type="password"]', "admin")
    page.click('button[type="submit"]')
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1500)
    if "/login" in page.url:
        raise SystemExit(
            f"login failed, still at {page.url}: {page.inner_text('body')[:300]}"
        )
    print("logged in, landed on", page.url)

    for name, path, scroll, sel in SHOTS:
        if only and name not in only:
            continue
        page.goto(f"{WEB}{path}", wait_until="networkidle")
        page.wait_for_timeout(1800)
        if sel:
            page.wait_for_selector(sel, timeout=15000)
        if scroll:
            page.mouse.wheel(0, scroll)
            page.wait_for_timeout(1200)
        raw = OUT / f"{name}.raw.png"
        page.screenshot(path=str(raw))
        img = Image.open(raw)
        img = img.resize((1600, round(img.height * 1600 / img.width)), Image.LANCZOS)
        px = img.convert("RGB").load()
        w, hh = img.size
        bg = px[w - 5, hh - 5]
        y = hh - 1
        while y > 0 and all(
            abs(px[x, y][c] - bg[c]) <= 2 for x in range(0, w, 8) for c in range(3)
        ):
            y -= 1
        img = img.crop((0, 0, w, min(hh, y + 40)))
        img.convert("RGB").save(OUT / f"{name}.png", optimize=True)
        raw.unlink()
        print(f"  {name}.png  <- {path}")

    browser.close()
print("done")
