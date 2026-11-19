---
page_title: "databricks_metric_view Resource - databricks"
subcategory: ""
description: |-
  Manages a Unity Catalog metric view.
---

# databricks_metric_view (Resource)

Manages a Unity Catalog metric view defined by YAML. SQL operations run on the warehouse configured on this resource; the provider itself does not need a warehouse ID.

## Example Usage

```terraform

terraform {
  required_version = ">= 1.5"
  required_providers {
    databricks = {
      source  = "databricks/databricks"
      version = "~> 1.132"
    }
    databricks-hacks = {
      source = "mateuszboryn/databricks-hacks"
    }
  }
}

provider "databricks" {
  host = "https://your-workspace.azuredatabricks.net"
}
provider "databricks-hacks" {
  host = "https://your-workspace.azuredatabricks.net"
}

data "databricks_sql_warehouse" "sql" {
  name = "wh-name"
}

resource "databricks_metric_view" "example" {
  warehouse_id = data.databricks_sql_warehouse.sql.id
  catalog_name = "main"
  schema_name  = "analytics"
  name         = "revenue_by_day"

  definition = <<-YAML
    version: 1.1
    source: main.sales.orders
    dimensions:
      - name: Order Date
        expr: order_date
    measures:
      - name: Revenue
        expr: SUM(amount)
  YAML
}
```

## Schema

### Required

- `warehouse_id` (String) SQL warehouse ID used to create, update, and delete this metric view.
- `catalog_name` (String) Unity Catalog catalog containing the metric view. Changing it recreates the view.
- `schema_name` (String) Schema containing the metric view. Changing it recreates the view.
- `name` (String) Metric view name within the schema. Changing it recreates the view.
- `definition` (String) Metric view YAML. YAML is compared after normalization, so formatting-only changes do not produce a diff.

### Read-Only

- `id` (String) Fully qualified metric view name in `catalog.schema.name` format.

## Lifecycle

- Create runs `CREATE VIEW ... WITH METRICS LANGUAGE YAML` on `warehouse_id`.
- Read checks the object through the Unity Catalog Tables API and reads its YAML definition using `SHOW CREATE TABLE` on `warehouse_id`.
- Definition updates run `ALTER VIEW` on `warehouse_id`, preserving grants and tags.
- Delete runs `DROP VIEW IF EXISTS` on `warehouse_id`.
- Changes to `catalog_name`, `schema_name`, or `name` replace the resource. Changing `warehouse_id` does not replace the view.

## Import

Import with the fully qualified name. The resource configuration must also provide `warehouse_id`:

```shell
terraform import databricks_metric_view.example main.analytics.revenue_by_day
```

## Definition comparison

Databricks may reserialize the YAML. The resource uses semantic YAML equality, ignoring formatting, comments, and map key order while preserving list order. Meaningful definition changes produce an update and a normalized diff warning.