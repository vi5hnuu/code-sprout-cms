Anyone with an account can write for Code Sprout. Drafts go through review before they are published, and this guide explains what reviewers look for and how to use the editor's building blocks.

## What makes a great article

1. **One clear goal.** State in the first paragraph what the reader will be able to do afterwards.
2. **Intuition before code.** Explain why the approach works, then show the implementation.
3. **Runnable examples.** Readers learn faster when they can change an input and press Run.
4. **Complexity, stated plainly.** Give time and space complexity, and say what they depend on.
5. **Practice at the end.** Link the problems where the idea is used.

## Structure

- The title is the page's only top-level heading. Start your sections at `##` and use `###` for subsections. Both appear in the "On this page" outline.
- Keep paragraphs short. Prefer a list or a table when you are comparing options.
- Write a summary of at least 50 characters. Search results and link previews show it.

## Building blocks

### Code that runs

Add `runnable` after the language of a fenced code block to give it a Run button. A `title` labels it, and a `stdin` value gives it default input:

````markdown
```python runnable title="Sum of a list" stdin="1 2 3"
print(sum(map(int, input().split())))
```
````

### The same example in several languages

Wrap fenced blocks in `code-tabs`. Readers pick their language once, and the choice is remembered:

````markdown
:::code-tabs
```python runnable title="Python"
print("hello")
```
```java runnable title="Java"
public class Main { public static void main(String[] a) { System.out.println("hello"); } }
```
:::
````

### Callouts and collapsible sections

```markdown
:::callout{kind=tip}
Kinds are note, tip, info, warning and danger.
:::

:::details[Show the solution]
Hidden until the reader opens it.
:::
```

### Practice problems and videos

```markdown
::problem{slug=two-sum}
::video{youtube=dQw4w9WgXcQ}
```

Images are uploaded from the editor toolbar, and every image needs alt text describing it.

## Review

When your draft is ready, submit it for review. A reviewer may approve it or request changes, with notes. Fix what they ask for and submit again. Once approved, an editor publishes it, or schedules it for later.

:::callout{kind=note}
Raw HTML is not allowed in articles. Everything you need is available as Markdown or as one of the blocks above. This keeps every page safe, fast and readable in the Android app.
:::
