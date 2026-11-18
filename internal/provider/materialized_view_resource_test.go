package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/databricks/databricks-sdk-go"
	"github.com/databricks/databricks-sdk-go/apierr"
	"github.com/databricks/databricks-sdk-go/service/catalog"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	resourceschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

func TestMaterializedViewIsRegisteredAndHasRequestedSchema(t *testing.T) {
	var names []string
	for _, factory := range (&metricViewProvider{}).Resources(context.Background()) {
		var response resource.MetadataResponse
		factory().Metadata(context.Background(), resource.MetadataRequest{}, &response)
		names = append(names, response.TypeName)
	}
	if !contains(names, "databricks_materialized_view") {
		t.Fatalf("materialized view resource not registered: %v", names)
	}

	var response resource.SchemaResponse
	NewMaterializedViewResource().Schema(context.Background(), resource.SchemaRequest{}, &response)
	for _, name := range []string{
		"warehouse_id", "catalog_name", "schema_name", "name", "view_definition", "partitions",
		"cluster_keys", "properties", "data_format", "refresh_policy", "schedule", "trigger_on_update",
		"trigger_on_update_interval", "row_filter", "comment", "id",
	} {
		if _, ok := response.Schema.Attributes[name]; !ok {
			t.Errorf("resource schema is missing %q", name)
		}
	}
}

func TestMaterializedViewDefinitionUsesSemanticComparison(t *testing.T) {
	var response resource.SchemaResponse
	NewMaterializedViewResource().Schema(context.Background(), resource.SchemaRequest{}, &response)
	definition := response.Schema.Attributes["view_definition"].(resourceschema.StringAttribute)
	if definition.CustomType == nil {
		t.Fatal("view_definition needs a custom type for semantic comparison")
	}
	value, ok := definition.CustomType.ValueType(context.Background()).(basetypes.StringValuableWithSemanticEquals)
	if !ok {
		t.Fatal("view_definition must support semantic equality")
	}
	newValue, diags := definition.CustomType.ValueFromString(context.Background(), types.StringValue(" \n\tSELECT 1\r\n"))
	if diags.HasError() {
		t.Fatal(diags)
	}
	baseValue, diags := definition.CustomType.ValueFromString(context.Background(), types.StringValue("SELECT 1"))
	if diags.HasError() {
		t.Fatal(diags)
	}
	value = baseValue.(basetypes.StringValuableWithSemanticEquals)
	equal, diags := value.StringSemanticEquals(context.Background(), newValue)
	if diags.HasError() || !equal {
		t.Fatalf("expected equivalent definitions, equal=%t diagnostics=%v", equal, diags)
	}
	if len(definition.PlanModifiers) == 0 {
		t.Fatal("view_definition needs a plan modifier to suppress equivalent configuration changes")
	}
}

func TestBuildMaterializedViewSQLTrimsOnlyRequestedWhitespace(t *testing.T) {
	model := testMaterializedViewModel(t)
	model.ViewDefinition = newViewDefinitionValue(" \t\r\n\u00a0SELECT  1\nFROM source\u00a0\n\r\t ")
	for _, replace := range []bool{false, true} {
		statement, err := buildMaterializedViewSQL(context.Background(), model, replace)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(statement, "AS\n\u00a0SELECT  1\nFROM source\u00a0") {
			t.Errorf("unexpected query in statement: %q", statement)
		}
	}
}

func TestBuildMaterializedViewSQLIncludesOptionsAndEscapesValues(t *testing.T) {
	ctx := context.Background()
	model := testMaterializedViewModel(t)
	model.Partitions, _ = listValue(ctx, []string{"event date", "region"})
	model.Properties, _ = mapValue(ctx, map[string]string{"owner's key": "a'b", "z": "last"})
	model.DataFormat = types.StringValue("ICEBERG")
	model.RefreshPolicy = types.StringValue("INCREMENTAL STRICT")
	model.Comment = types.StringValue("manager's view")
	model.RowFilter = types.StringValue("tenant_filter ON (tenant_id)")

	statement, err := buildMaterializedViewSQL(ctx, model, false)
	if err != nil {
		t.Fatalf("build create SQL: %v", err)
	}
	for _, expected := range []string{
		"CREATE MATERIALIZED VIEW `main`.`analytics`.`daily sales`",
		"USING ICEBERG",
		"PARTITIONED BY (`event date`, `region`)",
		"COMMENT 'manager''s view'",
		"TBLPROPERTIES ('owner''s key' = 'a''b', 'z' = 'last')",
		"REFRESH POLICY INCREMENTAL STRICT",
		"SCHEDULE EVERY 1 DAY",
		"WITH ROW FILTER tenant_filter ON (tenant_id)",
		"AS\nSELECT event_date, COUNT(*) AS total FROM source GROUP BY event_date",
	} {
		if !strings.Contains(statement, expected) {
			t.Errorf("statement does not contain %q:\n%s", expected, statement)
		}
	}
	if strings.Contains(statement, "TRIGGER ON UPDATE") {
		t.Errorf("statement unexpectedly contains trigger clause:\n%s", statement)
	}

	model.Partitions = types.ListNull(types.StringType)
	model.ClusterKeys, _ = listValue(ctx, []string{"region", "event_date"})
	model.Schedule = types.StringNull()
	model.TriggerOnUpdate = types.BoolValue(true)
	model.TriggerOnUpdateInterval = types.StringValue("INTERVAL 5 MINUTES")
	statement, err = buildMaterializedViewSQL(ctx, model, true)
	if err != nil {
		t.Fatalf("build replacement SQL: %v", err)
	}
	for _, expected := range []string{
		"CREATE OR REPLACE MATERIALIZED VIEW",
		"CLUSTER BY (`region`, `event_date`)",
		"TRIGGER ON UPDATE AT MOST EVERY INTERVAL 5 MINUTES",
	} {
		if !strings.Contains(statement, expected) {
			t.Errorf("replacement statement does not contain %q:\n%s", expected, statement)
		}
	}
}

func TestBuildMaterializedViewSQLClusteringModes(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		key      string
		contains string
	}{
		{key: "AUTO", contains: "CLUSTER BY AUTO"},
		{key: "NONE", contains: ""},
	} {
		model := testMaterializedViewModel(t)
		model.ClusterKeys, _ = listValue(ctx, []string{test.key})
		statement, err := buildMaterializedViewSQL(ctx, model, false)
		if err != nil {
			t.Fatalf("build SQL for %s: %v", test.key, err)
		}
		if test.contains != "" && !strings.Contains(statement, test.contains) {
			t.Errorf("statement for %s is missing %q: %s", test.key, test.contains, statement)
		}
		if test.key == "NONE" && strings.Contains(statement, "CLUSTER BY") {
			t.Errorf("NONE must disable clustering, got: %s", statement)
		}
	}
}

func TestMaterializedViewValidationRejectsConflictsAndMalformedOptions(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		change func(*materializedViewModel)
		want   string
	}{
		{
			name: "partition and clustering conflict",
			change: func(model *materializedViewModel) {
				model.Partitions, _ = listValue(ctx, []string{"day"})
				model.ClusterKeys, _ = listValue(ctx, []string{"NONE"})
			},
			want: "cannot both be set",
		},
		{
			name: "schedule and trigger conflict",
			change: func(model *materializedViewModel) {
				model.TriggerOnUpdate = types.BoolValue(true)
			},
			want: "cannot both be enabled",
		},
		{
			name: "trigger interval requires trigger",
			change: func(model *materializedViewModel) {
				model.TriggerOnUpdateInterval = types.StringValue("INTERVAL 5 MINUTES")
			},
			want: "requires trigger_on_update",
		},
		{
			name: "invalid schedule",
			change: func(model *materializedViewModel) {
				model.Schedule = types.StringValue("EVERY 1 DAY; DROP TABLE source")
			},
			want: "schedule must be",
		},
		{
			name: "invalid cluster special value",
			change: func(model *materializedViewModel) {
				model.ClusterKeys, _ = listValue(ctx, []string{"AUTO", "region"})
			},
			want: "must be the only value",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := testMaterializedViewModel(t)
			test.change(&model)
			err := validateMaterializedViewModel(ctx, model)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected validation error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestParseMaterializedViewDDL(t *testing.T) {
	ddl := `CREATE MATERIALIZED VIEW ` + "`main`.`analytics`.`daily sales`" + `
USING ICEBERG
COMMENT 'manager''s view'
PARTITIONED BY (` + "`event date`, `region`" + `)
TBLPROPERTIES ('owner''s key' = 'a''b', 'z' = 'last')
REFRESH POLICY INCREMENTAL STRICT
SCHEDULE REFRESH CRON '0 0 8 * * ?' AT TIME ZONE 'America/Los_Angeles'
WITH ROW FILTER tenant_filter ON (tenant_id)
AS SELECT 'AS SCHEDULE' AS note, event_date FROM source;`

	parsed, err := parseMaterializedViewDDL(ddl)
	if err != nil {
		t.Fatalf("parse SHOW CREATE DDL: %v", err)
	}
	if parsed.DataFormat != "ICEBERG" || parsed.RefreshPolicy != "INCREMENTAL STRICT" {
		t.Errorf("unexpected format or refresh policy: %#v", parsed)
	}
	if parsed.Comment != "manager's view" || parsed.Properties["owner's key"] != "a'b" {
		t.Errorf("SQL string escaping was not decoded: %#v", parsed)
	}
	if strings.Join(parsed.Partitions, ",") != "event date,region" || len(parsed.ClusterKeys) != 0 {
		t.Errorf("unexpected partition/clustering state: %#v / %#v", parsed.Partitions, parsed.ClusterKeys)
	}
	if parsed.Schedule != "CRON '0 0 8 * * ?' AT TIME ZONE 'America/Los_Angeles'" {
		t.Errorf("unexpected schedule %q", parsed.Schedule)
	}
	if parsed.RowFilter != "tenant_filter ON (tenant_id)" {
		t.Errorf("unexpected row filter %q", parsed.RowFilter)
	}
	if parsed.ViewDefinition != "SELECT 'AS SCHEDULE' AS note, event_date FROM source" {
		t.Errorf("unexpected query %q", parsed.ViewDefinition)
	}
}

func TestParseMaterializedViewDDLTriggerAndClusterAuto(t *testing.T) {
	ddl := "CREATE OR REPLACE MATERIALIZED VIEW main.analytics.events CLUSTER BY AUTO " +
		"TRIGGER ON UPDATE AT MOST EVERY INTERVAL 5 MINUTES AS SELECT * FROM source"
	parsed, err := parseMaterializedViewDDL(ddl)
	if err != nil {
		t.Fatalf("parse DDL: %v", err)
	}
	if len(parsed.ClusterKeys) != 1 || parsed.ClusterKeys[0] != "AUTO" {
		t.Errorf("unexpected cluster keys: %v", parsed.ClusterKeys)
	}
	if !parsed.TriggerOnUpdate || parsed.TriggerOnUpdateInterval != "INTERVAL 5 MINUTES" {
		t.Errorf("unexpected trigger state: %#v", parsed)
	}
}

func TestParseMaterializedViewImportID(t *testing.T) {
	warehouse, name, ok := parseMaterializedViewImportID("warehouse-1|main.analytics.daily_view")
	if !ok || warehouse != "warehouse-1" || name != "main.analytics.daily_view" {
		t.Fatalf("unexpected import ID parse: %q, %q, %t", warehouse, name, ok)
	}
	for _, invalid := range []string{"main.analytics.daily_view", "|main.analytics.daily_view", "warehouse|main.analytics", "warehouse|main..view", "warehouse|main.schema.view.extra"} {
		if _, _, ok := parseMaterializedViewImportID(invalid); ok {
			t.Errorf("expected import ID %q to be rejected", invalid)
		}
	}
}

func TestCheckMaterializedViewInfo(t *testing.T) {
	tests := []struct {
		name      string
		info      *catalog.TableInfo
		err       error
		wantExist bool
		wantError string
	}{
		{name: "missing object", err: apierr.ErrNotFound},
		{name: "wrong object type", info: &catalog.TableInfo{FullName: "main.schema.table", TableType: catalog.TableTypeManaged}, wantError: "not a materialized view"},
		{name: "API error", err: errors.New("simulated catalog failure"), wantError: "simulated catalog failure"},
		{name: "empty response", wantError: "no table information"},
		{name: "materialized view", info: &catalog.TableInfo{FullName: "main.schema.view", TableType: catalog.TableTypeMaterializedView}, wantExist: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exists, err := checkMaterializedViewInfo(test.info, test.err)
			if exists != test.wantExist {
				t.Errorf("unexpected exists result %t", exists)
			}
			if test.wantError == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("expected error containing %q, got %v", test.wantError, err)
			}
		})
	}
}

func TestReadMaterializedViewUsesShowCreateResult(t *testing.T) {
	ddl := "CREATE MATERIALIZED VIEW `main`.`analytics`.`daily_view` USING ICEBERG " +
		"COMMENT 'ready' TBLPROPERTIES ('owner' = 'analytics') REFRESH POLICY FULL AS SELECT 1 AS id"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/api/2.0/sql/statements") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			WarehouseID string `json:"warehouse_id"`
			Statement   string `json:"statement"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode statement request: %v", err)
		}
		if request.WarehouseID != "warehouse-1" || !strings.HasPrefix(request.Statement, "SHOW CREATE TABLE") {
			t.Errorf("unexpected statement request %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"statement_id": "show-create",
			"status":       map[string]any{"state": "SUCCEEDED"},
			"result":       map[string]any{"data_array": [][]string{{ddl}}},
		})
	}))
	defer server.Close()

	client, err := databricks.NewWorkspaceClient(&databricks.Config{Host: server.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("create Databricks client: %v", err)
	}
	resource := &materializedViewResource{client: &clientData{w: client}}
	state := testMaterializedViewModel(t)
	updated, err := resource.readMaterializedView(context.Background(), state, &catalog.TableInfo{
		FullName:  "main.analytics.daily_view",
		TableType: catalog.TableTypeMaterializedView,
	})
	if err != nil {
		t.Fatalf("read materialized view: %v", err)
	}
	if updated.ID.ValueString() != "main.analytics.daily_view" || updated.ViewDefinition.ValueString() != "SELECT 1 AS id" {
		t.Errorf("unexpected identity/query in state: %#v", updated)
	}
	if updated.DataFormat.ValueString() != "ICEBERG" || updated.RefreshPolicy.ValueString() != "FULL" || updated.Comment.ValueString() != "ready" {
		t.Errorf("unexpected settings in state: %#v", updated)
	}
	properties, err := stringMap(context.Background(), updated.Properties)
	if err != nil || properties["owner"] != "analytics" {
		t.Errorf("unexpected state properties %#v, error %v", properties, err)
	}
}

func TestExecSQLWithResultReturnsRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"statement_id":"statement-id","status":{"state":"SUCCEEDED"},"result":{"data_array":[["result"]]}}`))
	}))
	defer server.Close()
	client, err := databricks.NewWorkspaceClient(&databricks.Config{Host: server.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("create Databricks client: %v", err)
	}
	result, err := (&clientData{w: client}).execSQLWithResult(context.Background(), "warehouse", "SHOW CREATE TABLE main.schema.view")
	if err != nil {
		t.Fatalf("execute statement: %v", err)
	}
	if result.Result == nil || len(result.Result.DataArray) != 1 || result.Result.DataArray[0][0] != "result" {
		t.Fatalf("unexpected SQL result: %#v", result.Result)
	}
}

func testMaterializedViewModel(t *testing.T) materializedViewModel {
	t.Helper()
	return materializedViewModel{
		WarehouseID:             types.StringValue("warehouse-1"),
		CatalogName:             types.StringValue("main"),
		SchemaName:              types.StringValue("analytics"),
		Name:                    types.StringValue("daily sales"),
		ViewDefinition:          newViewDefinitionValue("SELECT event_date, COUNT(*) AS total FROM source GROUP BY event_date"),
		Partitions:              types.ListNull(types.StringType),
		ClusterKeys:             types.ListNull(types.StringType),
		Properties:              types.MapNull(types.StringType),
		DataFormat:              types.StringNull(),
		RefreshPolicy:           types.StringNull(),
		Schedule:                types.StringValue("EVERY 1 DAY"),
		TriggerOnUpdate:         types.BoolNull(),
		TriggerOnUpdateInterval: types.StringNull(),
		RowFilter:               types.StringNull(),
		Comment:                 types.StringNull(),
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
