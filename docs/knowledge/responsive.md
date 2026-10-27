# Phones: how the dashboard and the site stay responsive

Checked at 375 px and 320 px, dark and light, on every dashboard page and every site page (2026-10-26).

## The rules that hold it together
- **Nothing is wider than the screen.** The pass criterion is `document.documentElement.scrollWidth <= viewport`. `app.css` ends with a
  phone block (`@media (max-width: 640px)`) where each rule fixes one measured cause; `html { overflow-x: clip }` is only a safety net
  (unlike `hidden` it keeps `position: sticky` working), so **switch it off when measuring**.
- **Tables are cards on phones.** Every dashboard table is `table.deploy-table`. `app.js` copies each column heading onto its cells
  (`data-label`) and marks a row `cards-tight` (up to three short values: one row), or leaves two columns, and puts `wide` on long values.
  CSS draws the first cell as the card's title, the rest as small label-over-value pairs, and hides the heading row from sight but not
  from screen readers. Nothing in a template has to change for a new table. The docs tables do the same through `site/assets/site.js`
  (a heading row that is a `<thead>` or a first row made only of `<th>`).
- **Site header**: brand + theme button on one row, the links as a swipeable row beneath (they used to disappear under 680 px). The
  tour's tabs are one swipeable row.
- **Tap targets**: `.btn` 40 px, `.btn-sm` 36 px, inputs 16 px (iOS zooms the page when a smaller field gets focus).

## Measuring it (traps I fell into)
- On a page that overflows, `window.innerWidth` **grows to the content width**, so measuring against it hides the problem. Use a constant
  (375/320) or `visualViewport.width`.
- The dashboard's content security policy blocks `eval` and refuses to be framed. To run a script on a page, inject an inline
  `<script>` element; to audit many pages, navigate to each (iframes will not work). The site has no such limits: audit it through
  375 px wide iframes.
- A preview with `python -m http.server` caches CSS; use a server that sends `Cache-Control: no-store` or a cache-busting reload.
- The dashboard's CSS variables are `--font-mono`, `--panel-2`, `--muted`, `--faint`; the site's are `--mono`, `--panel`. A wrong name
  makes the whole `font:` shorthand invalid and silently inherited.
- The screenshot tool can lag one step behind a scroll or a tab click: take a second one before concluding something did not render.

## The automated guard
`internal/httpserver/responsive_browser_test.go` (`TestDashboardFitsAPhoneAndTablesBecomeLabelledCards`) seeds the demo
fleet, starts the real server, and drives headless Chrome through a page that loads 13 dashboard pages in 375 px and
320 px frames with `overflow-x: clip` switched off. It fails with the page name, width, and the element reaching past
the edge, or with the number of table cells that have no `data-label`. Takes ~13 s. It is skipped when no Chrome is
found (`DEPLOYMATE_CHROME` points at one); CI's Ubuntu runner has Chrome, so it runs there. Add a page to the list in
the test when you add a page. First run found `/security` 904 px wide (a long unbroken path in `.sec-detail`).
Not covered: landscape, tablets, the website (its audit is still manual).
