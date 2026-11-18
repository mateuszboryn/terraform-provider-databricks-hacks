package provider

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestNormalizeViewDefinition(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
	}{
		{"all requested whitespace", " \t\r\nSELECT  1\nFROM source \t\r\n", "SELECT  1\nFROM source"},
		{"no whitespace", "SELECT 1", "SELECT 1"},
		{"only whitespace", " \t\r\n", ""},
		{"other whitespace", "\u00a0SELECT 1\v", "\u00a0SELECT 1\v"},
		{"empty", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeViewDefinition(test.input); got != test.want {
				t.Errorf("normalizeViewDefinition(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestViewDefinitionSemanticEquality(t *testing.T) {
	null := viewDefinitionValue{StringValue: types.StringNull()}
	unknown := viewDefinitionValue{StringValue: types.StringUnknown()}
	for _, test := range []struct {
		name  string
		left  viewDefinitionValue
		right basetypes.StringValuable
		want  bool
		err   bool
	}{
		{"edge whitespace", newViewDefinitionValue("\n SELECT 1\t"), newViewDefinitionValue("SELECT 1"), true, false},
		{"internal whitespace", newViewDefinitionValue("SELECT  1"), newViewDefinitionValue("SELECT 1"), false, false},
		{"other whitespace", newViewDefinitionValue("\u00a0SELECT 1"), newViewDefinitionValue("SELECT 1"), false, false},
		{"different query", newViewDefinitionValue("SELECT 1"), newViewDefinitionValue("SELECT 2"), false, false},
		{"null versus empty", null, newViewDefinitionValue(""), false, false},
		{"unknown versus empty", unknown, newViewDefinitionValue(""), false, false},
		{"null values", null, null, true, false},
		{"unexpected type", newViewDefinitionValue("SELECT 1"), types.StringValue("SELECT 1"), false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, diags := test.left.StringSemanticEquals(context.Background(), test.right)
			if got != test.want || diags.HasError() != test.err {
				t.Errorf("equal=%t errors=%v, want equal=%t error=%t", got, diags, test.want, test.err)
			}
		})
	}
}

func TestViewDefinitionPlanModifier(t *testing.T) {
	for _, test := range []struct {
		name                string
		config, plan, state types.String
		want                types.String
	}{
		{"equivalent", types.StringValue(" SELECT 1\n"), types.StringValue(" SELECT 1\n"), types.StringValue("SELECT 1"), types.StringValue("SELECT 1")},
		{"different query", types.StringValue("SELECT 2"), types.StringValue("SELECT 2"), types.StringValue("SELECT 1"), types.StringValue("SELECT 2")},
		{"internal whitespace", types.StringValue("SELECT  1"), types.StringValue("SELECT  1"), types.StringValue("SELECT 1"), types.StringValue("SELECT  1")},
		{"unknown config", types.StringUnknown(), types.StringUnknown(), types.StringValue("SELECT 1"), types.StringUnknown()},
		{"unknown state", types.StringValue("SELECT 1"), types.StringValue("SELECT 1"), types.StringUnknown(), types.StringValue("SELECT 1")},
		{"null state", types.StringValue("SELECT 1"), types.StringValue("SELECT 1"), types.StringNull(), types.StringValue("SELECT 1")},
		{"null plan", types.StringNull(), types.StringNull(), types.StringValue("SELECT 1"), types.StringNull()},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := planmodifier.StringRequest{ConfigValue: test.config, PlanValue: test.plan, StateValue: test.state}
			response := planmodifier.StringResponse{PlanValue: test.plan}
			(viewDefinitionPlanModifier{}).PlanModifyString(context.Background(), request, &response)
			if !response.PlanValue.Equal(test.want) || response.Diagnostics.HasError() {
				t.Errorf("planned value %v, diagnostics %v, want %v", response.PlanValue, response.Diagnostics, test.want)
			}
		})
	}
}

func TestViewDefinitionTypeTerraformConversion(t *testing.T) {
	for _, test := range []struct {
		name  string
		value tftypes.Value
		want  viewDefinitionValue
	}{
		{"known", tftypes.NewValue(tftypes.String, " SELECT 1\n"), newViewDefinitionValue(" SELECT 1\n")},
		{"null", tftypes.NewValue(tftypes.String, nil), viewDefinitionValue{StringValue: types.StringNull()}},
		{"unknown", tftypes.NewValue(tftypes.String, tftypes.UnknownValue), viewDefinitionValue{StringValue: types.StringUnknown()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := (viewDefinitionType{}).ValueFromTerraform(context.Background(), test.value)
			if err != nil || !test.want.Equal(value) {
				t.Errorf("converted %v, error %v, want %v", value, err, test.want)
			}
		})
	}
}

func TestParseMaterializedViewDefinitionWhitespace(t *testing.T) {
	parsed, err := parseMaterializedViewDDL("CREATE MATERIALIZED VIEW main.schema.view AS \t\r\n\u00a0SELECT  1\nFROM source\u00a0 \t;\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ViewDefinition != "\u00a0SELECT  1\nFROM source\u00a0" {
		t.Errorf("unexpected parsed definition %q", parsed.ViewDefinition)
	}
}

func TestViewDefinitionResourcePlan(t *testing.T) {
	ctx := context.Background()
	server := providerserver.NewProtocol6(New("test")())()
	schema, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil || len(schema.Diagnostics) != 0 {
		t.Fatalf("get provider schema: %v, %v", err, schema.Diagnostics)
	}
	objectType := schema.ResourceSchemas["databricks_materialized_view"].ValueType().(tftypes.Object)
	config, prior, proposed := map[string]tftypes.Value{}, map[string]tftypes.Value{}, map[string]tftypes.Value{}
	for name, typ := range objectType.AttributeTypes {
		config[name] = tftypes.NewValue(typ, nil)
		prior[name] = tftypes.NewValue(typ, nil)
		proposed[name] = tftypes.NewValue(typ, nil)
	}
	for name, value := range map[string]string{
		"warehouse_id": "warehouse", "catalog_name": "main", "schema_name": "analytics", "name": "view",
	} {
		config[name] = tftypes.NewValue(tftypes.String, value)
		prior[name] = config[name]
		proposed[name] = config[name]
	}
	prior["view_definition"] = tftypes.NewValue(tftypes.String, "SELECT 1")
	config["view_definition"] = tftypes.NewValue(tftypes.String, " \nSELECT 1\t")
	proposed["view_definition"] = config["view_definition"]
	for name, value := range map[string]tftypes.Value{
		"id":                tftypes.NewValue(tftypes.String, "main.analytics.view"),
		"data_format":       tftypes.NewValue(tftypes.String, "DELTA"),
		"refresh_policy":    tftypes.NewValue(tftypes.String, "AUTO"),
		"trigger_on_update": tftypes.NewValue(tftypes.Bool, false),
	} {
		prior[name] = value
		proposed[name] = value
	}
	makeDynamic := func(values map[string]tftypes.Value) *tfprotov6.DynamicValue {
		v, err := tfprotov6.NewDynamicValue(objectType, tftypes.NewValue(objectType, values))
		if err != nil {
			t.Fatal(err)
		}
		return &v
	}
	for _, test := range []struct {
		name       string
		attribute  string
		value      tftypes.Value
		wantNoDiff bool
	}{
		{name: "whitespace only", wantNoDiff: true},
		{name: "different SQL", attribute: "view_definition", value: tftypes.NewValue(tftypes.String, "SELECT 2")},
		{name: "different comment", attribute: "comment", value: tftypes.NewValue(tftypes.String, "updated")},
		{name: "different name", attribute: "name", value: tftypes.NewValue(tftypes.String, "other_view")},
		{name: "unknown comment", attribute: "comment", value: tftypes.NewValue(tftypes.String, tftypes.UnknownValue)},
	} {
		t.Run(test.name, func(t *testing.T) {
			currentConfig, currentProposed := maps.Clone(config), maps.Clone(proposed)
			if test.attribute != "" {
				currentConfig[test.attribute] = test.value
				currentProposed[test.attribute] = test.value
			}
			response, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
				TypeName: "databricks_materialized_view", Config: makeDynamic(currentConfig),
				PriorState: makeDynamic(prior), ProposedNewState: makeDynamic(currentProposed),
			})
			if err != nil || len(response.Diagnostics) != 0 {
				t.Fatalf("plan resource: %v, %v", err, response.Diagnostics)
			}
			planned, err := response.PlannedState.Unmarshal(objectType)
			if err != nil {
				t.Fatal(err)
			}
			if noDiff := planned.Equal(tftypes.NewValue(objectType, prior)); noDiff != test.wantNoDiff {
				t.Errorf("no diff = %t, want %t; planned: %v", noDiff, test.wantNoDiff, planned)
			}
			if test.attribute != "" {
				var attributes map[string]tftypes.Value
				if err := planned.As(&attributes); err != nil {
					t.Fatal(err)
				}
				if !attributes[test.attribute].Equal(test.value) {
					t.Errorf("planned %s = %v, want %v", test.attribute, attributes[test.attribute], test.value)
				}
			}
		})
	}
}

func TestViewDefinitionResourceReadSemanticEquality(t *testing.T) {
	query := "SELECT 1"
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/unity-catalog/tables/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "main.analytics.view", "table_type": "MATERIALIZED_VIEW"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/sql/statements"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statement_id": "show-create", "status": map[string]any{"state": "SUCCEEDED"},
				"result": map[string]any{"data_array": [][]string{{"CREATE MATERIALIZED VIEW main.analytics.view AS " + query}}},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer endpoint.Close()
	ctx := context.Background()
	server := providerserver.NewProtocol6(New("test")())()
	schema, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	providerType := schema.Provider.ValueType().(tftypes.Object)
	providerConfig, err := tfprotov6.NewDynamicValue(providerType, tftypes.NewValue(providerType, map[string]tftypes.Value{
		"host": tftypes.NewValue(tftypes.String, endpoint.URL), "token": tftypes.NewValue(tftypes.String, "test-token"),
		"profile": tftypes.NewValue(tftypes.String, nil),
	}))
	if err != nil {
		t.Fatal(err)
	}
	configured, err := server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: &providerConfig})
	if err != nil || len(configured.Diagnostics) != 0 {
		t.Fatalf("configure provider: %v, %v", err, configured.Diagnostics)
	}
	objectType := schema.ResourceSchemas["databricks_materialized_view"].ValueType().(tftypes.Object)
	state := map[string]tftypes.Value{}
	for name, typ := range objectType.AttributeTypes {
		state[name] = tftypes.NewValue(typ, nil)
	}
	for name, value := range map[string]string{
		"warehouse_id": "warehouse", "catalog_name": "main", "schema_name": "analytics", "name": "view",
		"id": "main.analytics.view", "view_definition": " \nSELECT 1\t",
	} {
		state[name] = tftypes.NewValue(tftypes.String, value)
	}
	currentState, err := tfprotov6.NewDynamicValue(objectType, tftypes.NewValue(objectType, state))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ query, want string }{
		{"SELECT 1", " \nSELECT 1\t"},
		{"SELECT 2", "SELECT 2"},
	} {
		query = test.query
		response, err := server.ReadResource(ctx, &tfprotov6.ReadResourceRequest{
			TypeName: "databricks_materialized_view", CurrentState: &currentState,
		})
		if err != nil || len(response.Diagnostics) != 0 {
			t.Fatalf("read resource: %v, %v", err, response.Diagnostics)
		}
		value, err := response.NewState.Unmarshal(objectType)
		if err != nil {
			t.Fatal(err)
		}
		var attributes map[string]tftypes.Value
		if err := value.As(&attributes); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := attributes["view_definition"].As(&got); err != nil || got != test.want {
			t.Errorf("query %q: definition = %q, error %v, want %q", query, got, err, test.want)
		}
	}
}
