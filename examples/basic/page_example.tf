resource "databricks_page" "sample_page" {
  provider     = databricks-hacks
  domain       = local.sample_domain.name
  display_name = "SOME TEST DEPLOYED WITH TERRAFORM"
  description  = "SOME TEST DEPLOYED WITH TERRAFORM asdfasdf"
  synonyms     = ["sales", "income"]
  state        = "PUBLISHED"
  body         = file("page.md")
}
