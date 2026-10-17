## Nesting

::::details[Outer]
:::callout{kind=note}
Inner callout.
:::
Still inside the outer block.
::::

:::callout{kind=info}
```text
:::
this line and the one above are code, not a fence
```
Still inside the callout.
:::

:::::callout[Level 1]
::::details[Level 2]
:::callout[Level 3]
::callout-is-not-a-leaf
:::
::::
:::::

::::::callout[1]
:::::details[2]
::::callout[3]
:::details[4 is too deep]
deep
:::
::::
:::::
::::::
