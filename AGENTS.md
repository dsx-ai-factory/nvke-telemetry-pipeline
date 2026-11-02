# Agent Guidance for NKTP

## Project context

- Purpose and users: NKTP is a Helm chart that deploys an OpenTelemetry-based
  pipeline for collecting, processing, and exporting Kubernetes metrics, logs,
  and traces. It is how NVIDIA teams run OpenTelemetry on Kubernetes. NVCB, in
  the same repository, is a Helm library chart that transpiles
  `blueprint/<type>[/<name>]` declarations into native Collector configuration.
- Primary languages and frameworks: Helm charts and Go templates; Go for the
  NVCB golden tests and the `tests/` module; Taskfile for the command surface;
  GitHub Actions for CI.
- Main entry points: `nktp/Chart.yaml`, `nktp/values.yaml`, and
  `nktp/templates/` for the chart; `nvcb/chart/templates/_run.tpl` for NVCB;
  `Taskfile.yml` for commands; `dev/Tiltfile` for the local loop.
- Docs home: [docs/README.md](docs/README.md). Contribution rules:
  [CONTRIBUTING.md](CONTRIBUTING.md). Design principles, which every change
  must follow: [docs/design_principles.md](docs/design_principles.md).

## Repository map

| Path | Purpose | Notes |
| --- | --- | --- |
| `nktp/` | The NKTP application chart | Dependencies are pinned in `Chart.yaml` and `Chart.lock`; `charts/*.tgz` is gitignored and produced by `helm dependency build` |
| `nktp/tests/` | helm-unittest suites | `__snapshot__/` pins the full default render; other suites use targeted assertions |
| `nktp/dashboards/` | Grafana dashboards shipped with the chart | JSON, loaded by templates |
| `nvcb/chart/` | NVCB library chart | The engine lives in `templates/_run.tpl` |
| `nvcb/tests/` | Go golden tests for NVCB | `pass/<scenario>/values.yaml` renders to `output.yaml`; `test-chart/` is the fixture chart |
| `tests/` | Go suites for integration, e2e, scale, and perf | Build-tagged; integration runs locally without a Kubernetes cluster; e2e and scale have Taskfiles for cluster setup and execution; only e2e runs in CI |
| `dev/` | Local Kind and Tilt loop | Uses only `dev/.kubeconfig` and never other kubectl contexts |
| `docs/` | Getting started, architecture, per-component pages, guides, and design documents | Each component's behavior and values are documented under `docs/components/`; design proposals go under `docs/designs/` |
| `.github/` | Workflows, composite action, PR template, CODEOWNERS, issue forms | Changes here need explicit instruction |
| `RELEASING.md` | Release, tagging, and publishing process | Maintainer-owned |

## Build, test, and verify

- Required tools and versions: Helm 3 (CI pins 3.20), the helm-unittest plugin
  (CI pins 1.1.2), Task, and Go as declared in the `go.mod` files. The dev loop
  additionally needs a Docker-compatible runtime, Kind, kubectl, and Tilt.

Run these commands from the repository root.

```bash
# once
helm plugin install https://github.com/helm-unittest/helm-unittest.git --version 1.1.2

# NKTP chart: unit tests, lint, and render
task test
helm lint nktp && helm template nktp nktp

# NKTP chart: regenerate the default-output snapshot after an intentional change, then review
task test -- -u
git diff nktp/tests/__snapshot__/

# NVCB: golden tests, and regeneration after an intentional change
(cd nvcb/tests && go test ./...)
(cd nvcb/tests && go test ./... -update)

# NKTP chart: end-to-end tests on a Kind cluster
task e2e:up
task e2e:test
task e2e:down

# local cluster
task dev:up
task dev:down
```

- Expected results or limitations: `task test` needs network access to add the
  upstream Helm repositories and build dependencies, then runs in a few
  seconds. The NVCB tests download Go modules on first run.
  `task e2e:up` additionally needs a running
  container runtime, Kind, and kubectl; it and the run that follows take
  minutes, because a cluster has to come up and pull every image the chart
  installs. The cluster is the caller's to delete, with `task e2e:down`.
  Integration Collector tests require Docker and the pinned image; scale tests
  use plans and a selected cluster.

## Working rules

1. **Always respect the design principles.** Every change must follow
   [docs/design_principles.md](docs/design_principles.md). This rule comes
   before all other rules in this file. If a task cannot be done without
   breaking a principle, stop and explain why. Do not work around a principle,
   and do not add an exception to one.
2. **Read before writing.** Do not modify a template, values file, or test you
   have not read, and read the rendered output when the change affects it.
3. **Tests must pass.** Run `task test` before opening a pull request, and the
   NVCB tests when `nvcb/` changes. Fix failures; do not work around them, and
   do not treat a pre-existing failure as acceptable without saying so.
4. **Never make a test pass by weakening it.** Do not skip, delete, or loosen
   an assertion, and do not regenerate a snapshot or golden file until you can
   explain why the rendered output changed and the change is intended.
5. **Do what was asked.** Do not widen the change, add features, or refactor
   nearby code without being asked.
6. **Stop after three failed attempts** at the same fix. Reassess and explain
   what is blocking instead of trying a fourth variation.
7. **Keep documentation commands runnable as written.** Check Markdown
   examples against the code, and ensure every referenced `task` target exists
   in the relevant Taskfile. State prerequisites and the working directory.
   Show short, editable assignments such as `NAMESPACE=nktp` or
   `CA_FILE=/path/to/ca.crt` before commands that use them, and briefly tell
   readers to edit the values before running. Quote variable references in
   commands. Use this pattern instead of interactive `read -r` prompts or
   separate variable-validation lines. Keep example output and pseudocommands
   separate from runnable shell blocks.
8. **Keep READMEs focused on user needs.** Document what readers need to run,
   configure, or interpret the software. Do not update a README for every minor
   change or record every implementation decision. Include internal details,
   compatibility notes, or decision rationale only when they affect a user's
   actions or understanding; keep other details in code comments, design docs,
   or pull request descriptions.

## Secrets and credentials

Never commit a secret. Registry tokens, kubeconfigs, exporter credentials,
authentication headers, and cloud service-account files do not belong in this
repository: not in values files, not in `dev/configs/`, not in test fixtures,
not in a commit message. That includes values used only locally, such as an
NGC registry token or the kubeconfig the dev loop writes to `dev/.kubeconfig`,
which is gitignored and must stay that way.

Never paste one where it is recorded. Issues, pull requests, review comments,
and CI logs are durable. Redact before pasting; a token in a rendered manifest
or a collector log is a leaked token.

Commit the reference, not the value: an environment variable name, or a
Kubernetes Secret name and key, read at run time. Test fixtures and dev configs
use obviously fake values.

If a secret does get committed, rotate it first, then tell a maintainer, then
clean the history. Deleting the file or amending the commit is not a fix; the
value is already in every clone that fetched it.

## Pull requests

- Give the pull request a Conventional Commits title. Squash merge makes the
  title the commit subject on `main`. See
  [CONTRIBUTING.md](CONTRIBUTING.md) for the allowed types and scopes.
- Fill in the pull request template as defined in
  `.github/PULL_REQUEST_TEMPLATE.md`. The Testing section states what was
  actually run and what it showed, not "CI is green".
- Include regenerated snapshots or golden files in the same pull request as
  the change that alters them, with the diff reviewed.
- Update the documentation in the same pull request when chart defaults,
  bundled dependencies, or user-visible configuration change: the affected
  component's page under `docs/components/`, and `nktp/README.md` when the
  change touches what it covers.
- Keep the pull request in draft while still changing it. Once it is ready,
  append commits rather than rewriting history, reply on the review thread
  with what changed and where, and re-request review once per round.
- Verify what is about to be pushed: `git log --oneline origin/main..HEAD` and
  `git diff --stat origin/main...HEAD`.

## Project guardrails

Agents may, when requested:

- Edit chart templates, values, tests, dashboards, and documentation.
- Run the test and verification commands above.
- Open pull requests.

Do not change unless explicitly instructed:

- Anything under `.github/`: workflows, the composite action, CODEOWNERS, the
  pull request template, and issue forms.
- `RELEASING.md`, `SECURITY.md`, `CONTRIBUTING.md`, and this file.
- Bundled dependency versions in `nktp/Chart.yaml`, and `nktp/Chart.lock`.
- Anything under `dev/` that affects cluster access or kubeconfig handling.

Always:

- Follow the [e2e test authoring guidance](tests/e2e/README.md#adding-a-new-test)
  when adding or modifying tests under `tests/e2e/`.
- Keep `tests/e2e/README.md` focused on suite setup, execution, and shared
  infrastructure. Do not document individual tests or add test-specific run
  commands there. Put each test's purpose, prerequisites, assertions, and
  cleanup in comments beside the test code. Omit routine run commands and
  suite instructions from test comments; the README already covers them. Follow
  [Google's Go commentary guidance](https://google.github.io/styleguide/go/decisions#commentary):
  doc comments begin with the declaration's name, use complete sentences, and
  wrap for readability consistently with the file.
- Keep Collector configurations monolithic. Write each one as complete, native
  Collector YAML in `values.yaml`, and render it unchanged. Templates must not
  add, merge, or change anything inside a configuration, and no other value may
  change one. There are no exceptions. See
  [docs/design_principles.md](docs/design_principles.md).
- Keep `nktp/README.md` self-contained. It is packaged into the chart, where
  links to other files do not resolve, so it must not link to other
  documentation except its closing link to `docs/` on `main`. Put in-depth
  material under `docs/`.
- Treat bundled dependency versions as deliberate pins. Propose a bump as its
  own pull request and update `Chart.lock` there with `helm dependency update`.
- Do not add roadmap, support, security, or review-time commitments unless a
  maintainer has confirmed them.
- Keep README, CONTRIBUTING, SECURITY, and the issue forms consistent when
  changing policy or engagement routes.
- Do not disclose security vulnerabilities publicly; follow
  [SECURITY.md](SECURITY.md).

## Anti-patterns

| Anti-pattern | Correct approach |
| --- | --- |
| Edit `nktp/tests/__snapshot__/*.snap` or `nvcb/tests/pass/*/output.yaml` by hand | Regenerate with `task test -- -u` or `go test ./... -update`, review the diff, explain the output change in the PR |
| Regenerate a snapshot to silence a failure | Find out why the rendered output changed first; the snapshot exists to catch exactly that |
| Change `nktp/values.yaml` defaults to fix a local setup problem | Put the override in a `dev/configs/<name>/values.yaml` overlay and run `task dev:up -- <name>` |
| Run `helm dependency update` as part of an unrelated change | Only in a deliberate dependency-bump PR; `task test` uses `helm dependency build`, which honors `Chart.lock` |
| Use an arbitrary kubeconfig or context | Use `dev/.kubeconfig` for dev operations, or the suite-local or explicitly supplied kubeconfig for e2e and scale. Use the intended cluster's kubeconfig explicitly; leave unrelated contexts untouched |
| Hardcode an image tag or upstream chart version in a template | Versions live in `nktp/Chart.yaml` and `nktp/values.yaml`; templates reference them |
| Disable a bundled component in defaults to make something else work | Explain the interaction in an issue; default topology changes need discussion first |
| Skip, delete, or loosen a failing test | Fix the actual issue, or say plainly that it is not fixed |
| Create a new file when editing an existing one suffices | Prefer editing; new top-level files and directories need a reason |
| Guess at a missing value or intent | Ask, or state the assumption explicitly in the PR |
