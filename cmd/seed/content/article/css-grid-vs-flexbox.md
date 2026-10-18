Flexbox and Grid are not competitors. **Flexbox lays items out along one axis**: a row or a column, letting content decide sizes. **Grid lays items out along two axes at once**: rows and columns defined up front, with items placed into them. Most real layouts use both.

## Flexbox: one dimension, content first

Use Flexbox when items flow in a line and their own size should drive the layout: navigation bars, button groups, a row of tags, centring a single element.

```css
.toolbar {
    display: flex;
    align-items: center;      /* cross axis */
    gap: 0.5rem;
}

.toolbar .spacer {
    margin-left: auto;        /* pushes the following items to the far end */
}
```

Items share free space through `flex-grow`, `flex-shrink` and `flex-basis`. `flex: 1` means "take an equal share of what is left".

:::callout{kind=tip}
To centre anything both ways, use `display: flex; align-items: center; justify-content: center;` or, with Grid, `display: grid; place-items: center;`.
:::

## Grid: two dimensions, layout first

Use Grid when you want rows and columns to line up: page shells, card galleries, dashboards and forms with aligned labels.

```css
.page {
    display: grid;
    grid-template-columns: 16rem minmax(0, 1fr) 14rem;
    grid-template-areas: "nav main aside";
    gap: 2rem;
}

.page > nav   { grid-area: nav; }
.page > main  { grid-area: main; }
.page > aside { grid-area: aside; }
```

`minmax(0, 1fr)` matters. A plain `1fr` column refuses to shrink below its widest content, such as a long code line, which makes the page scroll horizontally.

### The responsive gallery without media queries

```css
.gallery {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(16rem, 1fr));
    gap: 1rem;
}
```

The browser fits as many columns of at least 16rem as it can and stretches them to fill the row. The gallery goes from four columns to one with no breakpoints at all.

## Choosing quickly

| Question | Flexbox | Grid |
|---|---|---|
| Items in a single row or column? | ✅ | Works too |
| Need rows **and** columns to align? | Hard | ✅ |
| Item sizes driven by their content? | ✅ | Possible with `auto` |
| Layout defined by the container? | Partly | ✅ |
| Overlapping items in the same cell? | No | ✅ |

## Combining them

A typical page uses Grid for the shell (header, sidebar, content), Grid again for a card gallery, and Flexbox inside each card to line up its icon, title and actions. Pick per container: each one answers "one axis or two?" on its own.

:::callout{kind=info}
Use `rem` for gaps and track sizes, so layouts scale with the reader's font-size setting.
:::
