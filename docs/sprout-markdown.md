# Sprout Markdown

The format of every content body (articles, blogs, problem statements, editorials). It is
**CommonMark + GitHub Flavored Markdown + a fixed set of directives**. It is not MDX, so
nothing in a body is executable.

There are three implementations, and they must agree:

| Role | Where | Library |
|---|---|---|
| Validator / analyser | `internal/markdown` (this repo) | goldmark + directive extension |
| Web renderer | `@libs/next-markdown` | remark + `remark-directive` |
| Android renderer | KMP app | JetBrains `markdown` + directive renderers |

`spec/markdown/*.md` are the conformance fixtures. Each `.json` beside a fixture is what the
analyser derives from it. Renderers use the same files: heading ids must match `toc[].id`, and
a renderer must handle every directive in the valid fixtures.

## Base syntax

CommonMark plus GFM tables, strikethrough, task lists and autolinks. Math (`$...$`, `$$...$$`)
and ```` ```mermaid ```` blocks pass through as text and code, and the renderers draw them.

**Rejected** (the API refuses to submit or publish; drafts may be saved with issues):

| Code | Rule |
|---|---|
| `html.not_allowed` | No raw HTML, block or inline. Use Markdown or a directive. |
| `heading.h1` | No `#` headings. The title is the page's only `<h1>`; sections start at `##`. |
| `code.indented` | No indented code blocks. Use fences so the language is known. |
| `image.not_asset` | Images must be uploaded: `![alt](asset:123)`. External image URLs are rejected. |
| `image.no_alt` | Every image needs alt text. |
| `link.unsafe` | Links must be `http(s):`, `mailto:`, a site path (`/dsa/x`), relative, or `#anchor`. |
| `body.too_large` | Bodies are at most 1 MiB. |

## Code blocks

````md
```python runnable title="Binary search" stdin="5\n1 2 3 4 5" highlight="2-3"
...
```
````

The first word is the language (lower-cased). The options after it:

| Option | Meaning |
|---|---|
| `runnable` | Shows a Run button. The language must be one of c, cpp, csharp, go, java, javascript, kotlin, python, rust, typescript. |
| `title="..."` | Caption, and the tab label inside `code-tabs`. |
| `stdin="..."` | Default input for a run. `\n` means a newline. |
| `highlight="1,3-5"` | Lines to emphasise. |

Code always renders as static, highlighted HTML first, so crawlers see it. Running is a client
enhancement.

## Directives

Two forms, following the generic directives proposal (as implemented by `remark-directive`):

```md
::name[label]{key=value key2="quoted value"}      leaf: one line

:::name[label]{attrs}                             container: Markdown inside
...
:::
```

- Names are lower-case `a-z0-9-`, starting with a letter. Attribute values are bare
  (no spaces) or double-quoted, with `\"`, `\\` and `\n` escapes. A bare key is a `true` flag.
- **Nesting:** a container closes on a line of at least as many colons as opened it, so an outer
  container needs a longer fence: `::::details` around `:::callout`. At most 3 levels deep.
- A colon line inside a fenced code block is code. It never closes a container.
- Text-level directives (`:name`) are not part of the format.

| Directive | Form | Attributes | Renders as |
|---|---|---|---|
| `callout` | container | `kind`: note, tip, info, warning, danger; `[label]` is an optional title | Highlighted aside |
| `details` | container | `[label]` required (the summary) | Collapsed section |
| `code-tabs` | container | holds fenced code blocks only (at least one) | One tab per block, labelled by `title` or language |
| `problem` | leaf | `slug` required | "Try it yourself" card for that practice problem |
| `quiz` | leaf | `id` required | Embedded quiz |
| `video` | leaf | exactly one of `file` (uploaded asset id) or `youtube` (11-char id); optional `title` | Player, loaded lazily |

An unknown directive, wrong form, unknown attribute or bad value is an issue (`directive.*`).
A renderer that meets a directive it does not know (an old app version, say) shows the block's
children, or nothing for a leaf. It never fails the page.

## Heading ids

The ids in the TOC come from this algorithm, and renderers must use it too:

1. lower-case the heading's text;
2. keep letters, digits, `-` and `_`; turn each space into `-`; drop everything else;
3. an empty result becomes `section`;
4. repeated ids get `-1`, `-2`, ... in document order.

The TOC lists `##` and `###` headings.

## Derived data (stored at publish in `content_render`)

- `toc`: `##`/`###` headings with ids.
- plain text: prose, headings, labels, alt text and table cells, one block per line. Code is
  excluded. Used for search and excerpts.
- `wordCount`, `readingMinutes` (200 words per minute, at least 1).
- `snippets`: runnable blocks.
- `assetIds`: images and uploaded videos. Each must exist and be READY to publish.
- `problemSlugs`: embedded problems.
