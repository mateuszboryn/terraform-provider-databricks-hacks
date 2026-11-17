# terraform-provider-databricks-metricview

Terraform provider (plugin framework, Go) with one resource: `databricks_metric_view`.

## Resource `databricks_metric_view`

| Argument | Required | Description |
| --- | --- | --- |
| `name` | yes | `catalog.schema.view_name`. Changing it destroys and recreates the view. |
| `definition` | yes | Metric view YAML (the body between `$$ ... $$`). |
| `id` | computed | Same as `name`. |

Provider arguments: `host`, `token`, `profile` (all optional, Databricks unified auth), `warehouse_id` (or env `DATABRICKS_WAREHOUSE_ID`).

## Lifecycle

| Terraform action | What runs in Databricks |
| --- | --- |
| Create | `CREATE VIEW <name> WITH METRICS LANGUAGE YAML AS $$ <definition> $$` (SQL warehouse) |
| Read / refresh | `GET /api/2.1/unity-catalog/tables/<name>` and reads `view_definition`. If the view is missing, it is removed from state, so the next apply recreates it. |
| Update | `ALTER VIEW <name> AS $$ <definition> $$`. This keeps grants and tags, which `CREATE OR REPLACE` would drop. |
| Delete | `DROP VIEW IF EXISTS <name>` |
| Import | `terraform import databricks_metric_view.x catalog.schema.view_name` |

## Change detection (normalized YAML diff)

Databricks re-serializes the YAML it stores. For example, it quotes `"on"`, folds long strings with `\`, and drops comments and blank lines. Comparing raw strings would therefore always show drift.

`definition` uses a custom type (`YAMLType`) with **semantic equality**. Both sides are normalized (`NormalizeYAML`) before they are compared:

* comments, indentation, blank lines and quoting style are ignored
* map keys are sorted, and list order is kept (dimension and measure order matters)
* scalars are compared as strings (`1.1` == `"1.1"`)
* whitespace inside strings is collapsed, so `>-`, `|` and single-line SQL compare equal

Result:

* **Code changed** -> the plan shows `~ definition` and a warning with the normalized `-`/`+` diff -> apply runs `ALTER VIEW`.
* **Changed in the workspace** (UI or SQL) -> the refresh reads the new YAML and it no longer matches the code -> the plan shows an update back to the code version.
* **Formatting-only differences** -> no changes.

Known limitation: collapsing whitespace also applies inside SQL string literals. So `'a  b'` and `'a b'` count as equal.

## Build

Requirements: Go 1.22+, Terraform 1.5+.

```bash
./build.sh          # go mod tidy, vet, test, build into $(go env GOPATH)/bin
```

Then use `examples/basic` (see its README).

## Layout

```
main.go                                 provider server (address registry.terraform.io/vfc/databricks-metricview)
internal/provider/provider.go           provider config, Databricks client, SQL execution
internal/provider/metric_view_resource.go  CRUD, import, plan diff warning
internal/provider/yaml_type.go          custom string type with semantic equality
internal/provider/yaml_normalize.go     YAML canonicalization + line diff
internal/provider/yaml_normalize_test.go unit tests
```
