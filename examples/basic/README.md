# Sample usage: databricks_metric_view

Creates two metric views in `sample_workspace.wh_playground`:

* `tf_mv_sample_1`, defined in `metric_views/sample_1.yaml` and rendered with `templatefile`
* `tf_mv_sample_2`, defined inline as a heredoc

The official `databricks/databricks` provider looks up the SQL warehouse by name. Its ID is passed to the metric-view resources, which use the local `mateuszboryn/databricks-hacks` provider.

## Setup

```bash
# 1. build the custom provider from the repository root, using the provider name expected by dev.tfrc
go build -o terraform-provider-databricks-hacks.exe .

# 2. point Terraform at the local build (edit the override path in dev.tfrc to the repository root)
export TF_CLI_CONFIG_FILE=$PWD/dev.tfrc

# 3. auth + config
export DATABRICKS_TOKEN=<TOKEN>          # or profile / az login
cp terraform.tfvars.example terraform.tfvars   # fill host + warehouse_name

# 4. install the official Databricks provider
terraform init
```

The development override loads `databricks-hacks` from the local binary; `terraform init` installs the official provider used for the warehouse data source.

## Scenarios

1. **Create**: `terraform apply` creates both views.
2. **No-op**: run `terraform plan` again. It reports *No changes*, even though Databricks stores the YAML in a different format.
3. **Change in code**: edit a measure in `metric_views/sample_1.yaml`, then run `terraform plan`. The plan shows `~ definition` plus a warning with the normalized diff. `terraform apply` runs `ALTER VIEW`.
4. **Change in workspace (drift)**: for example, in the SQL editor run:
   `ALTER VIEW sample_workspace.wh_playground.tf_mv_sample_2 AS $$ ...different yaml... $$`
   `terraform plan` detects the drift and shows the update that restores the code version.
5. **Delete**: `terraform destroy`, or remove the resource block and apply. This runs `DROP VIEW IF EXISTS`.
6. **Import an existing view** (keep the warehouse lookup and resource `warehouse_id` configured):
   `terraform import databricks_metric_view.sample_2 'warehouse-id|sample_workspace.wh_playground.tf_mv_sample_2'`

Note: creating views in `wh_playground` requires CREATE TABLE privilege on the schema (environment rules apply: in prod, BI is read-only on warehouse).
