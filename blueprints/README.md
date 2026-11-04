# NKTP blueprints

The blueprints NKTP publishes, one per file at `<kind>/<type>.yaml`. Each file is
a Helm values file. The NKTP chart's default values include all of them.

## Change a blueprint

1. Edit its file. Keep its `template`, `version`, and `description`, and raise
   the major version for any change that may require users to change their
   configuration.
2. Run `task blueprints` from the repository root.
3. Commit the file and `nktp/values.yaml` together. `task test` fails when they
   differ.
