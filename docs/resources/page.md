---
page_title: "databricks_page Resource - databricks"
subcategory: ""
description: |-
  Manages a Databricks Knowledge page.
---

# databricks_page (Resource)

Manages a Databricks Knowledge page: a markdown document that belongs to a domain. Uses the [Knowledge Pages API](https://docs.databricks.com/api/knowledge/v1/page) (Beta). No SQL warehouse is required.

## Example Usage

```terraform
resource "databricks_page" "revenue" {
  domain       = "domains/0123456789abcdef"
  display_name = "Revenue"
  description  = "How revenue is defined and reported"
  synonyms     = ["sales", "income"]
  state        = "PUBLISHED"

  body = <<-MD
    # Revenue
    Revenue is the sum of `amount` for completed orders.
  MD
}
```

## Schema

### Required

- `domain` (String) Full domain resource name in the form `domains/{domain_id}` (not a bare ID).
- `display_name` (String) Page title, 1-200 characters. Published titles must be unique within the domain.

### Optional

- `body` (String) Markdown content of the page.
- `synonyms` (Set of String) Alternative names. At most 50, each 1-200 characters, without leading or trailing whitespace. The server deduplicates and sorts synonyms, so the attribute is a set; order in configuration does not matter. Removing the attribute clears all synonyms.
- `description` (String) Short description of the page.
- `state` (String) `DRAFT` (default) or `PUBLISHED`. Creating a published page requires Page Manager or Domain Admin rights.

### Read-Only

- `id` (String) Same as `name`.
- `name` (String) Server-assigned resource name in the form `pages/{page_id}`.

## Lifecycle

- Create sends `POST /api/2.0/knowledge/pages` with a random `request_id`, so SDK retries are idempotent.
- Read sends `GET /api/2.0/knowledge/pages/{page_id}`. If the page no longer exists it is removed from state and recreated on the next apply.
- Update sends `PATCH` with an `update_mask` that lists only the changed attributes. Every attribute, including `domain`, is updated in place.
- Delete sends `DELETE /api/2.0/knowledge/pages/{page_id}`.

Page ownership (`owner_ids`) is not managed. The page is owned by the identity Terraform authenticates as.

## Import

Import with the page name or the bare page ID:

```shell
terraform import databricks_page.revenue pages/1b4e28ba-2fa1-11d2-883f-0016d3cca427
terraform import databricks_page.revenue 1b4e28ba-2fa1-11d2-883f-0016d3cca427
```
