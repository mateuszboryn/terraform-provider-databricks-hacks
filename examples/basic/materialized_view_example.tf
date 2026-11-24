resource "databricks_materialized_view" "mv" {
  provider        = databricks-hacks
  warehouse_id    = data.databricks_sql_warehouse.sql.id
  catalog_name    = var.catalog_name
  schema_name     = var.schema_name
  name            = "test_tf_provider_materialized_view_example"
  view_definition = <<-SQLSQLSQL
    SELECT product_code, max(snapshot_date) max_snapshot_date from ${var.catalog_name}.${var.schema_name}.test_sample_data GROUP BY 1
  SQLSQLSQL
}
