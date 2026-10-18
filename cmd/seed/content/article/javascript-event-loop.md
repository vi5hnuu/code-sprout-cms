JavaScript runs your code on a **single thread**, yet a page can fetch data, run timers and respond to clicks at the same time. The **event loop** makes this work. It decides what runs next, and understanding it explains why some callbacks run before others and why a long loop freezes the page.

## The pieces

- **Call stack**: the function currently running and its callers. JavaScript runs one stack frame at a time to completion.
- **Task queue** (macrotasks): callbacks from `setTimeout`, `setInterval`, I/O and UI events.
- **Microtask queue**: promise reactions (`.then`, `await` continuations) and `queueMicrotask`.

## The loop

1. Run the current task until the call stack is empty.
2. Run **every** microtask, including microtasks queued by microtasks, until that queue is empty.
3. Let the browser render if it is time to.
4. Take the next task from the task queue and repeat.

## Predict the output

```javascript runnable title="Ordering of tasks and microtasks"
console.log('1: script start');

setTimeout(() => console.log('5: timeout (task)'), 0);

Promise.resolve()
    .then(() => console.log('3: promise (microtask)'))
    .then(() => console.log('4: chained promise (microtask)'));

console.log('2: script end');
```

The synchronous lines run first. Then the microtask queue drains, including the chained `.then`. Only after that does the zero-delay timeout run. `setTimeout(fn, 0)` means "as a later task", not "immediately".

## async/await is promises

`await` pauses the function and schedules the rest of it as a microtask once the awaited value settles:

```javascript runnable
async function load() {
    console.log('B: before await');
    await null;
    console.log('D: after await');
}

console.log('A: start');
load();
console.log('C: end');
```

## Keeping the page responsive

The browser can only render between tasks. A 200 ms loop blocks clicks, scrolling and painting for 200 ms. Options:

- **Split the work** into chunks, yielding between them with `setTimeout` or `scheduler.yield()` where it is supported.
- **Move heavy computation to a Web Worker**, which has its own thread and event loop.
- **Avoid unbounded microtask chains**. Because the microtask queue must drain fully before rendering, a promise loop that keeps queuing itself starves the page just like a `while (true)`.

:::callout{kind=tip}
Long tasks over 50 ms show up in the Performance panel and hurt Interaction to Next Paint (INP). Chunking work keeps every task short.
:::

## Node.js

Node.js uses the same model with extra phases (timers, I/O polling, `setImmediate`). It also has `process.nextTick`, which runs even before promise microtasks. The rule of thumb still holds: synchronous code, then microtasks, then the next task.
