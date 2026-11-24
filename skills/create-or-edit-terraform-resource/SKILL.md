---
name: create-or-edit-terraform-resource
description: Create or edit Terraform resource
---

Terraform resource should have:
1. Read
2. Update
3. Delete
4. Import

Preferably use Databricks library already present in the project.
If not suitable or have insufficient abilities, then should use Databricks API https://docs.databricks.com/aws/en/reference/api.
Documentation should include HCL example in form:

```hcl
terraform {
  required_version = ">= 1.5"
  required_providers {
    databricks = {
      source  = "databricks/databricks"
      version = "~> 1.132"
    }
    databricks-hacks = {
      source = "mateuszboryn/databricks-hacks"
      version = "~> 0.1.10" # list git tags to get the latest version
    }
  }
}

provider "databricks" {
  host = "https://your-workspace.azuredatabricks.net"
}

provider "databricks-hacks" {
  host = "https://your-workspace.azuredatabricks.net"
}

# resource comes here
resource "databricks_page" "new_resource" {
  provider = databricks-hacks
  # ...
}
```

In folder `examples/basic/` create a separate file for new resource example demonstrating its use in very simple case. 
Reuse already existing resources as much as possible.
