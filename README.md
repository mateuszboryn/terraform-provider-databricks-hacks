# terraform-provider-databricks-metricview

Terraform provider (plugin framework, Go) with one resource: `databricks_metric_view`.

## Resource `databricks_metric_view`

| Argument | Required | Description |
| --- | --- | --- |
| `warehouse_id` | yes | SQL warehouse ID used to create, update, and delete this view. |
| `catalog_name` | yes | Unity Catalog catalog containing the metric view. Changing it destroys and recreates the view. |
| `schema_name` | yes | Schema containing the metric view. Changing it destroys and recreates the view. |
| `name` | yes | Metric view name within the schema. Changing it destroys and recreates the view. |
| `definition` | yes | Metric view YAML (the body between `$$ ... $$`). |
| `id` | computed | Fully qualified name: `catalog.schema.name`. |

Provider arguments: `host`, `token`, and `profile` are optional and use Databricks unified authentication, including environment variables and configured profiles. The provider does not require a SQL warehouse during initialization.

## Lifecycle

| Terraform action | What runs in Databricks |
| --- | --- |
| Create | `CREATE VIEW <catalog.schema.name> WITH METRICS LANGUAGE YAML AS $$ <definition> $$` (the resource's SQL warehouse) |
| Read / refresh | Checks the object through the Unity Catalog Tables API, then reads its YAML from `SHOW CREATE TABLE` on the resource's SQL warehouse. If the view is missing, it is removed from state, so the next apply recreates it. |
| Update | `ALTER VIEW <catalog.schema.name> AS $$ <definition> $$` on the resource's SQL warehouse. This keeps grants and tags, which `CREATE OR REPLACE` would drop. |
| Delete | `DROP VIEW IF EXISTS <catalog.schema.name>` on the resource's SQL warehouse |
| Import | `terraform import databricks_metric_view.x catalog.schema.view_name` |

Changing `warehouse_id` updates the resource's warehouse setting without replacing the view. Changes to the catalog, schema, or view name replace it. Imports keep the `catalog.schema.name` ID format; configure `warehouse_id` on the resource before importing.

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

## Migration from provider-level warehouse configuration

Remove `warehouse_id` from the provider block and add it to each `databricks_metric_view` resource. Split each previous three-part `name` into `catalog_name`, `schema_name`, and the final `name` component. Existing IDs and import IDs remain `catalog.schema.name`.

## Layout

```
main.go                                 provider server (address registry.terraform.io/vfc/databricks-metricview)
internal/provider/provider.go           provider config, Databricks client, SQL execution
internal/provider/metric_view_resource.go  CRUD, import, plan diff warning
internal/provider/yaml_type.go          custom string type with semantic equality
internal/provider/yaml_normalize.go     YAML canonicalization + line diff
internal/provider/yaml_normalize_test.go unit tests
```
