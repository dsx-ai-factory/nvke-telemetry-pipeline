# Blueprint Kinds

Blueprints come in two kinds: `receivers` and `exporters`. The blueprint kinds share overlapping capabilities, but also distinct constraints.

## Receiver Blueprints

**Must**
- Declare one or more `receiver` components.
- Use the generated forward connector in **at least one** pipeline in the `exporters` list.

**Optionally**
- Declare one or more `processor` components.
- Declare one or more `extension` components.
- Declare one or more `connector` components.
- Define multiple pipelines and signals.

**Cannot**
- Declare any `exporter` components.
- Declare nested blueprints.

## Exporter Blueprints

**Must**
- Declare one or more `exporter` components.
- Use the generated forward connector in **at least one** pipeline in the `receivers` list.

**Optionally**
- Declare one or more `processor` components.
- Declare one or more `extension` components.
- Declare one or more `connector` components.
- Define multiple pipelines and signals.

**Cannot**
- Declare any `receiver` components.
- Declare nested blueprints.

For more examples on common success/failure scenarios, see our [Test Cases](../../tests/).
