# Blueprints in NKTP

NKTP renders every Collector configuration through NVCB.

- NKTP depends on the NVCB chart in this repository through
  `file://../nvcb/chart`, so each NKTP release carries the NVCB from its commit.
- `nktp.collectorConfig` passes NVCB each configuration together with the
  definitions in the top-level `blueprints` value. This is how NKTP builds
  NVCB's input; NVCB itself takes one configuration, as it does anywhere else.
- A definition in a configuration's own `blueprints` section takes precedence,
  so a self-contained configuration renders the same in NKTP as anywhere else.
- Templates that read a configuration, such as the cluster events RBAC, read
  the output of `nktp.collectorConfig`.
- NKTP's own blueprints live in `blueprints/`. `task blueprints` packages them
  into a marked section of `nktp/values.yaml`, so the chart renders with its
  defaults alone. `task blueprints:check` fails if that section is out of date,
  or if a blueprint file breaks the layout, field, or template function rules.
- Moving a default component into a blueprint changes the values path that
  users override for it, so it breaks those overrides.
