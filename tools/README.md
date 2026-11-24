# Release documentation

Requires Go as declared in `tools/go.mod`, Helm 4, and Task. From the repository
root, preview the image inventory table and documentation updates:

```bash
task docs:release
```

Add `-write` to update the README dependency versions, image inventory, and
Operator links:

```bash
task docs:release -- -write
```

To also update chart-version references in installation and upgrade examples,
set `nktp/Chart.yaml` separately, then edit the versions below and run:

```bash
OLD_VERSION=0.2.0
NEW_VERSION=0.3.0
task docs:release -- -from "$OLD_VERSION" -to "$NEW_VERSION" -write
```

The image table is always printed. Files are only changed with `-write`.
Review the resulting diff before committing.

To also validate an existing installation, install `kubectl` and supply both
`-kubeconfig` and `-namespace`. From the repository root, edit these values to
select one kubeconfig file (using its current context) and the installed release
namespace:

```bash
KUBECONFIG=/path/to/kubeconfig
NAMESPACE=nktp
task docs:release -- -kubeconfig "$KUBECONFIG" -namespace "$NAMESPACE"
```

Validation fails if any Running Pod in the namespace declares an image absent
from the chart inventory, or if no Running Pods are found. Use a namespace
dedicated to NKTP; unrelated Pods are included. The cluster is only read.

To run directly from `tools/`, use `go run ./release-docs` with the same flags;
chart dependencies must already be built.
