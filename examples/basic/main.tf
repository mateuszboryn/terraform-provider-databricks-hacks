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
  host = var.databricks_host
}

provider "databricks-hacks" {
  host = var.databricks_host
}

data "databricks_sql_warehouse" "sql" {
  name = var.warehouse_name
}

# 1) Definition kept in a separate YAML file (recommended)
resource "databricks_metric_view" "sample_1" {
  provider     = databricks-hacks
  warehouse_id = data.databricks_sql_warehouse.sql.id
  catalog_name = var.catalog
  schema_name  = var.schema
  name         = "tf_mv_sample_1"
  definition = templatefile("${path.module}/metric_views/sample_1.yaml", {
    catalog = var.catalog
  })
}

# 2) Inline definition
resource "databricks_metric_view" "sample_2" {
  provider     = databricks-hacks
  warehouse_id = data.databricks_sql_warehouse.sql.id
  catalog_name = var.catalog
  schema_name  = var.schema
  name         = "tf_mv_sample_2"
  definition   = <<-YAML
    version: 1.1
    comment: Stock by brand (managed by Terraform)
    source: ${var.catalog}.warehouse_b2b.fact_inventory_snapshot
    joins:
      - name: product
        source: ${var.catalog}.warehouse_master_data.dim_product
        on: source.product_key = product.product_key
    dimensions:
      - name: Brand Code
        expr: product.brand
      - name: Snapshot Date
        expr: source.snapshot_date
    measures:
      - name: Total Stock Quantity
        expr: SUM(source.total_stock_quantity)
  YAML
}

output "metric_views" {
  value = [
    databricks_metric_view.sample_1.id,
    databricks_metric_view.sample_2.id,
  ]
}
