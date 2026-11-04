# Design Principles

## Collector configurations are monolithic

Each OpenTelemetry Collector configuration in `values.yaml` is treated as a
monolithic value. The chart renders it exactly as written, unless it contains NVCB
blueprints. If it contains NVCB blueprints, these are first expanded into native
OTel configuration. NKTP's templates do not add, remove, merge, or change anything
else inside the OTel configuration, and no value in `values.yaml` other than `blueprints`,
which supplies blueprint definitions, can change it.

This guarantee ensures that NKTP does not limit a cluster operator's ability to
configure the Collectors for their needs.

The OpenTelemetry Operator still does its normal work when it creates each
Collector. For example, when a Target Allocator is enabled, the Operator
connects the `prometheus` receiver to it, as it does in any installation.
