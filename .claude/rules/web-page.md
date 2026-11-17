---
paths:
  - "web/**/*.ts"
  - "web/styles/*.css"
  - "web/index.html"
  - "web/.dependency-cruiser.json"
  - "internal/adapters/http/page/assets.go"
---

# Page modules and stylesheets

- The page's modules live only in `web/`, `web/components/` and `web/lib/`, are named like `^[a-z][a-z0-9-]*\.ts$`, and import each other by exact relative path with the `.ts` extension. `internal/adapters/http/page/assets.go` serves only those folders and `web/styles/`, and only names like `^[a-z][a-z0-9-]*\.(js|css|svg)$`, so a module anywhere else, named otherwise or imported without its extension 404s in the browser and blanks the page while `tsc` still passes; `make web-arch` catches it.
- The stylesheets' layout is read by code, so keep it: each theme in `web/styles/themes.css` starts on a line of its own as `[data-theme="<id>"] {` (one selector, not indented) and sets `--label` and `--default`; each text size starts a line as `:root[data-scale="<percent>"]`. The engine finds themes and sizes with those patterns (`internal/adapters/http/page/assets.go`, `themeRule` and `scaleRule`) and refuses to save any other, and `web/lib/prefs.ts` matches the same selectors exactly. A theme written another way, such as in a selector list, silently drops out of the picker.
- The page meets WCAG 2.2 Level AA: every Level A and AA success criterion holds for each part of the page, in every theme and text size, such as Reflow (1.4.10) at 320 CSS pixels wide, Focus Order (2.4.3) and Status Messages (4.1.3), and a review grades the page's accessibility findings against it. Reason: the owner's decision of 2026-11-17, so the page has one stated target that a change is checked against and every reviewer grades by, instead of each choosing its own.
