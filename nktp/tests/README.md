# NKTP Chart Unit Tests

Tests use [helm-unittest](https://github.com/helm-unittest/helm-unittest):

```sh
helm plugin install https://github.com/helm-unittest/helm-unittest.git --version 1.1.2 --verify=false
task test   # from the repository root; Python 3 and Go are also required
```

The default output of NKTP's own templates is always covered by a snapshot
(`default_snapshot_test.yaml`, goldens in `__snapshot__/`). Its post-renderer
validates known dependency image tags and upstream chart labels as numeric
three-part versions, then replaces only their versions with `VERSION`. Other
output is compared exactly. The integration suite separately checks that the
cleanup selector matches every Collector's actual, unnormalized chart label.
After an
intentional change to default output, regenerate and review the diff:

```sh
task test -- -u
git diff
```

Bundled charts and other configurations use targeted assertions. Add snapshots only for
output that is worth pinning in full.

The CRD suite uses `includeCrds: true` to check native CRDs and their installation flags.

`task test` also runs the cluster-free client-side apply regression tests in
`tests/integration/client_side_apply_test.go`. They render the full chart with
LGTM enabled and check Kubernetes' annotation size
limit after adding the serialized last-applied configuration. Run just this
check after building chart dependencies with `task test:client-side-apply`.

This checks standard `helm template` output. Separately rendered native CRDs
(`--include-crds`) are outside this check: the bundled upstream
`scrapeconfigs.monitoring.coreos.com` CRD also exceeds the annotation limit and
requires separate handling when installed with client-side apply.
