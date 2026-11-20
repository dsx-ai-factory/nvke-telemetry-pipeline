# Release documentation

Requires Go as declared in `tools/go.mod`, Helm 4, and Task. From the repository
root, refresh the README dependency versions, image inventory, and Operator links:

```bash
task docs:release
```

To also update chart-version references in installation and upgrade examples,
set `nktp/Chart.yaml` separately, then edit the versions below and run:

```bash
OLD_VERSION=0.2.0
NEW_VERSION=0.3.0
task docs:release -- -from "$OLD_VERSION" -to "$NEW_VERSION"
```

Add `-check` to report pending updates without writing, or `-print` to preview
only the image table. Review the resulting diff before committing.

To run directly from `tools/`, use `go run ./release-docs` with the same flags;
chart dependencies must already be built.
