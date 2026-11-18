package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func normalizeViewDefinition(s string) string {
	return strings.Trim(s, " \n\r\t")
}

type viewDefinitionType struct {
	basetypes.StringType
}

var _ basetypes.StringTypable = viewDefinitionType{}

func (t viewDefinitionType) Equal(other attr.Type) bool {
	o, ok := other.(viewDefinitionType)
	return ok && t.StringType.Equal(o.StringType)
}

func (viewDefinitionType) String() string { return "viewDefinitionType" }

func (viewDefinitionType) ValueFromString(_ context.Context, value basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return viewDefinitionValue{StringValue: value}, nil
}

func (t viewDefinitionType) ValueFromTerraform(ctx context.Context, value tftypes.Value) (attr.Value, error) {
	v, err := t.StringType.ValueFromTerraform(ctx, value)
	if err != nil {
		return nil, err
	}
	s, ok := v.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected view definition value type %T", v)
	}
	return viewDefinitionValue{StringValue: s}, nil
}

func (viewDefinitionType) ValueType(context.Context) attr.Value { return viewDefinitionValue{} }

type viewDefinitionValue struct {
	basetypes.StringValue
}

var _ basetypes.StringValuableWithSemanticEquals = viewDefinitionValue{}

func newViewDefinitionValue(s string) viewDefinitionValue {
	return viewDefinitionValue{StringValue: basetypes.NewStringValue(s)}
}

func (v viewDefinitionValue) Equal(other attr.Value) bool {
	o, ok := other.(viewDefinitionValue)
	return ok && v.StringValue.Equal(o.StringValue)
}

func (viewDefinitionValue) Type(context.Context) attr.Type { return viewDefinitionType{} }

func (v viewDefinitionValue) StringSemanticEquals(_ context.Context, other basetypes.StringValuable) (bool, diag.Diagnostics) {
	o, ok := other.(viewDefinitionValue)
	if !ok {
		var diags diag.Diagnostics
		diags.AddError("Semantic equality check error", fmt.Sprintf("expected viewDefinitionValue, got %T", other))
		return false, diags
	}
	if v.IsNull() || v.IsUnknown() || o.IsNull() || o.IsUnknown() {
		return v.Equal(o), nil
	}
	return normalizeViewDefinition(v.ValueString()) == normalizeViewDefinition(o.ValueString()), nil
}

type viewDefinitionPlanModifier struct{}

func (viewDefinitionPlanModifier) Description(context.Context) string {
	return "Preserves the prior definition when only surrounding whitespace changes."
}

func (m viewDefinitionPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (viewDefinitionPlanModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || req.PlanValue.IsNull() || req.PlanValue.IsUnknown() || req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return
	}
	if normalizeViewDefinition(req.PlanValue.ValueString()) == normalizeViewDefinition(req.StateValue.ValueString()) {
		resp.PlanValue = req.StateValue
	}
}
