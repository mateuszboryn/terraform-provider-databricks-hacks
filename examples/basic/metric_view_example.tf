resource "databricks_metric_view" "sample_2" {
  provider     = databricks-hacks
  warehouse_id = data.databricks_sql_warehouse.sql.id
  catalog_name = var.catalog_name
  schema_name  = var.schema_name
  name         = "test_tf_provider_metric_view_example"
  definition   = <<-YAML
    version: 1.1
    comment: Stock by brand (managed by Terraform)
    source: ${var.catalog_name}.${var.schema_name}.test_sample_data
    joins:
      - name: product
        source: ${var.catalog_name}.${var.schema_name}.test_sample_data_products
        on: source.product_code = product.product_code
    dimensions:
      - name: Product Name
        expr: product.product_name
      - name: Snapshot Date
        expr: source.snapshot_date
    measures:
      - name: Total Stock Quantity
        expr: SUM(source.stock_quantity)
      - name: Total Stock Value
        expr: SUM(source.stock_value)
      - name: Total sales qty
        expr: SUM(source.sales_quantity)
      - name: Total sales val
        expr: SUM(source.sales_value)
  YAML
}

output "metric_views" {
  value = [
    databricks_metric_view.sample_2.id,
  ]
}

resource "databricks_metric_view" "custom" {
  provider     = databricks-hacks
  for_each     = fileset("metric_views", "*/*.yaml")
  warehouse_id = data.databricks_sql_warehouse.sql.id
  catalog_name = var.catalog_name
  schema_name  = basename(dirname(each.key))
  name         = trimsuffix(basename(each.key), ".yaml")
  definition = templatefile("metric_views/${each.key}", {
    source_catalog = var.catalog_name
    catalog        = var.catalog_name
    schema_name    = var.schema_name
  })
}
