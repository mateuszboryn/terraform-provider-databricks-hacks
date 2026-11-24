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

data "databricks_domains" "all" {
}

locals {
  sample_domain = [
    for d in data.databricks_domains.all.domains : d if d.tag_key == var.domain_tag_key
  ][0]
}
# output "domain_names" {
#   value = {for d in data.databricks_domains.all.domains : d.name => d.tag_key}
# }
# output "domain_sample_domain" {
#   value = local.sample_domain
# }
