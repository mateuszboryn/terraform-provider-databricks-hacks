---
page_title: "databricks_materialized_view Resource - databricks"
subcategory: ""
description: |-
  Manages a Unity Catalog materialized view.
---

# databricks_materialized_view (Resource)

Manages a Unity Catalog materialized view with Databricks SQL. The resource uses `warehouse_id` for create, read, update, and delete operations; the provider itself only needs Databricks authentication.

## Example Usage

```terraform
terraform {
  required_providers {
    databricks-hacks = {
      source = "mateuszboryn/databricks-hacks"
    }
  }
}

provider "databricks-hacks" {
  host = "https://your-workspace.azuredatabricks.net"
}

resource "databricks_materialized_view" "daily_sales" {
  warehouse_id    = "warehouse-id"
  catalog_name    = "main"
  schema_name     = "analytics"
  name            = "daily_sales"
  view_definition = <<-SQL
    SELECT order_date, SUM(amount) AS total_sales
    FROM main.sales.orders
    GROUP BY order_date
  SQL

  partitions     = ["order_date"]
  data_format    = "DELTA"
  refresh_policy = "AUTO"
  schedule       = "EVERY 1 DAY"
  comment        = "Daily sales totals"
  properties = {
    owner = "analytics"
  }
}
```

The provider requires an SQL warehouse ID on each resource. The ID can come from the official Databricks provider's `databricks_sql_warehouse` data source if that provider is configured separately.

## Schema

### Required

- `warehouse_id` (String) SQL warehouse used for DDL and reads.
- `catalog_name` (String) Unity Catalog catalog. Changing it replaces the resource.
- `schema_name` (String) Unity Catalog schema. Changing it replaces the resource.
- `name` (String) Materialized view name. Changing it replaces the resource.
- `view_definition` (String) SQL query defining the materialized view. Leading and trailing spaces, tabs, carriage returns, and newlines are ignored when applying and comparing the query.

### Optional

- `partitions` (List of String) Partition columns; maps to `PARTITIONED BY`. Do not set together with `cluster_keys`.
- `cluster_keys` (List of String) Liquid clustering columns. Use `["AUTO"]` for automatic clustering or `["NONE"]` to disable clustering. Do not set together with `partitions`.
- `properties` (Map of String) Table properties written with `TBLPROPERTIES`.
- `data_format` (String) `DELTA` or `ICEBERG`; maps to `USING DELTA` or `USING ICEBERG`.
- `refresh_policy` (String) `AUTO`, `INCREMENTAL`, `INCREMENTAL STRICT`, or `FULL`.
- `schedule` (String) `EVERY number HOUR(S)`, `DAY(S)`, or `WEEK(S)`, or `CRON cron_string [AT TIME ZONE timezone_id]`. Mutually exclusive with `trigger_on_update`.
- `trigger_on_update` (Boolean) Refresh when an upstream source changes. Mutually exclusive with `schedule`.
- `trigger_on_update_interval` (String) Interval for `AT MOST EVERY`, for example `INTERVAL 5 MINUTES`. Requires `trigger_on_update = true`; Databricks enforces a minimum interval of one minute.
- `row_filter` (String) Contents of the `WITH ROW FILTER` clause, for example `tenant_filter ON (tenant_id)`.
- `comment` (String) Materialized view comment.

Optional settings are refreshed from Databricks during reads. If a setting is omitted, Databricks defaults and any existing remote value are reflected in state.

### Read-Only

- `id` (String) Fully qualified name in `catalog.schema.name` format.

## Lifecycle

- Create runs `CREATE MATERIALIZED VIEW` using the configured clauses and query.
- Read confirms the object is a materialized view through the Unity Catalog Tables API, then runs `SHOW CREATE TABLE` on `warehouse_id` to refresh the SQL-backed settings and definition.
- Updates run `CREATE OR REPLACE MATERIALIZED VIEW` with the complete configuration.
- Delete runs `DROP MATERIALIZED VIEW IF EXISTS`.
- Changes to `catalog_name`, `schema_name`, or `name` replace the Terraform resource. Changing `warehouse_id` does not replace the remote object.

`CREATE OR REPLACE` replaces the definition rather than applying a narrow alteration. Review Databricks' replacement effects on grants, ownership, and other metadata before applying updates.

## Databricks SQL references

- [CREATE MATERIALIZED VIEW](https://docs.databricks.com/aws/en/sql/language-manual/sql-ref-syntax-ddl-create-materialized-view) documents the format, partitioning, clustering, refresh policy, schedule, trigger, comment, properties, and row filter clauses.
- [SHOW CREATE TABLE](https://docs.databricks.com/aws/en/sql/language-manual/sql-ref-syntax-aux-show-create-table) returns a materialized view's DDL (Databricks Runtime 14.1 or later for materialized views). The resource uses this statement to read supported configuration.
- [Unity Catalog Tables API](https://docs.databricks.com/api/workspace/tables/get) is used to verify object existence and type.

## Import

The SQL read operation requires a warehouse, so include it in the import ID using `warehouse_id|catalog.schema.name`:

```shell
terraform import databricks_materialized_view.daily_sales 'warehouse-id|main.analytics.daily_sales'
```

After import, keep `warehouse_id` configured to the same SQL warehouse. The next refresh reads the remote definition and supported clauses with `SHOW CREATE TABLE`.