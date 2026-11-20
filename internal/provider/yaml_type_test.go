package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestYAMLValueStringSemanticEquals(t *testing.T) {
	for _, test := range []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{
			name:  "document comments",
			left:  "# old document comment\nname: example # old inline comment\n",
			right: "# new document comment\nname: example # new inline comment\n",
			want:  true,
		},
		{
			name:  "document comments presence",
			left:  "name: example\n",
			right: "# new document comment\nname: example # new inline comment\n",
			want:  true,
		},
		{
			name:  "nested mapping and sequence comments",
			left:  "items:\n  # old item comment\n  - name: example # old name comment\n    details:\n      # old details comment\n      enabled: true\n",
			right: "items:\n  # new item comment\n  - name: example # new name comment\n    details:\n      # new details comment\n      enabled: true\n",
			want:  true,
		},
		{
			name:  "quoted hash is scalar data",
			left:  "value: \"text # one\"\n",
			right: "value: \"text # two\"\n",
			want:  false,
		},
		{
			name:  "block scalar hash is scalar data",
			left:  "value: |\n  text # one\n",
			right: "value: |\n  text # two\n",
			want:  false,
		},
		{
			name:  "value change",
			left:  "value: one\n",
			right: "value: two\n",
			want:  false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, diags := NewYAMLValue(test.left).StringSemanticEquals(context.Background(), NewYAMLValue(test.right))
			if got != test.want || diags.HasError() {
				t.Errorf("equal=%t diagnostics=%v, want equal=%t and no errors", got, diags, test.want)
			}
		})
	}
}

func TestYAMLValueStringSemanticEqualsInvalidAndUnexpectedValues(t *testing.T) {
	t.Run("invalid YAML", func(t *testing.T) {
		got, diags := NewYAMLValue("value: [").StringSemanticEquals(context.Background(), NewYAMLValue("value: []"))
		if got || diags.HasError() {
			t.Errorf("equal=%t diagnostics=%v, want unequal and no diagnostics", got, diags)
		}
	})

	t.Run("unexpected type", func(t *testing.T) {
		got, diags := NewYAMLValue("value: one").StringSemanticEquals(context.Background(), types.StringValue("value: one"))
		if got || !diags.HasError() {
			t.Errorf("equal=%t diagnostics=%v, want unequal with an error diagnostic", got, diags)
		}
	})
}
