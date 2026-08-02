# Design

<!-- impeccable:design-schema 1 -->

The design system of the Snapfall dashboard, recorded after the build from the shipped artifact.
Every token, rule and ratio below was read out of `dashboard/` and, where it is a number, recomputed
rather than copied from the annotation beside it. Where the code and the direction contract disagree,
the code is recorded as the truth and the divergence is listed under **Open**.

A finish review was run at the end of the build. The **Open** section is that review's findings,
verbatim in substance. This document is the residue of a review, not a claim of completeness.

Sources: `dashboard/app/globals.css` (2159 lines), the direction contract emitted as a real HTML
comment at `dashboard/app/layout.tsx:30-58` (seed key `c0015262`), `PRODUCT.md`, the drawn components
in `dashboard/components/`, and the nine surfaces in `dashboard/app/`.

---

## 1. The world

A **piping and instrumentation diagram (P&ID)**. Ink on a drafting surface: hairline rules do the
structural work, vessels and valves are drawn as glyphs, figures hang off callout leaders that name
what they measure, annotation lettering is tracked small caps, all money is tabular.

It was chosen because the product genuinely is plumbing with capacity limits. `FloatPool` caps one
org's advance at 10% of TVL and pool-wide lending at 80%, and a request that crosses either reverts
with `CapExceeded` (`components/PoolVessel.tsx:41-43`). A P&ID is the drawing an engineer reads to
understand exactly that, so the vessel draws those two caps as threshold lines on the tank wall
rather than printing them as labels in a card — the reader sees headroom without doing arithmetic.

The world is borrowed whole, not quoted. Three consequences are enforced at the token layer so no
component can reintroduce them by accident (`app/globals.css:10-18`, `:129-135`):

| Rule | Enforcement in the artifact |
| --- | --- |
| No depth | `--shadow: none; --shadow-lg: none;` in both themes (`:134-135`, `:231-232`). Elevation is line weight. |
| No glow | every `*-glow` token resolves to `transparent` (`:81`, `:99-100`, `:106`). The names survive only because components still reference them. |
| No gradient | zero gradient declarations for colour; `--cta-from` and `--cta-to` are the same hex (`:104-105`, `:207-208`), so even a reintroduced one renders flat. |
| Corners | `--radius: 2px` (`:131`). Enough to stop a box reading as a rendering artefact, never enough to read as a rounded card. |

The one `repeating-linear-gradient` in the file is not decoration: it is the hatch that marks an
unknown budget (`:2041-2045`). Hatching is native P&ID vocabulary and is the absence idiom, section 5.

Colour is semantic only, in instrument grammar: **`--pos`** open/flowing/settled, **`--neg`**
alarm/refused, **`--warn`** caution/pending, **`--accent`** the live process line. Nothing is
coloured for decoration. The ground is warm graphite in dark and warm paper in light, deliberately
not blue-black and not white: blue-black plus a bright accent is the crypto-dashboard default this
world refuses, and white plus hairlines reads as a spreadsheet (`:38-40`, `:143-146`).

---

## 2. Tokens

### 2.1 Surfaces and lines

| Token | Dark | Light | Use |
| --- | --- | --- | --- |
| `--bg` | `#16171a` | `#efece4` | page ground |
| `--bg-2` | `#1a1c20` | `#f4f1ea` | recessed ground, inputs |
| `--well` / `--well-2` | `#121316` / `#0f1012` | `#e7e3d9` / `#e1dcd1` | nested canvases |
| `--panel` | `#1a1c20` | `#faf8f3` | drawn panels, `.card-shell`, `.vessel` |
| `--panel-2` | `#1e2024` | `#f4f1ea` | row surfaces (`.approval`, `.jobs-row`) |
| `--panel-3` | `#232529` | `#eeeae1` | lightest dark surface — the dark worst case for text |
| `--border` | `#2e3136` | `#cdc7b9` | the drawn line; most structure |
| `--border-2` | `#3e4249` | `#b2ab9a` | the heavier line: vessel walls, section boundaries |
| `--border-3` | `#34373c` | `#c2bbac` | intermediate rule |

Hairlines are not text and are not held to 4.5:1. Measured, `--border` on `--panel` is **1.31:1**
dark and **1.59:1** light; `--border-2` on `--panel` is **1.69:1** dark and **2.15:1** light.

### 2.2 Type ramp (colour), with measured contrast

Ratios recomputed from the shipped hex values by WCAG 2.x relative luminance. Text tokens are judged
against the **lightest surface they can land on** in each theme, which is the worst case: `--panel-3`
in dark, `--panel` in light. Additional surfaces are given where they change the verdict.

**Dark** (`:67-70`, `:76-93`):

| Token | Hex | on `--panel-3` (worst) | on `--panel` | on `--bg` | Verdict |
| --- | --- | --- | --- | --- | --- |
| `--text` | `#e9e7e2` | 12.42 | 13.81 | 14.50 | AAA |
| `--text-2` | `#bab6ae` | 7.59 | 8.44 | 8.87 | AAA |
| `--muted` | `#a09c94` | 5.61 | 6.24 | 6.55 | AA |
| `--muted-2` | `#918d85` | 4.64 | 5.16 | 5.42 | Clears AA on every dark surface. Was `#8d8981`, which claimed 4.5 and measured 4.41 on `--panel-3`; corrected during the finish review. |
| `--pos` | `#7cb389` | 6.33 | 7.04 | 7.40 | AA |
| `--neg` | `#d47c74` | 5.07 | 5.63 | 5.92 | AA |
| `--warn` | `#d1a45c` | 6.71 | 7.45 | 7.83 | AA |
| `--accent` | `#6ba3a8` | 5.43 | 6.04 | 6.34 | AA |
| `--accent-bright` | `#8bc0c4` | 7.63 | 8.48 | 8.91 | AAA |

State fills, dark: `--pos` on `--pos-bg` **6.66**, `--neg` on `--neg-bg` **5.66**, `--warn` on
`--warn-bg` **7.17**, `--accent` on `--accent-bg` **5.52**. `--on-accent` `#0e1416` on `--accent`
**6.57**.

**Light** (`:170-205`). Two tokens here were wrong in the annotations and were corrected during the
finish review; the values below are the corrected, remeasured ones.

| Token | Hex | on `--panel` (worst) | `--panel-2` | `--bg` | `--well-2` | Verdict |
| --- | --- | --- | --- | --- | --- | --- |
| `--text` | `#1c1b18` | 16.23 | 15.27 | 14.59 | 12.60 | AAA |
| `--text-2` | `#45423a` | 9.45 | 8.89 | 8.50 | 7.34 | AAA |
| `--muted` | `#6a665c` | 5.39 | 5.08 | 4.85 | 4.19 | AA except `--well-2` |
| `--muted-2` | `#6e695d` | 5.15 | 4.85 | 4.63 | **4.00** | AA except `--well-2` |
| `--pos` | `#3a7048` | 5.51 | 5.18 | 4.95 | 4.28 | AA on panels |
| `--neg` | `#a4443c` | 5.70 | 5.36 | 5.13 | 4.43 | AA on panels |
| `--warn` | `#8a6320` | 5.09 | 4.79 | 4.58 | 3.95 | AA on panels |
| `--accent` | `#35696e` | 5.82 | 5.48 | 5.24 | 4.52 | AA |
| `--accent-bright` | `#27565b` | 7.70 | 7.25 | 6.93 | 5.98 | AAA |

State fills, light: `--pos` on `--pos-bg` **4.97**, `--neg` on `--neg-bg` **4.91**, `--warn` on
`--warn-bg` **4.61**, `--accent` on `--accent-bg` **5.14**. `--on-accent` `#ffffff` on `--accent`
**6.18**; `--on-neg` `#ffffff` on `--neg` **6.05**.

**Two corrections the review made, recorded so nobody re-introduces them:**

- `--muted-2` light was `#7c776a`, whose annotation claimed 4.5 and which actually delivered 4.21 on
  `--panel`, 3.96 on `--panel-2` and 3.78 on `--bg` — it cleared AA on nothing. It is now `#6e695d`
  (`:177`).
- `--pos` light is now `#3a7048` (`:181`), measured 4.97 on `--pos-bg` and 5.51 on `--panel`.

**Rules that fall out of the measurement and must be honoured:**

- **`--muted-2` must not be used on `--well-2` in light** (4.00). `--muted` is the token there — and
  `--muted` is only 4.19 there itself, so `--well-2` is a `--text-2` surface for anything small.
- The muted ramp has about one step of headroom at 4.5:1 before `--muted` and `--muted-2` collapse
  into one grey. `--muted-2` sits at the floor and `--muted` a clear step above it. Do not add a
  third muted token; there is no room for one (`:29-32`).

### 2.3 Focus

Focus is the process line: `--sky` maps onto `--accent` in both themes (`:110`, `:211`), so focus
reads as "this is the live element". The ring is `outline: 2px solid var(--sky); outline-offset: 2px`,
used verbatim at `:1054`, `:1096`, `:1113`, `:2010`, `:2055`, `:2057`, `:2072`, `:2090`.

`--focus-wash` is **not** a shared alpha: dark uses 55% and light uses 70% of `--sky`
(`:125`, `:226`). Focus owes 3:1 against the composited field and one alpha clears it in one theme
only. If you add a wash-based focus state, keep the two figures separate.

### 2.4 Type ramp (size and treatment)

The face is the platform sans (`:241`) at 14px/1.45 body. There is no display face; see Open 8.

| Role | Declaration | Where |
| --- | --- | --- |
| Annotation lettering | `font-size: 9.5px; letter-spacing: 0.07em; text-transform: uppercase` | `.v-threshold text`, `.v-empty`, `.v-unknown text`, `.v-pipe text` (`:1928-1936`), `.wf-lab` (`:2028`) |
| Field label (`dt`) | `font-size: 10px; text-transform: uppercase; letter-spacing: 0.08em; color: var(--muted)` | `.vessel-facts dt` `:1947`, `.jobs-facts dt` `:1992`, `.approval-facts dt` `:1073`, `.settings-facts dt` `:2139` |
| Primary figure | `30px / 600 / -0.02em` tabular | `.vessel-figure` `:1942` |
| Secondary figure | `22px / 600 / -0.02em` tabular | `.approval-figure` `:1065`, `.jobs-figure` `:1985` |
| Drawn figure | `13px / 600` tabular | `.wf-fig` `:2027` |
| Value (`dd`) | `12.5px` tabular | `:1948`, `:1993`, `:1074`, `:2140` |
| Body prose | `13-13.5px / 1.55`, `--text-2` | `.approval-purpose` `:1069`, `.settings-body` `:2132` |
| Caveat / source | `11-11.5px / 1.5`, `--muted-2` | `.vessel-source` `:1944`, `.jobs-source` `:1988`, `.workforce-caveat` `:2124` |

There are exactly four steps of figure and they are chosen by rank, not by page. A number bigger than
30px is not in this system.

**Money is always tabular.** `font-variant-numeric: tabular-nums` appears on every money and hash
figure in the file. The one place it is switched **off** is deliberate and is the absence idiom
(section 5). PRODUCT principle 5: six decimals, no rounding that hides a cent.

### 2.5 Spacing and layout rhythm

Row padding `18px 20px` (`.approval` `:1049`, `.jobs-row` `:1975`, `.vessel` `:1919`). Card body
`20px`, card head `8px 20px` with a 46px floor (`:1802-1827`). List gap `12px` between drawn rows,
`0` between schedule rows — a schedule is separated by hairlines with `border-top` and a
`:first-child { border-top: none }`, never by gaps (`:2082-2083`, `:2138`, `:2145`). Fact rows are
`gap: 7-9px` over a `border-top` hairline with `padding-top: 7-8px`. App shell is a 224px sidebar
plus content (`:248`). Prose measures are capped at 56-70ch.

---

## 3. The drawn vocabulary

### 3.1 The vessel — `.v-*` (`app/globals.css:1916-1956`, `components/PoolVessel.tsx`)

| Class | Declaration | Meaning |
| --- | --- | --- |
| `.v-wall` | `fill: none; stroke: var(--text-2); stroke-width: 1.25` | the vessel body. No fill does structural work anywhere in this system. |
| `.v-fill` | `fill: var(--accent-wash); stroke: var(--accent); stroke-width: 1` | held capital at a known level |
| `.v-threshold` | dashed `6 4`, `--border-2`, label in `--muted` | a cap, drawn where it sits on the tank |
| `.v-threshold.near` | stroke + fill `--warn` | level within 10 points of the cap |
| `.v-threshold.at` | stroke + fill `--neg` | level at or over the cap |
| `.v-unknown` | dashed `3 5` hatch lines, `--muted-2` lettering | the read has not returned |
| `.v-empty` | `--muted-2` annotation text | a real, checkable zero |
| `.v-pipe` | `stroke: var(--text-2); stroke-width: 1.25` | a live line |
| `.v-pipe-dim` | `stroke: var(--border-2)`, text `--muted-2` | a subordinate line |
| `.v-callout` | `stroke: var(--border-2); stroke-width: 1` | a dimension leader |

**The threshold rule.** A cap is a threshold, not an alarm. It draws as an ordinary annotation until
the level actually approaches it (`:1924-1926`). Permanent red at 0% utilisation would make the
colour decorative, which is the one thing the semantic-colour rule forbids. `capState` is computed at
`PoolVessel.tsx:68`.

**Order is carried by weight, never by hue.** The two outlets are `.v-pipe` (pool, first) and
`.v-pipe v-pipe-dim` (operator, second) at `PoolVessel.tsx:143-154`. "First" is not an alarm, so it
does not get a colour.

### 3.2 The waterfall — `.wf-*` (`:2014-2030`, `components/SettlementWaterfall.tsx`)

The escrow drawn as a column split in proportion to where it actually goes, so a job whose advance
ate most of the escrow looks like one and needs no caption. `.wf-pool` is the system's one fill
without a stroke (`:2024`), because `.wf-split` at `--accent` 1.25 does the structural work. Figures
hang off `.wf-callout` leaders on the left, each with a `.wf-lab` naming what it measures
(`SettlementWaterfall.tsx:156-180`).

Four states, four different claims, drawn differently (`SettlementWaterfall.tsx:17-23`):
`settled` (past tense), `projection` (conditional, same drawing, different caption verb),
`terminal` (Refunded/Cancelled — **nothing is drawn**, a `.wf-terminal` sentence instead, `:58-66`),
`unavailable` (the pool read failed — `.v-unknown` hatch and "split unknown", never a zero).

It reuses `.v-pipe` / `.v-pipe-dim` / `.v-unknown` from the vessel rather than defining its own. Keep
doing that: the two drawings are the same drawing at two scales.

### 3.3 The valve — `.valve*` (`:1078-1085`, `components/ValveState.tsx`)

A gate valve on the spend line, in the state the owner's decision put it in. Four states, expressed
geometrically, not chromatically (`ValveState.tsx:9-12`):

- `pending` — shut, stem upright, handwheel unturned; downstream run drawn dead.
- `open` — gate lifted clear (stem `y2` 4.5 rather than 6.5), line runs through.
- `shut` — gate seated, downstream run drawn dead.
- `diverted` — flow leaves on a branch (`M12 12 L12 19 L19.5 19`).

`.valve-dead { stroke: var(--border-2); stroke-dasharray: 2 3 }` (`:1082`) is what makes a refusal
read as *a line that stops* rather than *a button that changed colour*.

**The colour rule for glyphs, and it is absolute: no glyph sets its own colour.** `ValveState.tsx`
sets `stroke="currentColor"` and nothing else; the row's state class paints it —
`.approval.approved .valve { color: var(--pos) }`, `.rejected` → `--neg`,
`.alternative_requested` → `--warn` (`:1083-1085`). The glyph and its lettering therefore cannot
disagree about what happened. `Badge.tsx` follows the same rule from the other side: the dot is
`background: currentcolor` and each state is one declaration (`:1853-1866`), so no colour is written
in a component.

### 3.4 The nav glyph set (`components/NavIcon.tsx`)

Each glyph is the P&ID symbol for the thing the surface shows, not a stock icon. All are 16-unit
grid, `fill="none"`, `stroke="currentColor"`, `stroke-width="1.25"` — the same weight as `.v-pipe` —
and `aria-hidden` because each sits beside its own text label.

| Route | Symbol | Reading |
| --- | --- | --- |
| `/` | circle bisected by its horizontal diameter | board-mounted instrument: an instrument read at the panel, not in the field |
| `/jobs` | ticket with two ruled lines | a work order |
| `/workforce` | three circles on a common line | agents on a common line |
| `/approvals` | gate-valve bowtie with stem and handwheel | the valve a human turns to stop flow — the one glyph the mechanism makes literally correct |
| `/float` | vessel with its level line | the tank drawn on the Overview |
| `/audit` | two interlocking links | the hash chain `AuditAnchor` commits to |
| `/settings` | hex fitting with a bore | a drawn fastener, not the gear every dashboard reaches for |

If you add a surface, add its P&ID symbol here. Do not import an icon package; this dashboard
carries no UI dependencies (`components/Card.tsx:8-10`).

---

## 4. Surface class families, and why they are separate

Each surface owns a prefix. They are separate on purpose, and one of them is separate because of a
real production defect.

| Family | Surface | Anchor |
| --- | --- | --- |
| `.approval-*` | `app/approvals/page.tsx` | `globals.css:1033-1133` |
| `.jobs-*` | `app/jobs/page.tsx` (index) | `:1960-2012` |
| `.job-*` | `app/jobs/[jobId]/page.tsx` (detail) | `:1547-1613`, `:2032-2057` |
| `.wf-*` | the waterfall, on job detail only | `:2014-2030` |
| `.portal-*` | `app/portal/[jobId]/page.tsx` | `:1002-1030`, `:2059-2072` |
| `.audit-*` | `app/audit/page.tsx` | `:2074-2116` |
| `.settings-*` | `app/settings/page.tsx` | `:2126-2159` |
| `.float-*`, `.loss-*`, `.rate-*` | `app/float/page.tsx` | `:590-967` |
| `.workforce-*`, `.manifest-*` | `app/workforce/page.tsx` | `:1135-1500` |

**`.jobs-*` was renamed out of `.job-*` after a real collision. Do not re-collide them.**
Commit `3453298` ("fix(ui): stop the Jobs index classes overriding the job detail page"): commit
`bab3248` appended `.job-row` and `.job-figure` for the new index rows, but both names were already
taken by the **detail** page — `.job-row` was its label/value row and `.job-figure` its 30px headline.
Appended rules win on source order at equal specificity, so every detail row silently gained a
border, a `--panel-2` fill and 18px padding, and the headline figure dropped from 30px/800 to
22px/600. The fix renamed the whole index block to `jobs-` rather than patching the two that collided.

This stylesheet is a single 2159-line file appended to surface by surface. Source order is the
cascade. **A new surface gets a new prefix, appended at the end, and never reuses a noun another
surface already owns.**

`.approval-*` and `.jobs-*` are deliberately the same *register* rather than a third vocabulary
(`:1966-1968`): identity left, money right, a source line under it, then a `dt`/`dd` fact table over
hairlines. Same shape, different names.

---

## 5. The absence idiom

This is the system's signature and the thing most likely to be broken by someone extending it.
It exists because PRODUCT principle 2 is "absence is information" and principle 4 is "never claim
more than happened", and because `PRODUCT.md` lists absences that must never be fabricated.

**The system distinguishes at least four different nothings, and draws each one differently.**

| Claim | Rendering | Evidence |
| --- | --- | --- |
| *A real, checkable zero.* The chain answered and the answer was none. | Drawn: no fill, plus the annotation `nothing drawn` inside the vessel. | `PoolVessel.tsx:101-110`, `.v-empty` `:1933` |
| *The read has not come back.* | Hatched: three dashed lines and `reading chain`. Never an empty box, which reads as a failed render. | `PoolVessel.tsx:111-120`, `.v-unknown` `:1923`, `:1934` |
| *Nobody asked.* The call was never issued because no org is configured. | `no organisation set`, plus a `.vessel-note` saying what to set. This is **not** "the chain could not answer". | `PoolVessel.tsx:34-38`, `:179-189` |
| *Unknowable.* The split cannot be derived because an upstream read failed. | `split unknown`, hatched — never zeroed, never shown as an operator payout of the whole amount. | `SettlementWaterfall.tsx:122-137`, `:184-186` |

**The muted floor for a figure nobody reported.** One declaration carries the whole rule, and it is
repeated verbatim per family so each surface owns it:

```css
.vessel-facts dd.is-absent { color: var(--muted-2); font-variant-numeric: normal; }   /* :1951 */
.jobs-row .is-absent { color: var(--muted-2); font-variant-numeric: normal; font-size: 11.5px; }  /* :1999 */
.portal-absent { color: var(--muted-2); font-size: 12.5px; font-variant-numeric: normal; }        /* :2069 */
```

Two things are load-bearing there. **Same size on the vessel**, so the row still aligns and the
absence is not hidden. And **tabular figures switched off** — the only place in the system where they
are — so an absence can never be mistaken for a value at a glance (`:1949-1951`, `:1997-1999`). On
the portal that distinction is the customer's money (`:2068`).

**Hatching means unknown, everywhere.** `.job-budget-bar.is-unknown` uses a 135° repeating hatch
rather than an empty bar, because an empty bar asserts nothing-was-spent (`:2040-2045`).

**Absence is set at the same weight as evidence.** The Audit page's gap list is styled identically to
its proof list: "an audit page that whispered its absences and shouted its evidence would be doing
the thing it exists to prevent" (`:2106-2111`). Follow that.

**Not-set is not an alarm.** `.settings-state` is `--muted-2` by default and only `.is-set` takes
`--pos` (`:2151-2154`). Painting nine optional overrides red would make the two that matter
invisible.

Rules for extending: a new field that can be absent must say *which* absence it is, in the muted
register, with tabular figures off, and must never fall back to `0`, `—` alone, or an empty box where
a distinction is available.

---

## 6. Two deliberate non-conformances

These are decisions, not gaps. They were taken with reasons and the reasons are recorded in the
stylesheet at the point of divergence.

**(a) The customer portal is deliberately not an instrument diagram.**
`app/portal/[jobId]/page.tsx` uses the same tokens and the same flat register as the owner surfaces
and **none** of the instrument vocabulary (`:2059-2063`). The reader is a different principal: per
`PRODUCT.md`, the external customer arrives from one tokenised magic link, sees one job, clicks
Accept, gets a receipt, and never sees the operator's internals. They arrived to accept one
deliverable and owe nobody a drafting language to do it. The portal still keeps `.portal-absent` and
tabular money, because those are truth rules, not world rules.

**(b) Audit and Settings are schedules of references, not drawings.**
`app/audit/page.tsx` (`:2074-2078`) and `app/settings/page.tsx` (`:2126-2130`) are set as records:
label left, state right, hairlines between, no gaps. Nothing on either page is flowing, so there is
no instrument to draw; inventing one would be decoration. Each page keeps exactly one coloured
element, and it is the page's point: on Audit, `.audit-ref` — somewhere independent to follow the
evidence (`:2087-2088`); on Settings, whether a value is present.

A third, smaller one is worth recording as a pattern rather than a licence: `.approval` is the only
surface the contract allows colour on, so its rules are **stricter**, not looser (`:1035-1042`).
Colour appears on exactly three things — the valve, the two decision controls, and a closed window.
Approve and refuse are identical in size, weight, padding and border and differ only in hue, because
PRODUCT principle 3 says refusing must be as easy and as legible as approving; a "primary" style on
either would break that. Request-cheaper is separated by position (`margin-left: auto`) rather than
demoted by weight, because it is a different answer, not a lesser one (`:1110-1111`).

---

## 7. Open

Findings of the finish review, unsoftened. These are divergences from the shipped direction contract
(`app/layout.tsx:30-58`) that the build carries. None of them is a design rule; nothing here should
be inherited by a new surface.

1. **The stat-tile grid is still on Overview.** `app/page.tsx:94` renders
   `<div className="grid cols-4 mt">` of four `StatCard`s directly beneath the pool vessel. The
   THESIS names "the stat-tile grid and the hero-metric template" as exactly what this world refuses.
   The stylesheet restyled the container into a gauge band — inside `.cols-4` the card loses its box
   and the grid gains a drawn frame (`globals.css:314-341`) — but restyling is not what the contract
   asked for. This is the largest open divergence, it is awaiting an owner decision, and the
   resolution is **deletion, not restyling**: three of the four tiles restate figures the vessel
   already draws.
2. **Three FIRST VIEWPORT clauses are not true on Overview today.** The contract specifies that
   figures sit on callout leaders naming their source, that the waterfall leaves the vessel in
   stages, and that pending approvals are the one place colour appears and carry the primary action.
   The vessel draws its two outlet stubs and its labels, but the drawn waterfall exists only on job
   detail (`components/SettlementWaterfall.tsx`); the StatCards' figures carry `sub` captions, not
   leaders; and there is no pending-approval block with the primary action on the first viewport.
3. **The OWN-WORLD claim of content-free recognisability is not met.** There is no drawing frame, no
   title block, no registration marks and no instrument tag bubbles anywhere in the artifact. With
   content removed, the surfaces read as hairline-ruled panels, not as a drawing.
4. **Two surfaces are converted only in part.** Float's loss-waterfall region
   (`globals.css:908-943`) and Workforce's manifest gallery (`:1135-1500`) still carry pre-redesign
   radii and pill forms — `.loss-stages li` at `12px` (`:934`), the stage numbers as `50%` circles
   (`:939`), `.workforce-active` / `.manifest-gallery` at `14px` (`:1166`), `.workforce-policy` as a
   `999px` pill with a `◇` pseudo-element (`:1155-1162`), `.float-table-wrap` at `12px` (`:856`).
5. **Craft-floor refusals still shipped.** Marketing-style eyebrows are live at six call sites:
   `.float-eyebrow` (`app/float/page.tsx:79, 199, 273, 403`, CSS `:621`) and `.workforce-eyebrow`
   (`app/workforce/page.tsx:112, 360`, CSS `:1183-1190`, set in `--accent` at `800` weight and
   `0.1em`). Typographic glyph icons are live at `app/workforce/page.tsx:102`, where
   `PermissionChip` picks `'▣'`, `'⊘'` or `'›_'` by string comparison on the label. These are
   defects, not house style. The rest of Workforce already draws its role marks as real SVG
   (`.role-mark`, `:2118-2121`), which is the pattern to extend.
6. **Roughly 27 hardcoded `border-radius` values above 2px survive** in older regions of
   `globals.css`, bypassing the `--radius` token: 4 each at 8/10/12px, 3 each at 9/13/16px, 2 each at
   7/14px, and one each at 4/6px. A further 29 declarations use `999px`, `99px` or `50%`. Some of the
   round ones are legitimate — a status dot is a dot (`:2036`), a `50%` avatar is a portrait frame —
   but the pill forms are not. Only 21 declarations in the file use `var(--radius)`.
7. **Resolved during the finish review.** `--muted-2` in dark measured 4.41:1 on `--panel-3` against an annotation claiming 4.5; it is now `#918d85` at 4.64. Light `--muted-2` and light `--pos` were wrong in the same way and were corrected at the same time. The lesson stands: these annotations were written by eye and sold as measurements
   (`globals.css:70`). It clears AA on every other dark surface (4.68 on `--panel-2`, 4.90 on
   `--panel`). Either `--panel-3` must stop carrying `--muted-2` small text, or the token needs one
   more step of lightness. This is a live AA miss, and the annotation should not be trusted until it
   is fixed.
8. **The type stack is the platform UI font** (`globals.css:241`), with a monospace stack for hashes.
   There is no display or drafting face anywhere in the build. The tracked small-caps annotation
   register carries the drawing's voice on a system sans, which works but is not a chosen face.

---

## 8. Extending this without breaking it

- Add a prefix, append at the end of `globals.css`, and never reuse a noun another surface owns.
  Source order is the cascade here; see commit `3453298`.
- Reach for `--radius`, never a number. Reach for a state token, never a new hex.
- Never write a colour in a component. Stroke `currentColor` and let the row's state class paint it.
- Order is weight, not hue. Alarm is hue. If it is not an alarm, it does not get a colour.
- A figure that is absent says *which* absence it is, at the muted floor, with tabular figures off.
- If you add a shadow, a glow or a gradient, the token layer will not stop you — the tokens resolve
  to `none`/`transparent`, so you would have to write a literal. Don't.
- If you add a nav destination, draw its P&ID symbol in `NavIcon.tsx`. No icon packages.
- Recompute any contrast ratio you state. Two annotations in this file were wrong for months; one
  still is (Open 7).
