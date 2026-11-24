---
page_title: "databricks_page_asset_link Resource - databricks"
subcategory: ""
description: |-
  Manages a link from a Databricks Knowledge page to an asset.
---

# databricks_page_asset_link (Resource)

Manages a link from a [Knowledge page](page.md) to an asset. A link appears in the page **Sources** list (`SOURCE`) or in its **Related assets** list (`RELATED_ASSETS`). It can point to a Unity Catalog table or view, a workspace dashboard, Genie space, notebook, app or SQL query, another page, a user, or an external URL. Uses the [Knowledge Asset Links API](https://docs.databricks.com/api/knowledge/v1/asset-link) (Beta). No SQL warehouse is required.

Links cannot be changed after they are created, so changing any argument deletes the link and creates a new one.

## Example Usage

```terraform
terraform {
  required_version = ">= 1.5"
  required_providers {
    databricks = {
      source  = "databricks/databricks"
      version = "~> 1.132"
    }
    databricks-hacks = {
      source  = "mateuszboryn/databricks-hacks"
      version = "~> 0.1.11"
    }
  }
}

provider "databricks" {
  host = "https://your-workspace.azuredatabricks.net"
}

provider "databricks-hacks" {
  host = "https://your-workspace.azuredatabricks.net"
}

data "databricks_domains" "all" {
}

locals {
  sample_domain = [
    for d in data.databricks_domains.all.domains : d if d.tag_key == "Sample domain name"
  ][0]
}

resource "databricks_page" "revenue" {
  provider     = databricks-hacks
  domain       = local.sample_domain.name
  display_name = "Revenue"
  body         = "Revenue is the sum of `amount` for completed orders."
}

# Unity Catalog table or view. asset_scope_id (the metastore ID) is looked up when omitted.
resource "databricks_page_asset_link" "orders_table" {
  provider   = databricks-hacks
  page       = databricks_page.revenue.name
  category   = "RELATED_ASSETS"
  asset_type = "ASSET_TYPE_UC_TABLE"
  asset_id   = "main.sales.orders"
}

# Workspace asset. asset_scope_id (the workspace ID) is looked up when omitted.
resource "databricks_page_asset_link" "revenue_dashboard" {
  provider   = databricks-hacks
  page       = databricks_page.revenue.name
  category   = "RELATED_ASSETS"
  asset_type = "ASSET_TYPE_WORKSPACE_DASHBOARD"
  asset_id   = "01f19285320a1e7cab564b13e2a430af"
}

# Account-level asset. asset_scope_id is the account ID.
resource "databricks_page_asset_link" "definition" {
  provider       = databricks-hacks
  page           = databricks_page.revenue.name
  category       = "SOURCE"
  asset_type     = "ASSET_TYPE_EXTERNAL_URL"
  asset_scope_id = "00000000-0000-0000-0000-000000000000"
  asset_id       = "https://github.com/your-org/your-repo/blob/main/revenue.md"
}
```

## Schema

### Required

- `page` (String) Page resource name in the form `pages/{page_id}`, for example `databricks_page.revenue.name`.
- `category` (String) `SOURCE` (Sources list) or `RELATED_ASSETS` (Related assets list).
- `asset_type` (String) Target type. One of `ASSET_TYPE_UC_TABLE`, `ASSET_TYPE_WORKSPACE_DASHBOARD`, `ASSET_TYPE_WORKSPACE_GENIE_AGENT`, `ASSET_TYPE_WORKSPACE_NOTEBOOK`, `ASSET_TYPE_WORKSPACE_APP`, `ASSET_TYPE_WORKSPACE_DBSQL_QUERY`, `ASSET_TYPE_ACCOUNT_PAGE`, `ASSET_TYPE_ACCOUNT_USER`, `ASSET_TYPE_EXTERNAL_URL`.
- `asset_id` (String) The target's identifier within the scope. For Databricks assets, use the bare identifier, not a resource name: the table full name, the dashboard, query, Genie space or app ID, the notebook ID, the page ID (not `pages/...`) or the user ID. For external links, use the full URL.

### Optional

- `asset_scope_id` (String) The scope that contains the asset, as a bare ID:
  - UC table: metastore ID. If omitted, the metastore of the current workspace is used.
  - Workspace assets: workspace ID. If omitted, the current workspace ID is used. For queries, Genie spaces, dashboards and apps, the API requires this to be the current workspace.
  - Account pages, users and external URLs: account ID. If omitted, it comes from `DATABRICKS_ACCOUNT_ID` or `account_id` in the `~/.databrickscfg` profile. If neither is set, apply fails.

### Read-Only

- `id` (String) Same as `name`.
- `name` (String) Server-assigned resource name in the form `pages/{page_id}/asset-link-categories/{category}/asset-links/{link_id}`.
- `resolved_asset_id` (String) Canonical asset ID returned by the server, such as the current table full name or the native UUID. `asset_id` keeps the configured value, so equivalent inputs do not cause replacement.
- `create_time` (String) Creation time (RFC 3339).

## Lifecycle

- Create sends `POST /api/2.0/knowledge/pages/{page_id}/asset-link-categories/{category}/asset-links`. The API is idempotent: if the page already has the same link in that category, the existing link is returned and adopted.
- Read sends `GET /api/2.0/knowledge/{name}`. The API also returns `NOT_FOUND` when the target can no longer be resolved, for example when the table was dropped or the caller has lost access to it. In both cases the link is removed from state and recreated on the next apply.
- There is no update. Changing any argument replaces the link.
- Delete sends `DELETE /api/2.0/knowledge/{name}`. A link that is already gone is ignored.

## Import

Import with the full link name:

```shell
terraform import databricks_page_asset_link.orders_table pages/1b4e28ba-2fa1-11d2-883f-0016d3cca427/asset-link-categories/RELATED_ASSETS/asset-links/7c9e6679-7425-40de-944b-e07fc1f90ae7
```
