terraform {
  required_version = ">= 1.5"
  required_providers {
    # Local name "databricks" => resource type databricks_metric_view.
    # If you also use the official databricks/databricks provider in the same module,
    # give this one another local name (e.g. "dbx_mv") and add `provider = dbx_mv` to each resource.
    databricks = {
      source = "mateuszboryn/databricks-metricview"
    }
  }
}

provider "databricks" {
  host         = var.databricks_host
  warehouse_id = var.warehouse_id
  # Auth: DATABRICKS_TOKEN env var, `profile = "..."`, Azure CLI login, etc. (Databricks unified auth)
}

locals {
  prefix = "${var.catalog}.${var.schema}"
}

# 1) Definition kept in a separate YAML file (recommended)
resource "databricks_metric_view" "inventory_snapshot" {
  name       = "${local.prefix}.tf_mv_inventory_snapshot"
  definition = templatefile("${path.module}/metric_views/inventory_snapshot.yaml", {
    catalog = var.catalog
  })
}

# 2) Inline definition
resource "databricks_metric_view" "brand_stock" {
  name       = "${local.prefix}.tf_mv_brand_stock"
  definition = <<-YAML
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
    databricks_metric_view.inventory_snapshot.id,
    databricks_metric_view.brand_stock.id,
  ]
}
