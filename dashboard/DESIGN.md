---
version: 6
name: Snapfall — paper and ink
description: >-
  The Snapfall owner dashboard. A working product first: standard navigation, tables, tabs and
  buttons on a white frame with black type and hairlines. Colour is reserved for money: blue is
  what goes back into the capital pool, amber is what the operator keeps. The one picture is the
  live 3D cascade on the overview, built from the real settled jobs, which follows the theme:
  daylight on paper, night on ink.

colors:
  light:
    page: "#f4f4f2"
    surface: "#ffffff"
    surface-2: "#f7f7f5"
    line: "#e4e4e0"
    ink: "#111214"
    ink-2: "#3a3c40"
    muted: "#63666c"
    accent: "#111214"      # primary buttons, active tab, active nav underline
    link: "#2563eb"        # links lead to evidence
    pool: "#2563eb"
    operator: "#c2650a"
    good: "#15803d"
    wait: "#a16207"
    bad: "#b91c1c"
    stage: "#dbe7f1 → #f4f4f2"   # daylight sky
  dark:
    page: "#0f1012"
    surface: "#17181b"
    line: "#26282d"
    ink: "#f2f2f0"
    muted: "#9a9da4"
    accent: "#f2f2f0"
    link: "#7aa7f7"
    pool: "#3b82f6"
    operator: "#d97706"
    stage: "#101a2b → #08090b"   # night sky

typography:
  family: Schibsted Grotesk  # one family from the 12px label to the 50px sentence
  mono: JetBrains Mono        # hashes, addresses and ids only
  hero: { fontSize: "clamp(30px, 3.4vw, 50px)", fontWeight: 800, lineHeight: 1.02, letterSpacing: -0.045em }
  page-sentence: { fontSize: "clamp(30px, 4vw, 46px)", fontWeight: 750, letterSpacing: -0.04em }
  page-name: { fontSize: 13px, fontWeight: 600, color: muted }
  figure: { fontWeight: 700, letterSpacing: -0.035em }
  body: { fontSize: 15px, lineHeight: 1.55 }

radius: { card: 10px, control: 6px, hero: 14px }

motion:
  hero-entrance: 700ms, once
  tile-entrance: 450ms, staggered 70ms, once
  hover: 150ms; linked tiles lift 2px, the primary button steps 1px and presses back
  cascade: runs only while visible; pauses off screen, in a hidden tab, on Pause, and under reduced motion
---

## Overview

Snapfall's owner dashboard shows a business whose AI agents work on capital advanced by a pool,
and whose customers' payments pay that pool back before the operator gets anything. The design
shows that order first and keeps everything else quiet.

## Colors

The frame is paper: a white top bar with a hairline, a light neutral page, white cards. Type is
near-black. Nothing is coloured for decoration.

Two colours carry meaning on every page, and only these two:

| | light (on #FFFFFF) | dark (on #17181B) |
|---|---|---|
| Back to the pool | `#2563EB` | `#3B82F6` |
| Operator keeps | `#C2650A` | `#D97706` |

Both pairs pass the dataviz validator (lightness band, chroma floor, CVD separation ΔE 31.3 and
30.2, normal-vision floor, contrast). Links are blue because they lead to evidence. State colours
(settled green, waiting amber, refused red) always come with a word.

The dark theme is the same system in ink: near-black page, dark grey cards, white type, white
primary buttons.

## Typography

Schibsted Grotesk does the whole interface. JetBrains Mono is only for machine text. Figures are
proportional: this face puts punctuation on the tabular grid, which opened every decimal point.

## Copy

One sentence per page header, no subtitle under it. Card headings are nouns ("Settlements",
"Open jobs"), with no line explaining them underneath. Empty states are a few words ("Nothing
waiting."). Kept in full: error messages, warnings before irreversible actions, and the Audit
page's "Not proven" list.

## Layout

A 1200px content column with 40px gutters (16px under 760px). Pages open with the page name
small and grey, then the sentence. The overview opens on the hero: the sentence and figures on the
left, the cascade on the right, stacked on phones.

## Signature component: the cascade

`components/Cascade.tsx` and `components/cascade/scene.ts`.

- **What it shows.** Three stone shelves step down the stage. What customers paid falls first into
  the pool's basin; only the remainder spills on to the operator's, the order
  `JobVault.acceptDelivery` pays in. Stream widths, droplet counts and basin levels come from the
  settled jobs' real amounts.
- **Day and night.** Light theme: a pale sky, limestone, normal-blended droplets. Dark theme: a
  night sky, darker stone, additive droplets. Toggling the theme re-lights the scene in place.
- **Replay.** One settlement at a time every 4.2s, starting from the newest: number, job link,
  paid, pool, operator, when. Previous, Pause/Play and Next are buttons.
- **Selecting a part.** Clicking a shelf or basin, or its legend button, highlights it. The legend
  is the keyboard path to the same thing.
- **Words are HTML** over an `aria-hidden` canvas, so they read with WebGL off.
- **Built in code only** (the img2threejs approach): displaced boxes, noisy icosahedra, lathe
  bowls, instanced droplets, a generated stone texture. No model or texture files.
- **Cost and fallbacks.** three.js loads after first paint in its own chunks. DPR capped at 1.75.
  The loop stops off screen, in a hidden tab, on Pause, or under reduced motion (one still frame).
  If WebGL fails or its context is lost, the 2D money flow takes its place. Everything is disposed
  on unmount.

## Do's and Don'ts

### Do
- Read every figure from the chain or the owner API, and say when it could not be read.
- Keep the cascade the only moving picture on a page.
- Keep blue and amber meaning the same thing everywhere.

### Don't
- Add a subtitle that restates the heading.
- Colour a surface or the shell for decoration.
- Use violet, or a third colour for pool or operator money.
