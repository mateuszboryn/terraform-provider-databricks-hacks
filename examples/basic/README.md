# Sample usage: databricks_metric_view

Creates two metric views in `sample_workspace.wh_playground`:

* `tf_mv_inventory_snapshot`, defined in `metric_views/inventory_snapshot.yaml` and rendered with `templatefile`
* `tf_mv_brand_stock`, defined inline as a heredoc

## Setup

```bash
# 1. build the provider
../../terraform-provider-databricks-metricview/build.sh

# 2. point Terraform at the local build (edit the path in dev.tfrc first)
export TF_CLI_CONFIG_FILE=$PWD/dev.tfrc

# 3. auth + config
export DATABRICKS_TOKEN=<TOKEN>          # or profile / az login
cp terraform.tfvars.example terraform.tfvars   # fill host + warehouse_id
```

With `dev_overrides`, skip `terraform init`. Run `plan` and `apply` directly.

## Scenarios

1. **Create**: `terraform apply` creates both views.
2. **No-op**: run `terraform plan` again. It reports *No changes*, even though Databricks stores the YAML in a different format.
3. **Change in code**: edit a measure in `metric_views/inventory_snapshot.yaml`, then run `terraform plan`. The plan shows `~ definition` plus a warning with the normalized diff. `terraform apply` runs `ALTER VIEW`.
4. **Change in workspace (drift)**: for example, in the SQL editor run:
   `ALTER VIEW sample_workspace.wh_playground.tf_mv_brand_stock AS $$ ...different yaml... $$`
   `terraform plan` detects the drift and shows the update that restores the code version.
5. **Delete**: `terraform destroy`, or remove the resource block and apply. This runs `DROP VIEW IF EXISTS`.
6. **Import an existing view**:
   `terraform import databricks_metric_view.brand_stock sample_workspace.wh_playground.tf_mv_brand_stock`

Note: creating views in `wh_playground` requires CREATE TABLE privilege on the schema (environment rules apply: in prod, BI is read-only on warehouse).
