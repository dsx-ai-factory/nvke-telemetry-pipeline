# Contributing to NKTP

NKTP is developed in an internal NVIDIA repository. Changes arrive as pull
requests from branches in this repository and are reviewed by the maintainers
listed in [.github/CODEOWNERS](.github/CODEOWNERS).

## Contribution scope

Open a pull request directly for:

- Bug fixes, test additions, and documentation improvements.
- Chart template and values changes that do not alter what NKTP installs by
  default.
- New local development configs under `dev/configs/`.

Open an issue first for:

- Changes to the default deployment: what NKTP installs, enables, or exposes
  out of the box.
- Adding, removing, or upgrading a bundled dependency in `nktp/Chart.yaml`.
- Changes to the release and publishing process in [RELEASING.md](RELEASING.md).
- New test suites under `tests/`.

Settling the approach in an issue costs less than reworking a finished pull
request. If you are not sure which kind of change you have, open an issue and
ask.

Problems in the bundled upstream components themselves belong upstream unless
NKTP's configuration of them is the cause.

## Reporting bugs, proposing changes, and asking questions

- Report bugs with the [bug report form](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/issues/new?template=01_bug_report.yml).
  Include the chart version, Kubernetes and Helm versions, the values overrides
  in use, and exact reproduction steps.
- Propose changes with the [feature request form](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/issues/new?template=02_feature_request.yml).
  Describe the problem before the solution.
- Ask questions with the [question form](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/issues/new?template=03_question.yml).
  Existing answers are under the
  [`question` label](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/issues?q=is%3Aissue+label%3Aquestion).
- Issues are triaged on a best-effort basis; response times vary.

## Development setup

The local loop runs NKTP in a Kind cluster under Tilt. It requires a
Docker-compatible container runtime, [Task](https://taskfile.dev/installation/),
[Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation),
[kubectl](https://kubernetes.io/docs/tasks/tools/),
[Helm](https://helm.sh/docs/intro/install/), and
[Tilt](https://docs.tilt.dev/install.html).

```bash
task dev:up       # Kind cluster + NKTP chart + Tilt dev loop
task dev:down     # remove the workloads and delete the cluster
```

See [dev/README.md](dev/README.md) for how the loop works and how to layer a
config from `dev/configs/` on top of the chart defaults. The tooling only ever
uses `dev/.kubeconfig`; it never touches other kubectl contexts.

### Tests and checks

Chart unit tests use [helm-unittest](https://github.com/helm-unittest/helm-unittest).
Install the plugin once:

```bash
helm plugin install https://github.com/helm-unittest/helm-unittest.git --version 1.1.2 --verify=false
```

Run the checks that CI runs before opening a pull request:

```bash
task test                                 # NKTP chart unit tests
helm lint nktp && helm template nktp nktp # what the lint job runs
(cd nvcb/tests && go test ./...)          # NVCB golden tests, when changing nvcb/
```

`task test` adds the upstream Helm repositories, builds the locked chart
dependencies, and runs `helm unittest`. The default output of NKTP's own
templates is pinned by a snapshot in `nktp/tests/__snapshot__/`. After an
intentional change to that output, regenerate the snapshot and review the diff
before committing it:

```bash
task test -- -u
git diff nktp/tests/__snapshot__/
```

NVCB scenarios under `nvcb/tests/pass/` pin their rendered output in
`output.yaml`. Regenerate them the same way after an intentional change:

```bash
(cd nvcb/tests && go test ./... -update)
```

Never edit a snapshot or golden file by hand, and never regenerate one to make
a failing test pass without understanding why the output changed. See
[nktp/tests/README.md](nktp/tests/README.md) for when to add a snapshot versus
targeted assertions.

The end-to-end suite installs the chart on a Kind cluster, so it needs a
container runtime and takes minutes rather than seconds. CI runs it on every
pull request; run it locally when changing what the chart installs:

```bash
task e2e:up      # create the cluster and fetch the chart dependencies
task e2e:test    # run the suite, as often as you like
task e2e:down    # delete the cluster
```

See [tests/e2e/README.md](tests/e2e/README.md) for what a run does, for
pointing the suite at a cluster you brought up yourself, and for running the
validations against a release that is already installed somewhere.

Two license checks also run on every pull request. Run them locally before
pushing:

```bash
task license:check
```

The first requires the SPDX header on Go, shell, `.tpl`, workflow and Taskfile
files. The second reads the chart dependencies in `nktp/charts/`, which the
task fetches first, and fails when a bundled chart's version changes without
the same change in [nktp/THIRD_PARTY_NOTICES.md](nktp/THIRD_PARTY_NOTICES.md).
For a chart version bump, run `task license:update-notices`, review the notices
diff and rendered chart, then run `task license:check`. Additions or removals of
bundled charts require a manual notices review.

No specialized hardware or credentials are needed for any of the above. Chart
publishing to NVCR runs only from `main` and release tags and is described in
[RELEASING.md](RELEASING.md); pull requests validate without publishing.

### Security scanning

CI scans source dependencies on every pull request and chart images when a pull
request changes chart inputs. Both scans also run on a schedule. Review the job
summary and report artifact for findings, even when the scan passes. If a
security check fails or reports a vulnerability introduced by your change,
reproduce it locally, update the affected dependency or image, then rerun the
scan and relevant tests to verify the fix. Coordinate fixes for existing
findings or bundled chart updates with a maintainer.

To scan locally, install Helm, Grype, and [yq v4](https://github.com/mikefarah/yq),
then run `task security:scan` from the repository root. The task needs network
access to fetch chart dependencies and scan images. Follow [SECURITY.md](SECURITY.md)
for potentially undisclosed vulnerabilities; do not post sensitive details in
a public issue or pull request.

### Submodule tooling

When a project submodule needs environment bootstrap or tool installation beyond
the main development workflow, define a minimal `Taskfile.yml` in that submodule
with the required automation. Include it in the [root Taskfile](Taskfile.yml)
with `dir` set to the submodule directory. The root exposes namespaced commands
while the submodule owns their implementation.

Examples:

- [NVCB](nvcb/Taskfile.yml) owns its test and snapshot update commands,
  exposed as `task nvcb:test` and `task nvcb:update`.
- [Scale tests](tests/scale/README.md) require an existing cluster selected by
  `KUBECONFIG`. Use `task scale:install` and `task scale:uninstall` to manage the
  environment. From the repository root, run a plan with
  `task scale:test -- -plan "$PWD/tests/scale/examples/telemetrygen/plan.yaml"`.

## Developer Certificate of Origin

Every commit must carry a `Signed-off-by` line. The sign-off certifies that you
wrote the change, or otherwise have the right to submit it under this project's
license. It is a statement about where the code came from, not a claim that it
is correct. There is no separate contributor license agreement to sign.

`git commit -s` adds the line for you from your configured identity. Edit the
name and email below before configuring them for this checkout:

```bash
GIT_AUTHOR_NAME="Your Name"
GIT_AUTHOR_EMAIL=you@example.com
git config --local user.name "$GIT_AUTHOR_NAME"
git config --local user.email "$GIT_AUTHOR_EMAIL"
git commit -s
```

The line it appends looks like this, and has to be a real name rather than a
pseudonym:

```text
Signed-off-by: Joe Smith <joe.smith@email.com>
```

If you forget, `git commit --amend -s` fixes the last commit and
`git rebase --signoff main` fixes an entire branch. You can also push one
signed-off remediation commit that covers the earlier ones; the failing `DCO`
check prints its exact text. Pull requests with a missing sign-off are labelled
`needs-dco`.

Signing off certifies the Developer Certificate of Origin 1.1, reproduced in
full below from [developercertificate.org](https://developercertificate.org/):

```text
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.

Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```

## Pull request process

1. Branch from `main` in this repository. The convention in use is
   `<user>/<topic>`.
2. Give the pull request a title in
   [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/)
   form: `<type>[(scope)][!]: <subject>`. Allowed types are `build`, `chore`,
   `ci`, `docs`, `feat`, `fix`, `perf`, `refactor`, `revert`, `style`, and
   `test`. Mark a
   breaking change with `!`. A workflow lints the title on every pull request,
   and editing the title re-runs it without a new commit.
3. Write the title for a reader, not for the linter. Pull requests merge by
   squash only, so the title becomes the commit subject on `main`, where it
   cannot be corrected. Branch commits are discarded at merge, so there is no
   need to tidy them.
4. Fill in both sections of the pull request template. The Testing section
   should say what you ran and what it told you.
5. Keep a pull request to one change. Dependency version bumps go in their own
   pull request with the corresponding `Chart.lock` update.
6. Include regenerated snapshots or golden files in the same pull request as
   the change that alters them, so reviewers see the rendered effect.
7. Update the documentation in the same pull request when a change alters chart
   defaults, bundled dependencies, or user-visible configuration: the affected
   component's page under `docs/components/`, and `nktp/README.md` when the
   change touches what it covers.
8. Keep `nktp/README.md` self-contained. It is packaged into the chart and read
   through `helm show readme` and the chart registry, where links to other files
   do not resolve. It must not link to other documentation, except its closing
   link to `docs/` on `main`. Put in-depth material under `docs/`.
9. Keep the pull request in draft while you are still reshaping it. Once it is
   ready for review, address feedback by pushing new commits rather than
   rewriting history, and reply on the thread with what changed and where.

## Review process

Pull requests are reviewed by the `nktp-maintainers` team through
[.github/CODEOWNERS](.github/CODEOWNERS). Merging to `main` requires one
approving review from a code owner, all review threads resolved, and
re-approval after any new commits. Re-request review once you have responded
to a round of feedback, not once per commit. Reviews happen on a best-effort
basis; if a pull request has not received a response, comment on it to request
a status update.

## Releases

Chart versioning, tagging, and publishing are maintainer actions described in
[RELEASING.md](RELEASING.md). A pull request does not need to change chart
versions.

## AI-assisted contributions

AI-assisted contributions are welcome under the same review standard as any
other change.

- **You are responsible:** Review and understand all AI-generated changes
  before submitting them. "The AI wrote it" does not explain a bug.
- **Verify correctness:** AI-generated changes may contain bugs or security
  issues. Run the applicable tests and review the diff carefully.
- **You sign off, not the tool:** Whatever produced the diff, the
  `Signed-off-by` line is your certification that you have the right to submit
  the work. An AI cannot make that certification for you.
- **No AI-generated sensitive content:** Do not use AI to generate security
  policies, legal, or license text.

See [AGENTS.md](AGENTS.md) for instructions for coding agents working in this
repository.

## Security issues

Do not report security vulnerabilities through public issues or pull requests.
Follow the process in [SECURITY.md](SECURITY.md).
