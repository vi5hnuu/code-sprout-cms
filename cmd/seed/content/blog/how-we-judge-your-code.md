When you press **Submit** on a problem, your code is compiled and run against dozens of hidden test cases, usually in a few seconds. Here is what happens in between, and why it is built the way it is.

## Isolation first

Running strangers' code is dangerous by design. A submission might loop forever, allocate all memory, fork thousands of processes or try to read files it should not. Every submission therefore runs inside its own **microVM**: a minimal virtual machine that boots in a fraction of a second. It has no network access, a read-only base image and hard limits on CPU time, memory and output size.

Containers alone share the host kernel, so a single kernel exploit could escape them. A microVM adds a hardware-virtualisation boundary for the price of a few milliseconds of boot time.

## The pipeline

1. **Accept.** The API validates the submission, applies a rate limit and enqueues a job. You get a job id immediately.
2. **Fetch the tests.** Test sets are stored as files and cached on each judge worker by checksum. A problem with thousands of cases is fetched once, not once per submission.
3. **Compile once, run many.** The code is compiled a single time. Each test case then runs as a separate process with fresh limits.
4. **Compare.** Output is compared token by token, so a trailing space or newline never fails an answer. Problems with several valid answers use a custom checker.
5. **Stream the verdict.** Results arrive over a WebSocket as each case finishes, so you see progress instead of a spinner.

## Fair limits

Every language gets the same time and memory limits, and each test case starts with a fresh process. The test data is chosen so that the intended complexity passes comfortably in every language, and a slower complexity does not.

## Scaling out

Workers pull jobs from a queue, so adding capacity during a contest is a matter of starting more workers.

:::callout{kind=info}
Running an example inside an article uses the same sandbox, with only your input and no hidden tests.
:::
