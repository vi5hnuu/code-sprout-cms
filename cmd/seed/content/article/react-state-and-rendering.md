Most React bugs come from one of three misunderstandings: when a component re-renders, what a state variable holds during a render, and how React decides whether a component is the "same" one as before. Get these right and hooks stop being surprising.

## When React re-renders

A component re-renders when:

1. its **state** changes (a `useState` setter or `useReducer` dispatch);
2. its **parent** re-renders (by default, children re-render with it);
3. a **context** it reads changes.

Re-rendering means calling your component function again and comparing the result with the previous one. React only touches the DOM where the output differs, so a re-render is usually cheap. Optimise only when profiling shows a problem.

## State is a snapshot

During one render, a state variable is a **constant**. Calling the setter does not change it. It schedules the next render with the new value.

```jsx
function Counter() {
    const [count, setCount] = useState(0);

    function addThree() {
        setCount(count + 1);   // count is 0 in this render
        setCount(count + 1);   // still 0
        setCount(count + 1);   // still 0  -> result: 1
    }

    function addThreeCorrectly() {
        setCount(c => c + 1);  // updater functions queue up
        setCount(c => c + 1);
        setCount(c => c + 1);  // result: 3
    }

    return <button onClick={addThreeCorrectly}>{count}</button>;
}
```

:::callout{kind=tip}
When the next state depends on the previous one, pass an **updater function**. It receives the latest queued value, not the snapshot from this render.
:::

## Don't copy props into state

```jsx
// ❌ Goes stale: initial value only, ignores later prop changes
function Greeting({ name }) {
    const [label, setLabel] = useState(`Hello, ${name}`);
    return <h2>{label}</h2>;
}

// ✅ Derive it during render
function Greeting({ name }) {
    const label = `Hello, ${name}`;
    return <h2>{label}</h2>;
}
```

If a value can be computed from props or other state, compute it. Use `useMemo` only when the computation is actually expensive.

## Keys and identity

React matches elements between renders by **position and type**, or by **key** in lists. A changed key means a different component, so React throws away its state and mounts a new one.

- Use stable ids as list keys, never the array index if items can be reordered or removed.
- To reset a component's state deliberately, for example a form when you switch to a different user, change its key: `<ProfileForm key={user.id} user={user} />`.

## Effects are for synchronising, not deriving

`useEffect` runs **after** render to synchronise with something outside React: a subscription, a timer, the document title, a network request.

```jsx
useEffect(() => {
    const id = setInterval(() => setSeconds(s => s + 1), 1000);
    return () => clearInterval(id);    // cleanup on unmount or before re-running
}, []);
```

If you are writing an effect that only sets state from other state, delete it and compute the value during render instead. It removes an extra render and a whole class of stale-value bugs.

This article is part of the [JavaScript Under the Hood](/series/javascript-under-the-hood) series. Start with [closures](/web/javascript-closures) to see why hooks capture the values of the render they were created in.
