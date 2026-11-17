variable "databricks_host" {
  description = "Workspace URL, e.g. https://adb-1234567890123456.7.azuredatabricks.net"
  type        = string
}

variable "warehouse_id" {
  description = "SQL warehouse ID used to run CREATE / ALTER / DROP VIEW"
  type        = string
}

variable "catalog" {
  description = "Catalog for source tables and metric views (env_region, e.g. sample_workspace)"
  type        = string
  default     = "sample_workspace"
}

variable "schema" {
  description = "Schema where metric views are created"
  type        = string
  default     = "wh_playground"
}
