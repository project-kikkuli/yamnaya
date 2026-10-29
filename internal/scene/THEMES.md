# Adding a theme

A theme draws the board behind the table: the same groups, units, agents, pull requests and weather in its own
pictures. `L` on the board and `ym theme <name>` switch between registered themes; `?` explains the current
one from the legend the theme returns.

## Where it goes

Each theme is one package under `internal/scene/<name>/`, named by one lowercase word. The packages
`anthill`, `carrier`, `civilizations`, `forest`, `islands`, `moai`, `pyramids`, `skyline`, `stocks` and
`subway` already exist with only a `doc.go` and are imported by `internal/scene/themes/themes.go`, so building
one of them touches only files in its own directory. A theme under any other name adds its own directory and one
`_` import line to `themes.go`.

`internal/scene/mountains` is the reference implementation.

## Steps

1. Create the theme type, register it, and implement `scene.Theme` (`internal/scene/theme.go`):

   ```go
   type Forest struct{}

   func init() { scene.Register(Forest{}) }

   func (Forest) Name() string { return "forest" }
   func (Forest) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas
   func (Forest) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string
   func (Forest) Legend(s scene.Scene) scene.Legend
   ```

2. Paint. Start from `c := scene.NewCanvas(width, height, box)` and draw every cell outside `box`, where the
   table floats; cells inside `box` must stay as `NewCanvas` left them. Draw a cell with
   `c.Set(x, y, fg, bg, glyph)`. For two pixels per cell, draw `"▀"` with the upper pixel as `fg` and the lower
   as `bg`. A wide glyph is `Set` on its first cell and `c.Spill` on its second. Under
   `scene.Colors == scene.NoColor` draw shapes only, with `c.Plain(x, y, glyph)`. The same arguments must give
   the same canvas: derive randomness from `scene.SeedOf(id)` and `scene.Unit`, `Noise`, `Fbm`, `Noise2`,
   `Fbm2`, never from `math/rand` or the clock. Frames are `scene.Step` (250ms) apart; animate from `frame`.
   `scene.Frame` already passes frame 0 under reduced motion (the default) and caches a repeated frame, and
   `scene.Pace` picks the tick rate from the model, so a theme neither checks reduced motion nor chooses a pace.
   Colors: `scene.RGB`, `Hex`, `Mix`, `Scale`, `Luma`, `Contrast`, `Backing`, `Readable`, `IsRed`, `Smooth`, `Clamp`,
   `Lerp`.

3. Show every signal. `scene.Scene` holds `Ranges` (groups: a repository, or a chief whose goals and nested
   chiefs sit inside it), loose `Peaks` (units), `Selected` (a unit or group id), `Now` (local time; zero means
   dusk) and `Dark`. `Walk` visits every unit and group. A `Peak` has `Name`, `Progress` 0..1, `Weather`
   (`Clear`, `Clouds`, `Storm`, `Eruption`, `Summit`; `Range.Sky()` is a group's), `Team` (each `Climber` is
   `Climbing` = busy, `Waiting` = waiting on the owner, or `Resting`), `Camps` (pull requests: `Stage` 0..6 gates
   passed, `State` `Open`, `Green`, `Failing` or `Merged`), `NeedsYou` (blink an alert on it) and `Dying`.
   Red is reserved for waiting agents, needs-you and failing CI, and those three always show red, in a glyph or
   behind it. Red is what `scene.IsRed` says: a hue within 15 degrees of pure red, HSV saturation at least .5
   and value at least .3. Nothing else may be drawn in `scene.NearRed`, that band widened to 20 degrees because a
   16 to 19 degree orange reads as red on many terminals, and that holds for pixels as much as glyphs: the
   background of a space, both halves of a half block, a full block, and the background behind a glyph. Fire,
   lava, embers, brick, earth and rock use orange or amber, a hue of 20 degrees or more, or wash below saturation
   .5 (a pink dusk sky, a grey-brown), or darken below value .3.
   No color may mark a goal or repository. Draw the busy, waiting and needs-you glyphs on
   `scene.Backing(fg, bg)`, `bg` being what is behind the cell: it darkens the cell, or lightens it behind a
   dark glyph, until the glyph reads at `scene.MinContrast` (3:1) and leaves the glyph's color alone, so busy and
   waiting stay apart and red stays red. Put other glyphs through `f, b := scene.Readable(fg, bg)` and draw `f`
   on `b`: it keeps the hue and never lightens the glyph, darkening it on a light cell or darkening the cell
   behind a light one. The contrast check finds a signal's cells by the runes of its legend `Glyph`, so list
   every rune it is drawn with there.
   A crowded screen may merge units, as the mountains do, but must keep the selection and every alert visible.

   Labels (names, progress, pull request numbers) keep a blank cell between themselves and any other label or
   signal glyph on their row: a cell that is not a letter, digit, `%`, `#` or `…` and not an agent, pull request
   or alert glyph. Cut a name only with `scene.Short`, which keeps at least `scene.ShortMin` (8) characters and the
   name's `scene.UniquePrefix` among the names of its kind, and refuses when there is less room: fold those units
   ("5 trees") rather than cut them further. `scene.NewLabels(c)` keeps the spacing: `Reserve` the cells of
   each signal glyph, then `Place` each label at the first of its candidate spots that keeps clear, which draws it
   `Readable` on whatever is behind it.

4. Kill. `Peak.Dying` is 0 while the unit lives and rises from just above 0 to 1 over `scene.KillTime` (1.5s)
   after the owner kills it; the board then removes the unit. Play the theme's kill animation from it (the
   mountains bury the peak in an avalanche), with the unit nearly gone by 1. Hide its agents and alerts while it
   falls. The kill itself never waits for this.

5. Hit. Return the id of the unit drawn at `x, y` in frame 0, its name and agents included, else the id of the
   group there, else "". A click on the board selects that row.

6. Legend. Return one `scene.Entry` for every `scene.Signal` (`scene.Signals()` lists them): its `Glyph`, its
   `Color` as `#rrggbb` (the color it is drawn in at `s.Now` and `s.Dark`, "" for the terminal's own) and its
   `Look`, how it looks in this theme ("a red bird on the trunk"). Do not say when it shows: `Signal.When()`
   says that, the same for every theme, and help prints both. List everything drawn only to decorate in
   `Decoration`.

## Checks

The tests in `internal/scene/themes` run against every registered theme with no code from it:

- every signal is in the legend once, with a look and a valid color;
- the canvas fills the screen, leaves the table's box alone, and repeats for the same frame;
- each signal (waiting, resting, more agents, each pull request state and stage, progress, needs you, each
  weather, kill, selection) changes the picture of a lone unit;
- `Hit` finds each unit;
- at every quarter hour, in dark and light terminals, the busy, waiting and needs-you glyphs have at least
  `scene.MinContrast` (3:1) against their cell, waiting and needs-you are red, and busy and waiting agents
  differ in color;
- red is drawn only for waiting agents, needs-you and failing CI, and each of those shows red: on a board with none
  of them nothing, glyph or pixel, is red or near red at any hour, and on the busy board at 80x24, 120x40 and
  200x60 at most 0.2% of the colors outside the cells those signals change are;
- on the busy board and a board of five goals whose names share their first words, at 80x24, 120x40 and 200x60,
  no label touches another label or a signal glyph on its row, and no unit, group or agent name is cut below 8
  characters or to a prefix another name of its kind shares;
- a busy board at 200x60 (30 units, 60 agents, 40 pull requests, every weather, a kill playing) paints in at
  most 2ms of CPU per frame at the 90th percentile, and allocations per frame do not grow as frames go by.

`pending` in `internal/scene/themes/pending_test.go` holds off the checks a theme fails until it is fixed; a fixed
theme fails until its entry is removed.

Run them, and the benchmark for one theme:

    ym slot --lane yamnaya-test -- nice -n 19 go test ./internal/scene/themes
    ym slot -- nice -n 19 go test ./internal/scene/themes -run XXX -bench Theme/forest

To look at it, write previews of the busy board and `cat` them in a truecolor terminal:

    YM_THEME_PREVIEW=/tmp/preview go test ./internal/scene/themes -run Preview && cat /tmp/preview/forest-4.ans

To stay within budget, paint what holds still once and reuse it while nothing that shapes it changes, as
`still` in `internal/scene/mountains/mountains.go` does; keep per-frame buffers in a `sync.Pool`; take glyph
strings from `scene.Glyph`; and avoid `fmt` in the frame loop.
