package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// YAMLType is a string attribute type whose values are compared semantically
// (see NormalizeYAML) instead of byte-by-byte.
type YAMLType struct {
	basetypes.StringType
}

var _ basetypes.StringTypable = YAMLType{}

func (t YAMLType) Equal(o attr.Type) bool {
	other, ok := o.(YAMLType)
	if !ok {
		return false
	}
	return t.StringType.Equal(other.StringType)
}

func (t YAMLType) String() string { return "YAMLType" }

func (t YAMLType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return YAMLValue{StringValue: in}, nil
}

func (t YAMLType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	stringValue, ok := attrValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", attrValue)
	}
	v, diags := t.ValueFromString(ctx, stringValue)
	if diags.HasError() {
		return nil, fmt.Errorf("unexpected error converting StringValue to YAMLValue: %v", diags)
	}
	return v, nil
}

func (t YAMLType) ValueType(_ context.Context) attr.Value { return YAMLValue{} }

// YAMLValue holds a YAML document. Semantic equality makes Terraform keep the prior value
// (no diff) when config / remote YAML differ only in formatting or comments:
//   - during plan: prior state vs. configuration
//   - during refresh: prior state vs. value read from the workspace
type YAMLValue struct {
	basetypes.StringValue
}

var _ basetypes.StringValuableWithSemanticEquals = YAMLValue{}

func NewYAMLValue(s string) YAMLValue {
	return YAMLValue{StringValue: basetypes.NewStringValue(s)}
}

func (v YAMLValue) Equal(o attr.Value) bool {
	other, ok := o.(YAMLValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

func (v YAMLValue) Type(_ context.Context) attr.Type { return YAMLType{} }

func (v YAMLValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	newValue, ok := newValuable.(YAMLValue)
	if !ok {
		diags.AddError("Semantic equality check error",
			fmt.Sprintf("expected YAMLValue, got %T", newValuable))
		return false, diags
	}
	a, err := NormalizeYAML(v.ValueString())
	if err != nil {
		return false, diags // unparsable -> treat as different, validator reports the error
	}
	b, err := NormalizeYAML(newValue.ValueString())
	if err != nil {
		return false, diags
	}
	return a == b, diags
}
