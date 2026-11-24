# Knowledge page that documents the metric view from main.tf, with two links:
# - the metric view itself in the page "Related assets" list
# - the YAML definition in source control in the page "Sources" list

# asset_scope_id is omitted: the metastore ID of the current workspace is looked up
resource "databricks_page_asset_link" "sample_metric_view" {
  provider   = databricks-hacks
  page       = databricks_page.sample_page.name
  category   = "RELATED_ASSETS"
  asset_type = "ASSET_TYPE_UC_TABLE"
  asset_id   = databricks_metric_view.sample_2.id
}

resource "databricks_page_asset_link" "sample_materialized_view" {
  provider   = databricks-hacks
  page       = databricks_page.sample_page.name
  category   = "RELATED_ASSETS"
  asset_type = "ASSET_TYPE_UC_TABLE"
  asset_id   = databricks_materialized_view.mv.id
}

# account-level asset: asset_scope_id is the account ID (or set DATABRICKS_ACCOUNT_ID)
resource "databricks_page_asset_link" "sample_source" {
  provider       = databricks-hacks
  page           = databricks_page.sample_page.name
  category       = "SOURCE"
  asset_type     = "ASSET_TYPE_EXTERNAL_URL"
  asset_scope_id = var.databricks_account_id
  asset_id       = "https://github.com/your-org/your-repo/blob/main/metric_views/sample_1.yaml"
}
