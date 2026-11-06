# NVCB design principles

These principles apply to every implementation of NVCB, including the Helm
library chart in `chart/`.

## NVCB is an interface

NVCB is a configuration format and the way it expands into native Collector
configuration. Helm is one way to run it.

**Why:** blueprints should not depend on how a Collector is deployed. Keeping the
interface separate from Helm lets the same blueprints work for any Collector,
in Kubernetes or not, through other implementations.

## Expand before the Collector starts

An implementation turns a configuration that uses blueprints into native
Collector configuration before any Collector reads it.

**Why:** the result works with any Collector distribution. In Kubernetes, the
OpenTelemetry Operator sees native configuration, so receiver ports and the
Target Allocator work as usual.

## One configuration in, native configuration out

The input is one Collector configuration, including its blueprint definitions.
The output replaces each blueprint referenced in a pipeline with native
components, and removes all blueprint definitions and declarations. A
configuration with no blueprint definitions or declarations comes out
unchanged.

**Why:** native configurations keep working, and the output can be reviewed and
run like any other Collector configuration.

## A blueprint file is part of a configuration

A blueprint file holds definitions under `blueprints` at its root, so ordinary
merging, such as Helm values files, adds it to a configuration. Choosing and
merging the files is up to whoever runs NVCB.

**Why:** users bring their own blueprints, including published ones, with the
tools they already use.

## Each blueprint has its own version

The recommended layout is one blueprint per file. Each published definition has
a `version`, which follows semantic versioning, and a `description`.
Implementations ignore fields they do not use.

**Why:** users choose which version of a blueprint they use and when to move to
a newer one, and a new major version tells them their configuration may need to
change. Newer blueprint files still work with older implementations.

## Templates use a short list of functions

Templates can use Go's built-in template functions plus `default`, `required`,
`quote`, `fail`, and `toJson`. A parameter that is not set renders as an empty
string, and `required` rejects a missing or empty value.

**Why:** each function has a purpose: optional and required parameters, safe
strings, custom errors, and structured values. Leaving everything else out keeps
renders reproducible, keeps a blueprint from reading the cluster or the
surrounding chart, and makes blueprints work the same in every implementation.

## The golden tests define the behavior

The scenarios in `tests/` pair configurations with the output or error they must
produce. Every implementation must produce the same results.

**Why:** one executable definition keeps implementations consistent.
