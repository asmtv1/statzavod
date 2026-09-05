# UI baseline — StatZavod

**Status:** approved reference for the roles / permissions implementation. This
document records the existing product UI; it is not a redesign brief. All new
role, company, context, archive, and permission surfaces must extend this
system without changing the current screens.

## Capture set

The captures use a disposable local development database with one company
(`Northwind Studio`) and one creator (`Анна Петрова`) so that populated table,
detail, and selection states are visible. They are layout references only; no
seed data belongs to the product.

| Surface | Desktop 1440×900 | Tablet 1024×768 | Mobile 390×844 |
| --- | --- | --- | --- |
| App shell / dashboard | `screenshots/app-shell-desktop-1440x900.jpg` | `screenshots/app-shell-tablet-1024x768.jpg` | `screenshots/app-shell-mobile-390x844.jpg` |
| Companies | `screenshots/companies-desktop-1440x900.jpg` | `screenshots/companies-tablet-1024x768.jpg` | `screenshots/companies-mobile-390x844.jpg` |
| Creators | `screenshots/creators-desktop-1440x900.jpg` | `screenshots/creators-tablet-1024x768.jpg` | `screenshots/creators-mobile-390x844.jpg` |
| Creator detail | `screenshots/creator-detail-desktop-1440x900.jpg` | `screenshots/creator-detail-tablet-1024x768.jpg` | `screenshots/creator-detail-mobile-390x844.jpg` |
| Analytics | `screenshots/analytics-desktop-1440x900.jpg` | `screenshots/analytics-tablet-1024x768.jpg` | `screenshots/analytics-mobile-390x844.jpg` |

Supplementary state references:

- `screenshots/companies-archive-modal-desktop-1440x900.jpg`
- `screenshots/creators-create-modal-desktop-1440x900.jpg`
- `screenshots/creators-create-modal-mobile-390x844.jpg`
- `screenshots/creator-detail-edit-desktop-1440x900.jpg`

## Non-negotiable system rules

- Use CSS Modules and existing SCSS tokens only. Do not introduce a UI library
  or a second icon system.
- All new visible strings require RU and EN entries through the existing i18n
  layer. Do not hard-code one-locale UI copy.
- The page is graphite, not black or warm off-white: `--page: #111213`.
  Existing radial ambience may remain subtle, but must not obscure content.
- Gold is reserved for primary actions, selected navigation/tabs, and the
  small uppercase eyebrow. Mint is success/healthy/read-only-positive; the
  pink-red scale is destructive/error only.
- Preserve the current compact data-product density. Tables stay tables at
  desktop and use their existing responsive-card collapse; do not replace
  them with a generic card grid.

## Tokens and visual language

| Token / role | Current value / treatment |
| --- | --- |
| Page / surfaces | `#111213`; `#222526` and `#2b2e2e`, normally with a graphite `linear-gradient(145deg, …, #202222)` |
| Text / muted | `#f3eee5` / `#a49f96` |
| Primary gold | `#e5b86a`; primary fill `linear-gradient(135deg, #e4b667, #c98454)` with dark `#1d1c19` text |
| Success / positive | `#9ce5bd` / `#bcefd0` on a 12% mint surface |
| Danger / error | `#ff9b8f` or `#ffb4a9`, including soft red borders/backgrounds |
| Borders | `rgb(244 235 218 / 15%)`; compact inner rows usually 8–12% |
| Focus | `2px solid rgb(156 229 189 / 52%)`, `3px` offset (primary button: 3px / 2px) |
| Radius | controls `.45–.6rem`; chips `.35–.4rem`; cards/dialogs `1rem`; summary cells `.75rem` |
| Shadow | `0 24px 48px rgb(0 0 0 / 32%), 0 3px 8px rgb(0 0 0 / 20%)` plus subtle inset highlights |
| Motion | short `.16–.18s ease` transition for hover/focus feedback; no decorative motion required |

## Typography and spacing

- Typeface: `Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont,
  "Segoe UI", sans-serif`; normal UI text inherits the 16px browser root.
- Main headings: `clamp(2rem, 4vw, 3rem)`, weight around 650, tracking
  `-.045em` to `-.055em`. Dashboard uses `clamp(1.8rem, 3vw, 2.7rem)`.
- Eyebrows are gold, uppercase content, `.65–.66rem`, weight 750, tracking
  `.15–.17em`. Section headings are generally `.95–1.05rem`.
- Labels use `.75–.84rem`, weight 650; table cells use `.72–.82rem`; dense
  table headers use `.61–.72rem`, uppercase/letter-spaced where already used.
- Core spacing step is `.35–.85rem`; panels are `1–1.25rem` padded; page
  sections use `1–1.5rem` gaps. Keep page content at `max-width: 74–76rem`.

## Geometry and density contract

- Desktop shell: two columns, `15.75rem` sidebar + flexible main, `1.1rem`
  outer gap/padding. Sidebar is a rounded `1.15rem` graphite panel; its
  selected link uses a 3px gold left rail and low-opacity gold gradient.
- Main content keeps a light radial halo inside a `1.5rem` frame. At `≤760px`
  the sidebar disappears, the shell becomes one column, and main padding is
  `1.4rem .5rem`.
- Companies: creation form is a two-column input/action row at `>900px`, with
  `1.25rem` padding; cards use 3rem marks, `1.15rem` padding, and open action
  links. At `≤900px` the create action becomes full width; at `≤620px` card
  actions stack.
- Creators: desktop grid is `minmax(15rem,1fr) 9rem 8rem 7rem 7rem
  minmax(11rem,15rem)`, with `1rem` column gaps and `.95rem 1.15rem` rows.
  At `≤620px`, table headings disappear and each creator row becomes one
  vertical card-like row; this is the approved mobile exception.
- Creator detail: summary has `1.1fr .8fr 1.5fr`; editable profile form has
  two equal columns, `.85rem` gaps, then a full-width note/actions row. At
  mobile, all multi-column layout collapses to one column.
- Analytics: filter builder/card panels are `1rem` padded. Secondary desktop
  panels use `1.65fr/.85fr`; analytics ranking and publication tables retain
  horizontal scroll at `≤900px` rather than losing columns.

## Forms, dialogs, and states

- Text inputs/selects are near-black (`#181a1a`/`#191b1b`), use the shared
  translucent border, `.45–.55rem` radius, and `.62–.72rem` padding. Their
  inset shadow and mint focus ring are required.
- Creator-create dialog: `min(100%, 42rem)`, `1.35rem` padding, two-column
  field grid at desktop and one column at `≤620px`; see the mobile capture.
- Company confirmation dialog: `min(100%, 30rem)`; VK dialog: 36rem. Dialogs
  appear over `rgb(6 7 7 / .7)` plus `blur(8px)`, use a 1rem radius, and
  `0 25px 60px rgb(0 0 0 / .5)` shadow.
- Primary save/create action is the gold gradient. Cancel/secondary actions
  stay transparent or gold-outline. Destructive confirmation is text-red,
  never a gold primary fill.
- Disabled controls have `opacity: .55` and `not-allowed` cursor. Hover
  preserves the component family: gold row tint / slight lift for a primary
  button; do not turn danger actions mint.
- `success` uses mint text and a mint 12% surface; `error` uses `#ff9b8f`
  (analytics error adds a soft red border/background). Loading/empty states
  retain the same framed surface and muted text.

## Comparison ledger for subsequent UI work

Use all points below during visual QA at 1440×900, 1024×768, and 390×844.
Differences require an explicit documented reason.

| Point | Baseline evidence | Acceptance check |
| --- | --- | --- |
| Palette | token table; every captured page | Page is `#111213`; only primary actions/selected items are gold; success and destructive semantics remain mint/red |
| App shell | `app-shell-*`, `companies-*` | 15.75rem sidebar at desktop/tablet; no sidebar at mobile; selected nav retains gold rail/gradient |
| Typography | page captures and token rules | Inter, heading scale/tracking, uppercase eyebrow, and dense label/table text remain within the recorded scale |
| Spacing / panels | Companies and detail captures | 1rem card radii, 1–1.25rem panel padding, translucent borders, and layered shadows remain consistent |
| Tables / responsive density | `creators-*`, `analytics-*` | Creator grid columns and padded row density persist on desktop; intended mobile stack/analytics overflow is preserved |
| Forms / modals | supplementary modal/edit captures | Input colors, focus ring, two-column desktop/one-column mobile form, 42rem create dialog and blurred overlay remain unchanged |
| Status / action semantics | creator detail and companies captures | Gold primary, mint success, red danger; disabled opacity and error/success treatment follow the existing rules |

## QA record

- Browser-rendered captures were taken at all 15 required surface/viewport
  combinations, then visually inspected for Companies (desktop), Creators
  (mobile), Creator Detail (tablet), Analytics (desktop), and the modal/form
  states above.
- The baseline flow was: login → app shell → Companies → create local sample
  company → Creators → create local sample creator → Creator Detail →
  Analytics. Browser console had no relevant warnings or errors.
- This baseline adds documentation and JPEG artifacts only. It intentionally
  does not change `web/src`, current routes, styles, icons, or copy.
