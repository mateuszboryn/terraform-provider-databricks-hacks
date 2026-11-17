package provider

import (
	"strings"
	"testing"
)

// What the user writes in Terraform.
const userYAML = `
version: 1.1   # spec version
source: nonprod_emea.platinum_b2b.fact_inventory_snapshot
joins:
  - name: product
    source: nonprod_emea.platinum_master_data.dim_product
    on: source.product_key = product.product_key
measures:
  - name: Total Stock Quantity
    expr: SUM(source.total_stock_quantity)
  - name: ATP Share
    expr: >-
      MEASURE(` + "`ATP Stock Quantity`" + `) /
      NULLIF(MEASURE(` + "`Total Stock Quantity`" + `), 0)
`

// How Databricks returns the same view in view_definition (quoted "on", folded string).
const workspaceYAML = `version: 1.1

source: nonprod_emea.platinum_b2b.fact_inventory_snapshot

joins:
  - name: product
    source: nonprod_emea.platinum_master_data.dim_product
    "on": source.product_key = product.product_key

measures:
  - name: Total Stock Quantity
    expr: SUM(source.total_stock_quantity)

  - name: ATP Share
    expr: "MEASURE(` + "`ATP Stock Quantity`" + `) / NULLIF(MEASURE(` + "`Total Stock Quantity`" + `),\
      \ 0)"`

func mustNorm(t *testing.T, s string) string {
	t.Helper()
	n, err := NormalizeYAML(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFormattingOnlyDifferencesAreEqual(t *testing.T) {
	if mustNorm(t, userYAML) != mustNorm(t, workspaceYAML) {
		t.Fatalf("expected equal:\n%s\n---\n%s", mustNorm(t, userYAML), mustNorm(t, workspaceYAML))
	}
	d, err := YAMLDiff(userYAML, workspaceYAML)
	if err != nil || d != "" {
		t.Fatalf("expected empty diff, got %q (err %v)", d, err)
	}
}

func TestVersionNumberVsString(t *testing.T) {
	if mustNorm(t, "version: 1.1") != mustNorm(t, `version: "1.1"`) {
		t.Fatal("1.1 and \"1.1\" should be equal")
	}
}

func TestRealChangeIsDetected(t *testing.T) {
	changed := strings.Replace(workspaceYAML, "SUM(source.total_stock_quantity)", "SUM(source.total_valuated_stock_quantity)", 1)
	d, err := YAMLDiff(userYAML, changed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "- ") || !strings.Contains(d, "+ ") || !strings.Contains(d, "total_valuated_stock_quantity") {
		t.Fatalf("unexpected diff:\n%s", d)
	}
}

func TestListOrderMatters(t *testing.T) {
	a := "dimensions:\n  - expr: a\n  - expr: b\n"
	b := "dimensions:\n  - expr: b\n  - expr: a\n"
	if mustNorm(t, a) == mustNorm(t, b) {
		t.Fatal("dimension order change must be detected")
	}
}

func TestInvalidYAML(t *testing.T) {
	if _, err := NormalizeYAML("a: [1, 2"); err == nil {
		t.Fatal("expected error")
	}
}

func TestQuoteFullName(t *testing.T) {
	if got := quoteFullName("cat.sch.my_view"); got != "`cat`.`sch`.`my_view`" {
		t.Fatalf("got %s", got)
	}
	if _, ok := splitFullName("cat.sch"); ok {
		t.Fatal("two-part name must be rejected")
	}
}
