---
name: homelab
description: Operator dashboard for one NixOS home server; health first, controls one step deeper.
colors:
  bg: "#f4f5f7"
  surface: "#ffffff"
  sunken: "#eef0f3"
  line: "#d9dde3"
  fg: "#161b22"
  muted: "#5d6773"
  accent: "#0f6e8c"
  accent-soft: "#dcedf2"
  core: "#4a3f7a"
  core-soft: "#e9e6f4"
  ok: "#1f7a4d"
  ok-soft: "#dff1e7"
  warn: "#8f5c06"
  warn-soft: "#f8ecd2"
  bad: "#b03a2e"
  bad-soft: "#f7e0dc"
  bg-dark: "#101317"
  surface-dark: "#171b21"
  sunken-dark: "#13171c"
  line-dark: "#2a313a"
  fg-dark: "#e3e8ee"
  muted-dark: "#8e99a6"
  accent-dark: "#5cb8d6"
  accent-soft-dark: "#15323e"
  core-dark: "#b3a6ea"
  core-soft-dark: "#2a2545"
  ok-dark: "#5fc48f"
  ok-soft-dark: "#153325"
  warn-dark: "#e0b050"
  warn-soft-dark: "#3a2e12"
  bad-dark: "#ee8577"
  bad-soft-dark: "#43201c"
typography:
  headline:
    fontFamily: "Archivo Variable, system-ui, sans-serif"
    fontSize: "1.25rem"
    fontWeight: 600
    lineHeight: "1.75rem"
  title:
    fontFamily: "Archivo Variable, system-ui, sans-serif"
    fontSize: "14px"
    fontWeight: 600
  dialog-title:
    fontFamily: "Archivo Variable, system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 600
    lineHeight: "1.5rem"
  body:
    fontFamily: "Archivo Variable, system-ui, sans-serif"
    fontSize: "14px"
    fontWeight: 400
  control:
    fontFamily: "Archivo Variable, system-ui, sans-serif"
    fontSize: "13px"
    fontWeight: 500
  small:
    fontFamily: "Archivo Variable, system-ui, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: "1rem"
  label:
    fontFamily: "Archivo Variable, system-ui, sans-serif"
    fontSize: "11px"
    fontWeight: 500
    letterSpacing: "0.05em"
  data:
    fontFamily: "Azeret Mono Variable, ui-monospace, monospace"
    fontSize: "13px"
    fontWeight: 400
  data-small:
    fontFamily: "Azeret Mono Variable, ui-monospace, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: "1.25rem"
rounded:
  sm: "4px"
  md: "6px"
  lg: "8px"
  full: "9999px"
spacing:
  "1": "4px"
  "2": "8px"
  "3": "12px"
  "4": "16px"
  "5": "20px"
  "6": "24px"
  "8": "32px"
components:
  button:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    typography: "{typography.control}"
    rounded: "{rounded.md}"
    padding: "6px 12px"
  button-hover:
    backgroundColor: "{colors.sunken}"
  button-primary:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.surface}"
    typography: "{typography.control}"
    rounded: "{rounded.md}"
    padding: "6px 12px"
  button-danger:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.bad}"
    typography: "{typography.control}"
    rounded: "{rounded.md}"
    padding: "6px 12px"
  button-danger-hover:
    backgroundColor: "{colors.bad-soft}"
  button-sm:
    typography: "{typography.small}"
    padding: "2px 8px"
  card:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.lg}"
    padding: "16px"
  input:
    backgroundColor: "{colors.sunken}"
    textColor: "{colors.fg}"
    typography: "{typography.data}"
    rounded: "{rounded.md}"
    padding: "6px 10px"
  pill-ok:
    backgroundColor: "{colors.ok-soft}"
    textColor: "{colors.ok}"
    typography: "{typography.small}"
    rounded: "{rounded.full}"
    padding: "2px 8px"
  pill-warn:
    backgroundColor: "{colors.warn-soft}"
    textColor: "{colors.warn}"
    rounded: "{rounded.full}"
    padding: "2px 8px"
  pill-bad:
    backgroundColor: "{colors.bad-soft}"
    textColor: "{colors.bad}"
    rounded: "{rounded.full}"
    padding: "2px 8px"
  pill-info:
    backgroundColor: "{colors.accent-soft}"
    textColor: "{colors.accent}"
    rounded: "{rounded.full}"
    padding: "2px 8px"
  pill-idle:
    backgroundColor: "{colors.sunken}"
    textColor: "{colors.muted}"
    rounded: "{rounded.full}"
    padding: "2px 8px"
  nav-item-active:
    backgroundColor: "{colors.accent-soft}"
    textColor: "{colors.accent}"
    typography: "{typography.control}"
    rounded: "{rounded.md}"
    padding: "4px 10px"
  alert-bad:
    backgroundColor: "{colors.bad-soft}"
    textColor: "{colors.bad}"
    rounded: "{rounded.md}"
    padding: "8px 12px"
  dialog:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.lg}"
    padding: "20px"
    width: "min(100vw - 2rem, 32rem)"
  dialog-wide:
    width: "min(100vw - 2rem, 56rem)"
  menu:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.md}"
    padding: "4px"
    width: "15rem"
  menu-item:
    textColor: "{colors.fg}"
    rounded: "{rounded.sm}"
    padding: "6px 10px"
  menu-item-hover:
    backgroundColor: "{colors.sunken}"
  menu-item-danger:
    textColor: "{colors.bad}"
    rounded: "{rounded.sm}"
    padding: "6px 10px"
  menu-item-danger-hover:
    backgroundColor: "{colors.bad-soft}"
  chip-link:
    backgroundColor: "{colors.sunken}"
    textColor: "{colors.fg}"
    typography: "{typography.small}"
    rounded: "{rounded.sm}"
    padding: "0 6px"
  chip-link-hover:
    textColor: "{colors.accent}"
  capacity-bar:
    backgroundColor: "{colors.sunken}"
    rounded: "{rounded.full}"
    height: "8px"
  run-mark:
    backgroundColor: "{colors.sunken}"
    rounded: "{rounded.sm}"
    height: "20px"
    width: "10px"
  run-mark-large:
    height: "36px"
    width: "16px"
  progress-bar:
    backgroundColor: "{colors.sunken}"
    rounded: "{rounded.full}"
    height: "6px"
---

# Design System: homelab

<!-- Recorded from the shipped code (src/app.css and the Stacks, Storage and Backups surfaces) on 2026-11-16. The Stacks surface (overview, a stack's page, add a stack), Storage (the health ledger: capacity, subvolumes, disks), Backups (the run ledger, a backup's page, the backup and destination forms) and Settings > Logs have had a design pass. System Settings (the index ledger and Updates) had its pass on 2026-11-19. Notifications has not; it inherits these tokens and components but is not evidence for any rule here. -->

## Overview

**Creative North Star: "The Quiet Control Room"**

A dense, calm operations console for one server. The first thing on any screen is state: a table of what runs, tinted pills for how it is doing, a strip or banner for what is waiting. Controls sit at the end of the row or in the page header, one step behind the state they act on. Everything is flat, bordered and evenly lit; color appears only when it carries meaning (status, the accent action, the core host).

Visible copy is terse: labels and values, with sentences kept for errors, empty states and destructive actions (see Copy). Explanations live behind a small question-mark help popover, and anything long or focused (logs, history, a grant review) opens in a large dialog over the page instead of growing the page. Light and dark are the same system: every token is redefined under `.dark`, chosen by the operator (system, light, dark) in Settings.

**Key Characteristics:**
- Compact base type (14px), a short type scale and one font family; mono only in logs and the stack editor.
- Status as one mark whose shape says the state, then one lowercase word; never a saturated fill.
- Flat surfaces separated by 1px lines; shadows only on things that float.
- Tables are the primary layout; detail columns drop out below md so a phone never scrolls sideways.
- A purple `core` hue reserved for the core host.

## Colors

A cool-gray neutral field with one teal accent, four semantic status hues each paired with a soft tint, and a reserved purple for the core host.

### Primary
- **Harbor Teal** (accent): the one action color. Primary buttons, the active nav item (on accent-soft), links, focus outlines, the "info" pill, the update-ready banner (accent-soft fill with an accent border at 40% opacity), running-step markers, the progress bar's fill and a running day in the run strip.
- **Harbor Mist** (accent-soft): the tint behind active nav, info pills and the update banner.

### Secondary
- **Core Violet** (core / core-soft): only for the core host: System Settings in the nav when active, the Updates link on the settings ledger, the Updates page heading, its Version card heading and border at 40%, core status notices. Also ANSI magenta in log lines.

### Status
- **Running Green** (ok / ok-soft): healthy, done, set, up to date.
- **Amber Hold** (warn / warn-soft): needs approval, degraded, missing required variables, restart needed, the nav's pending-updates count.
- **Brick Fault** (bad / bad-soft): failed, rolled back, error alerts, the danger button.

### Chart series
- **series-1 / series-2 / series-3** (blue, orange, green): categorical colours for the parts of a chart, used in this order and only on chart marks (the capacity bar's segments and legend swatches). They were checked apart for colour-blind readers in both themes; a fourth part folds into a muted "other". Never used for text, status or actions.

### Neutral
- **Fog Ground** (bg): page background.
- **Paper** (surface): cards, tables, dialogs, header (at 95% with backdrop blur), default buttons.
- **Well** (sunken): table header rows, expanded service rows, inputs, log panes, idle pills, button hover.
- **Rule** (line): every border and divider.
- **Ink** (fg) and **Slate** (muted): text and secondary text; muted also carries placeholders (at 70%), icons at rest, and "nothing to do" states.

### Named Rules
**The Tint Pair Rule.** Every status hue is used as strong text on its own soft tint (alerts, notices, banners). Never a saturated status fill with white text. A data mark follows the same pairing without text: a run strip's day is a strong fill standing in a column of its own soft tint.

**The Core Is Purple Rule.** The core hue appears only on core-host surfaces; a stack control is never purple, and the core host is never teal.

**The Quiet When Fine Rule.** Healthy state reads as muted text ("Up to date", "Healthy") or a green pill; amber and red appear only when something needs the operator.

## Typography

**Body Font:** Archivo Variable (with system-ui, sans-serif)
**Mono Font:** Azeret Mono Variable (with ui-monospace, monospace)

**Character:** One sturdy grotesque at a small, dense size for everything that is read or typed: words, names, paths, revisions, field contents. The mono face appears only where text is streamed or edited as code: log panes, build output and the stack editor. Numbers in tables are tabular.

### Hierarchy
- **Headline** (600, 1.25rem): the page h1, one per page, often followed by a status pill.
- **Dialog title** (600, 1rem): Modal titles only.
- **Title** (600, 14px): section h2 and card h3; hierarchy by weight, not size.
- **Body** (400, 14px): set on html/body; text, table cells, secondary lines (muted when secondary).
- **Control** (500, 13px): buttons, nav and inputs. It comes with `.btn`, `.input` and the nav; markup never asks for it.
- **Small** (400, 0.75rem, `text-xs`): pills, small buttons, timestamps, the one hint a section may have.
- **Label** (500, 11px, 0.05em tracking, uppercase, muted, `.label`): table column headers, field labels and kind tags.
- **Log** (mono, `font-mono text-xs` on a `pre`): log lines, build output, the stack editor. Nothing else.

Markup uses these names only: the body default, `text-xs`, `.label`, and `text-xl`, `text-lg` and `text-base` for the three heading roles. An arbitrary size (`text-[13px]`) fails the rules test. A row or a field shows at most two of these styles besides its pills; a name that needs weight is semibold, not another face.

### Named Rules
**The One Family Rule.** Names, paths, revisions, addresses and field contents are set in Archivo like the words around them. `font-mono` is allowed on a `pre` element and in the log viewer and the stack editor; anywhere else it fails the rules test. (Decided with the operator on 2026-11-17, after Unraid's example: the mix of two faces on every row was the main source of visual noise.)

**The Label Is A Column Rule.** The uppercase label style names a column or a field kind. It never sits above a heading as an eyebrow.

## Copy

The voice is terse and neutral. These rules apply to every page and dialog; the ones marked (test) fail `src/ui-rules.test.ts`.

- **Labels and values, not sentences.** State is a pill, a value or a short phrase ("Applies on next update"). Write a full sentence only for an error, an empty state, the reason something is blocked, or what a destructive action removes and keeps.
- **Nothing about the reader.** No "you" or "your", no reassurance, no "simply". Buttons are a verb, or a verb and a noun.
- **Say it once.** A value or a fact appears once on a screen. If the input shows the path, no line above it repeats the path.
- **Explain in the help.** What a thing is and why it matters go in the label's `Help`, which the page shows only on request. Text a stack's author wrote (a variable's description) goes there too.
- **No helper line under a row.** A section may have one hint (`text-xs text-muted`); a row in a list or table has none. A row's secondary facts are values on one muted line, separated by a middle dot.
- **Every row says its state the same way.** One status mark and one lowercase word (see Status), in the same column on every row. Value rows use five words: set, default, empty, required, host.
- **One piece of text is at most 16 words** outside help (test).
- **A page shows at most 150 words of fixed text**, and a dialog or form at most 120, help not counted (test). A page that needs more has a section that belongs in a dialog.
- **Terms** follow `CONTEXT.md`. Everyday words where they exist: "version" for a generation, "the host" for core.

## Layout

One centered column, max 72rem (`max-w-6xl`), 16px side padding, 24px top and bottom. A sticky header holds the wordmark, the nav (horizontally scrollable) and System Settings pushed right behind a divider; below 640px Settings collapses to a gear icon.

Pages stack vertically: header row (title and summary left, actions right, wrapping), then an updates strip or banner, then alerts, then the main table, then setup cards in a two-column grid from 768px. Vertical rhythm is mostly 16px between blocks (mt-4), 20px before a major section, 32px before Setup; gaps of 8px between buttons and 12px between header groups.

Tables are full-width inside a bordered, rounded, horizontally scrollable box. Cells are 12px by 8-10px; sub-rows (services under a stack) are 13px, 6px tall padding, indented 32-40px on a sunken fill. Detail columns (Version, CPU, Memory, Open) are hidden below 768px; the table's min-width (760px) applies only from md up. A secondary column that matters on a phone moves instead of dropping: below 640px it renders under the primary cell (a `sm:hidden` copy inside the cell, the column itself `max-sm:hidden`), as Storage does with a subvolume's "Used by" chips and the Backups ledger does with a backup's run strip and state. A table inside a dialog or a card has less room, so its detail columns drop below 640px instead. Breakpoints are Tailwind's: sm 640px, md 768px, lg 1024px.

## Elevation & Depth

Flat by default. Surfaces separate by tone (bg, surface, sunken) and 1px line borders. Shadows exist only on elements that float above the page: popovers, the route-links menu and dropdown menus (Tailwind shadow-lg) and dialogs (shadow-xl over a 50% black overlay). The sticky header uses 95% surface with backdrop blur, not a shadow.

### Named Rules
**The Only Floaters Cast Shadows Rule.** Cards, tables, banners and buttons have no shadow. If it isn't portaled above the page, it doesn't get one.

## Shapes

Small, consistent radii: 4px for things nested inside another box (chip links, dropdown menu items); 6px for controls, inputs, alerts, log panes, popovers and menus; 8px for cards, tables, strips, banners and dialogs; fully round for pills, the nav count badge, the help trigger, toggles and the capacity bar. Borders are 1px `line` everywhere; the one heavier stroke is a 2px amber left rule on a variable row that still needs a value.

### Named Rules
**The Shape Carries State Rule.** A mark that shows state without words says it in shape as well as hue. A day in the run strip is a column whose fill height tells how the run went (full, two thirds, a quarter, a hairline), so it still reads in greyscale and for a colour-blind reader; the tint only repeats it. Every such mark also has a spoken name.

## Components

### Buttons
Quiet, bordered, compact.
- **Shape:** 6px radius, 1px line border, 6px by 12px padding, 13px medium, icon gap 6px, no wrapping.
- **Default:** surface fill, ink text; hover to sunken. Used for nearly every action.
- **Primary:** accent fill and border, surface text; hover to 90% accent. One per area at most: "Add stack", "Update", "Update N stacks", "Review…".
- **Danger:** bad text and border on surface; hover to bad-soft. Destructive actions carry an ellipsis and confirm in a dialog ("Remove…").
- **Small:** 2px by 8px, 12px text, for row actions (Logs, Restart, Stop, Open).
- **Focus / Disabled:** 2px accent outline at 2px offset; disabled at 50% opacity with a not-allowed cursor and a title explaining why.

### Status
Unraid's orbs, in our tokens (`Pill.svelte`): a 10px mark, then one lowercase word in the text colour at body size. No background, no border. The shape says the state, so it reads without colour: a filled circle for ok, a triangle for warn, a cross for bad, a diamond for info (in progress, or filled in by the host), a ring for idle. The mark takes the status hue; the word does not. Every row of a table or list carries one, in its own column.

### Row Menu
A row keeps at most one button: the action used most (Logs for a service, Run now for a backup, the folder icon that browses a path). Everything else is in `RowMenu.svelte`, a small button with an ellipsis at the row's end that opens a dropdown; destructive items come last, after a separator, in the fault colour, and still confirm in a dialog. A row with nothing else to do has no menu.

### Value List
A card of values to fill in (a stack's variables) is built like a table. Groups are headed by a band from edge to edge of the card in the table-header style (`.label` on sunken, 1px lines above and below). A row is the name, the status mark and word, the field, then the row's button and menu: one line from 1024px, the name and state above the field on a phone. Name and value are plain body text. There is no button per row for saving: the card ends in Reset and a primary Apply, disabled until a value changes; Apply saves every changed row, and a refused row keeps its text and shows the reason under it. A stack's Mappings card has two groups, Env mappings and Path mappings, and one "Add mapping" button left of Reset. It stays in place and opens or closes a single form row above it: a type select (Env or Path), two fields and a checkbox, then Add. Add saves at once.

### Cards / Containers
- **Card:** 8px radius, 1px line, surface, 16px padding; used for Setup sections, operation progress and empty states.
- **Strip / Banner:** same radius and border at 12px by 8-10px padding. The updates strip is surface; the update-ready banner is accent-soft with a 40% accent border. The Backups "Not backed up" strip is the calm form of a gap: a surface strip that opens with a warn pill, then one sentence with its help tip, then a select for where the folders go and a default "Review…" button; the folders are listed under it in 13px muted, the stack name in mono. The page itself stays untinted, and the strip is absent when nothing is missing.
- **Quiet empty line:** where a section has nothing to show, one muted sentence in the strip's box (8px radius, 1px line, surface, 16px by 12px).
- **Alerts:** 6px radius, soft tint with strong text, 12px by 8px, role alert or status.

### Inputs / Fields
Sunken fill, 1px line, 6px radius, 6px by 10px padding, body text (16px on a touch screen, so iOS does not zoom the page), placeholder muted at 70%. Focus is the same 2px accent outline as buttons. Selects use the same class. Checkboxes and radios take the accent color. A field's label sits above it in muted body text with a 4px gap; a group of fields is a fieldset whose legend is a title (600, 14px), or muted when the group is one setting. A choice among a few kinds is a row or stack of bordered radio boxes (6px radius, 12px by 6-8px), the chosen one on accent-soft with an accent border.

### Forms
A form lives in a dialog and shows only what most people set: a backup's form is a name, what to back up, where to and when. Everything rarer sits in an "Advanced" fold (a Disclosure). A simple choice and its advanced form stay in step: the schedule radios rewrite the cron field, and a cron value the radios cannot express appears among them as "Custom (under Advanced)". A list the operator builds (folders, skip patterns, credentials) is rows of inputs with a small "Remove" at each row's end and a small "Add a…" button under the last row.

When the form cannot be saved, the primary button is disabled and the reason is written as one muted sentence at the foot of the form, above the action row; the same sentence is the button's title. A disabled button's title never shows on a phone, so the sentence is the real message. An error from the server appears in the same place as a bad alert.

### Links
Accent text, underlined on hover, with a 4px radius so the focus ring fits the word: the same 2px accent outline at 2px offset as a button. A backup's name in the ledger and the "Backups /" line above a backup's page are links.

### Navigation
Text links at 13px muted, hover to ink; the current page gets accent-soft fill, semibold accent text, 6px radius. Settings sits right of a divider and turns core purple when current. A warn-tinted count badge on Stacks shows pending updates.

### Disclosure
The one way to fold content: `Disclosure.svelte`, a native details element with a chevron that turns. The label names what is inside and stays in place open or closed, so the same control hides it again; a `summary` beside it says what the fold holds in a few words ("4 routes, 1 folder"). A part of a page that is looked at rarely is a card whose whole content is a fold (`section`), closed at first; with `remember` an opened fold stays open on the device. There is no Basic/Advanced switch. A fold's own help goes in its `help` snippet, shown at the top of the fold while the Help switch is on. A raw `details` element fails the rules test. A link or button that reveals a field and then disappears is not allowed, and a checkbox is never used to show part of a form: a checkbox sets a value that is saved. An optional field is simply visible, marked "(optional)", with its default as the placeholder.

### Help
Unraid's model. The page carries no explanation; a label that has help is a button (`Help.svelte`), and clicking it unfolds the help under it: body text, muted, behind a 2px accent rule on the left. The header has a Help switch (also F1) that unfolds every help block on the page; it is remembered on the device. The label shows a dotted underline on hover and a help cursor, nothing at rest. Help text says what the thing does in up to about 30 words. Nothing a person must know to act safely lives only in help.

### Dialog
bits-ui Dialog, centered, 8px radius, 20px padding, 16px internal gap, max 85vh with the body scrolling. 32rem by default, 56rem when wide (logs, history, review). Close is always first in a right-aligned action row; a primary or danger action follows. A locked dialog is the exception: it has no Close button, and Escape or a click outside does nothing, so it ends only through its own action. It is used once, for a restic password the server has just generated, which closes on "I have saved it"; asking to see a stored password opens the same dialog unlocked. A form in a dialog gets an id and its submit button sits in that action row with `form="<id>"`, so Enter and the button do the same thing.

### Dropdown Menu
bits-ui DropdownMenu for a surface's rarer controls ("Manage pool"), opened from a default button with a 14px chevron-down, so the page header shows state, not a row of switches. Portaled, end-aligned, 6px offset; 6px radius, 1px line, surface, 4px padding, 13px, min 15rem, shadow-lg. Items are 10px by 6px at 4px radius and highlight to sunken. A setting is a checkbox item with a drawn toggle on the right (28 by 16px, fully round, accent when on, line when off, a 12px surface knob). The same toggle stands alone as a switch at the end of a ledger row (a backup turned on or off), with a spoken name and the 2px accent focus outline. A separator (1px line, 4px margin) sets the destructive item apart at the end: bad text, highlighting to bad-soft, an ellipsis, and a confirm dialog.

### Capacity Bar
A space figure, then the bar, then its legend, inside a card. The figure is mono at 1.125rem ("2.1 TB") with "used of 8 TB" muted beside it; free space sits right and stays muted until 75% full (warn above, bad above 90%). The bar is 8px, fully round, sunken track. Split by what fills it, the biggest three parts take the chart series colours `series-1` to `series-3` (blue, orange, green, checked apart for colour-blind readers in light and dark), the remainder is muted, and segments are divided by 1px surface lines; unsplit, it is one accent fill. The legend is 12px muted: an 8px swatch at 2px radius, the name in mono, the size. Sizes count usable space (raw divided by copies kept), here and on the Overview card.

### Chip Link
A stack name that links to that stack, used where a row lists who uses it: 12px, 4px radius, 1px line, sunken, 6px side padding; hover turns border and text accent. Several wrap with a 4px gap.

### Health Table (signature)
The Stacks overview: one row per stack with a chevron toggle, a state pill with active/total count, version (mono), update cell (pill plus a small action), CPU and memory (right-aligned, tabular), links, and Open. Expanding reveals service sub-rows on sunken with their own pills and row actions. A stack with a failed service is forced open and its toggle disabled, so failure is visible without a click.

### Run Strip (signature)
A backup's last 14 days as a row of marks, one per calendar day, oldest first; when a day had several runs the worst result sets the mark. Each mark is a small column (20 by 10px in a ledger row, 36 by 16px on the backup's page, a step narrower below 640px; 4px radius, 3-4px apart) in the soft tint of its state, with a strong fill rising from the bottom. The fill height is the state:
- **OK:** full height, ok on ok-soft.
- **Partly failed:** two thirds, warn on warn-soft.
- **Failed:** a quarter, bad on bad-soft.
- **Running:** full height, accent on accent-soft, pulsing slowly. The pulse is switched off under reduced motion.
- **Did not run:** a sunken column with a 1px line at its foot and an inset 1px line outline, because sunken on surface is too faint to see in the dark theme.

A day with a run is a link to that run; a day without one is not focusable. Each mark's title and spoken name are the same sentence ("12 Nov: OK, 4 min 12 s, 1.2 GB", "9 Nov: did not run"), and the strip is a list named "<backup>: the last 14 days". Hover outlines a mark in line, keyboard focus in accent, and the day whose run is on show keeps a 2px accent outline at 2px offset. A help tip beside the "Last 14 days" heading explains the four shapes in words.

### Run Ledger (signature)
The Backups overview: one row per backup. The name is a link to the backup's page, with its destinations under it in 13px muted and any setup problem as a small warn line. Then the run strip, then "Last run" (the state pill over one small muted line: how long ago, how long it took, the size sent; the exact time is in the title; a failed run's error under it in small bad text), then "Next run" (relative time over the schedule in words), and at the row's end the on/off switch and a small "Run now". Durations are written "42 s", "4 min 12 s" and "under 1 s". Below 640px the strip, the pill and the last-run line move under the name; "Next run" drops below 768px. Under the ledger, 32px down, a Destinations table of the same build: name and kind, where (mono) with what it keeps, who uses it, and small row actions ending in a danger "Remove…".

### Progress
A running backup shows its progress where the state already is. In a ledger row it takes the place of the last-run line under the pill: a 6px, fully round bar (sunken track, accent fill that grows with the share copied) over one small muted tabular line ("media · 1.2 GB of 4.0 GB · 12 MB/s · 31%"; the folder name drops below 768px). On the backup's page the same bar sits in a bordered 6px box under the run's summary, led by a sentence naming the folder and the destination. Before the first folder starts there is no bar, only "Getting ready…". When the total is not known the fill is a quarter-width block that pulses. The block is a status region and the bar carries its value for a screen reader.

### Backup Page
One backup, composed as a ledger row unfolded. A muted "Backups /" link line, then the name as h1 with the state pill, "Edit" and a primary "Run now" on the right. Then a card titled "Last 14 days" with the large run strip, the next run in 13px muted on the right, and a small "All runs" button that opens the full history in a wide dialog. Then the run on show, 20px down, headed "Latest run", "Run on 12 Nov" or "An earlier run": its pill and one muted summary line, an error alert when it failed, then a section for each destination with a table of folders (folder with its path in small mono, result pill, files sent, size sent, time taken; files and time drop below 640px), and a small "Show log lines" button that unfolds a sunken mono pane. Last, 32px down, "What this backup does" as two cards: what it copies, and where and when.

Selecting a mark changes the run shown in place: the run is in the address (`?run=`), the page does not scroll or add a history step, and a link from a notification opens on the run that failed. "Run now" switches to the new run and shows it live.

### Ask
`AskButton.svelte`: a small default button, "Ask", on a thing the assistant can explain — the log filter row, a failed backup run, stack update or core deploy, a notification that opened a problem; "Ask…" in a failed service's Row Menu. It shows only when an assistant is set up, and only where something went wrong (the log row excepted). It opens `AskDialog.svelte`, wide: while the model works its reasoning, then its raw answer, stream in a muted pre; the finished answer is shown in its parts (`AnswerParts.svelte`: a summary in medium weight, then a labelled cause and numbered steps), the model and usage on one muted line, and a field for another question. The stack editor's assistant uses the same parts (summary, Changes, To do). The model is asked for the parts in tags; a model that ignores them shows as plain text. The daemon reads the material itself (`POST /ask`); the page sends only what the thing is.

### Settings Ledger
The System Settings index: a list in three bands (This host, Assistant, This browser), each band headed in the table-header style. A row is the page's name as a link (semibold, 9rem wide), the status mark and word with one muted value beside it (dropped below 640px), and a chevron. A row that is a setting, not a page (Sign-in, Theme), ends in its small buttons instead. The index answers "is anything waiting?" without opening a page.

### Update Card
The Updates page (the core host) opens with one card, "Version". Its first line is the state: one mark and word (up to date, update ready, reboot to finish) and the version with what taking it means ("0.2.2 · needs a reboot"). The line ends in "Check now" and the one primary action for that state: "Update to X…" or "Reboot…". Under it, three values: Running, Newest release, Checked. A version that did not start is a warn line in the card, never folded. The card ends in "Update automatically": a time and a switch. One verb, update, covers the image and the core; the page shows one version, the newer of the two. Installed versions sit under it in a closed fold, "Versions on this host", with the words running, fallback, next boot and failed; "Development" (the build from main) is a second fold, shown only on a host with a source. "Reboot…" stays in the page header.

### Workbench (stack editor)
The one full-width page (`Shell` `wide`): a light VS Code layout filling the viewport from 1280px. A sunken title bar holds the path and the toolbar (Save, Check, Build, Install, and a Row Menu with Delete); the file list (mono, folders first, a dot on a changed file, New file in its header, Rename and Delete in each file's Row Menu) sits left of a borderless editor on the page surface, under one tab naming the open file; one Output panel under it shows what Check and Build said; the assistant is a side bar opened from an activity bar on the right edge (remembered per browser); a sunken status bar ends it. Below 1280px the parts stack in that order, the activity bar first. The editors help as you type: the manifest tab completes fields and values and explains them on hover from the manifest's schema, and marks Check's problems on their field with a wavy underline in the fault colour; a Nix file does the same from nixd (NixOS options and packages), whose state the status bar names ("Nix: nixd", "starting…", "off" with the reason on hover).

### Log Pane
Filter row (service select, search, level select, Follow checkbox, Refresh), then a sunken, bordered, 6px-radius pane in mono 12px with 20px line height: muted timestamp, a fixed-width level word in its status color, the message with ANSI colors mapped onto the token hues. 60vh tall inside a dialog. Older lines load on demand.

## Do's and Don'ts

### Do:
- **Do** put state before controls: the table or pill first, the action at the row's end or in the page header.
- **Do** show status as the mark and word, picking the tone by meaning (ok, warn, bad, info, idle); alerts keep the soft tint.
- **Do** set names, paths and revisions in the body font and give numeric columns tabular figures.
- **Do** open long or focused content (logs, history, review) in a wide Modal rather than lengthening the page.
- **Do** put the explanation in the label's Help.
- **Do** hide detail columns below md (`max-md:hidden`) and keep the table's min-width to md and up; move a column a phone still needs under the primary cell instead of hiding it.
- **Do** put a surface's rare or destructive controls in a header dropdown menu, the destructive item last behind a separator.
- **Do** keep one primary button per area and one button per row; a row's other actions go in its menu.
- **Do** end a card of values with one Apply and Reset.
- **Do** give a mark that shows state a shape for each state as well as a tint, and a spoken name that says the state in words.
- **Do** show live progress where the state already sits (the row's last-run cell, the run on show), not in a separate panel.
- **Do** keep a form to what most people set, with the rest in an Advanced fold, and fold a rarely read section of a page with a summary beside its label.
- **Do** write the reason a form cannot be saved as a sentence under the form; the disabled button's title is only a second copy.

### Don't:
- **Don't** write a sentence where a label and a value would do, or repeat a fact the screen already shows.
- **Don't** put a helper line under a row, or a second hint in a section.
- **Don't** use mono outside logs and the stack editor, or ask for a font size outside the scale.
- **Don't** reveal content with a control that then disappears, or with a checkbox.
- **Don't** use the core purple on anything that is not the core host, or teal on the core host.
- **Don't** add shadows to cards, tables or banners; only portaled popovers and dialogs float.
- **Don't** use the uppercase label style as an eyebrow or kicker above a heading.
- **Don't** fill a surface with a saturated status color; status is strong text on its soft tint. (A run strip's marks are data marks, not surfaces.)
- **Don't** tell two states of a mark apart by colour alone.
- **Don't** lock a dialog unless it has to end through its own action; every other dialog keeps Close first, Escape and the click outside.
- **Don't** let a page scroll sideways on a phone; the scroll belongs inside the table box.
