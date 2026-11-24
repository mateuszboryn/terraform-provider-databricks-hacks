variable "databricks_host" {
  description = "Workspace URL, e.g. https://adb-1234567890123456.7.azuredatabricks.net"
  type        = string
}

variable "databricks_account_id" {
  description = "uuid-like"
  type        = string
}

variable "warehouse_name" {
  description = "Name of the SQL warehouse looked up by the official Databricks provider"
  type        = string
}

variable "catalog_name" {
  description = "Catalog for source tables and metric views (env_region, e.g. sample_workspace)"
  type        = string
}

variable "schema_name" {
  description = "Schema where metric views are created"
  type        = string
}

variable "domain_tag_key" {}
