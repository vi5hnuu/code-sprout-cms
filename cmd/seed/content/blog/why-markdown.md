Every article on Code Sprout is written in Markdown with a small set of extensions we call **directives**: runnable code, code tabs, callouts, collapsible sections, embedded problems and videos. We considered MDX, which lets authors use React components directly, and decided against it. Here is why.

## Safety with many authors

MDX compiles to JavaScript and runs it. That is fine for a team blog. It is a real risk for a site where thousands of contributors write content, because a malicious draft could execute code on the server that renders it. Markdown with directives is **pure data**: only components we registered can render, with only the attributes we allow.

## Cost at a million pages

Rendering Markdown is a parse and a tree transform, about a millisecond per article. MDX needs a compile and bundle step per document. When pages are rendered on demand and cached, that difference adds up quickly across a large catalogue.

## One format, every platform

The same document renders on the web and in our Android app. Each platform maps directives to its own native components. An MDX component written for React cannot render in a native app.

## Forward compatible

If a future version adds a new directive, older renderers do not break. An unknown directive degrades to its plain content. Articles written today keep working for as long as the site exists.

## What authors get

Authors get everything they need to write a great tutorial, without learning a framework:

```markdown
:::callout{kind=tip}
Use lo + (hi - lo) / 2 to avoid overflow.
:::

::problem{slug=binary-search}
```

The [writing guide](/topics/writing-guide) lists every building block.
