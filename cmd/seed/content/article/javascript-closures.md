A **closure** is a function together with the variables it can see from where it was **defined**. Even after the outer function has returned, the inner function keeps access to those variables. Closures power callbacks, private state, partial application, React hooks and memoisation.

## Lexical scope first

JavaScript resolves a variable by looking at where the code is *written*, from the innermost scope outwards:

```javascript runnable
const greeting = 'Hello';

function greet(name) {
    return `${greeting}, ${name}!`;   // greeting comes from the outer scope
}

console.log(greet('Ada'));
```

## A function that remembers

```javascript runnable title="A counter with private state"
function makeCounter() {
    let count = 0;                  // not reachable from outside
    return {
        increment: () => ++count,
        current: () => count,
    };
}

const a = makeCounter();
const b = makeCounter();
a.increment();
a.increment();
b.increment();
console.log(a.current(), b.current());   // 2 1
```

Each call to `makeCounter` creates a fresh `count`. The returned functions close over **that** variable, so the two counters never interfere. No code outside can reach `count` except through them, which gives you real encapsulation without classes.

:::callout{kind=note}
A closure captures **variables, not values**. If the captured variable changes later, the closure sees the new value.
:::

## The classic loop bug

Before `let`, this printed `3 3 3`:

```javascript runnable
for (var i = 0; i < 3; i++) {
    setTimeout(() => console.log('var', i), 0);
}
for (let j = 0; j < 3; j++) {
    setTimeout(() => console.log('let', j), 0);
}
```

`var` is function-scoped, so all three callbacks share **one** `i`. When they run, the loop has already finished and `i` is 3. `let` is block-scoped and creates a **new binding for every iteration**, so each callback captures its own `j` and the second loop prints `0 1 2`.

## Practical uses

### Memoisation

```javascript runnable
function memoize(fn) {
    const cache = new Map();          // lives as long as the returned function
    return (n) => {
        if (!cache.has(n)) cache.set(n, fn(n));
        return cache.get(n);
    };
}

const slowSquare = (n) => { for (let i = 0; i < 1e6; i++); return n * n; };
const fastSquare = memoize(slowSquare);
console.log(fastSquare(12), fastSquare(12));   // second call is a cache hit
```

### Partial application

```javascript
const multiply = (a) => (b) => a * b;
const double = multiply(2);
double(21);   // 42
```

### Event handlers that remember their context

```javascript
function bindCounter(button) {
    let clicks = 0;
    button.addEventListener('click', () => {
        clicks += 1;
        button.textContent = `Clicked ${clicks} times`;
    });
}
```

## Memory considerations

A closure keeps everything it references alive. A long-lived listener that closes over a large array keeps that array in memory until the listener is removed. Remove listeners you no longer need, and avoid capturing big objects you only use once.

Next in this series: how those callbacks actually get scheduled, in [The JavaScript Event Loop](/web/javascript-event-loop).
