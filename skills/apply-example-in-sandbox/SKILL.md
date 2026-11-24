---
name: apply-example-in-sandbox
description: Validate terraform provider by applying in sandbox Databricks environment.
---

## On Windows:
1. Compile from project's root `go build -o terraform-provider-databricks-hacks.exe .`
2. Set secrets (once per shell session). Never open, read, process or interpret this file:
```bash
. ./sandbox-secrets.sh
```
It will export TF_VAR_ variables used by the provider and also required by the example:
- DATABRICKS_TOKEN
- TF_VAR_...
3. change folder to `examples/basic/`
4. `terraform init` can be executed only if subsequent `terraform apply` fails with `Error: Inconsistent dependency lock file`
5. run `terraform apply`
6. Other examples from `examples/` can be run depending on the resource being tested.

Never open, read, process or interpret `sandbox-secrets.sh`.

## On MacOS

TODO: implement

## On Linux

TODO: implement
