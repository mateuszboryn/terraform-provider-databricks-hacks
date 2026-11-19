package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/databricks/databricks-sdk-go"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	resourceschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestProviderSchemaDoesNotRequireWarehouse(t *testing.T) {
	var resp provider.SchemaResponse
	(&metricViewProvider{}).Schema(context.Background(), provider.SchemaRequest{}, &resp)
	if _, ok := resp.Schema.Attributes["warehouse_id"]; ok {
		t.Fatal("provider schema must not contain warehouse_id")
	}
	for _, name := range []string{"host", "token", "profile"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("provider schema is missing %q", name)
		}
	}
}

func TestMetricViewSchemaHasSeparateArguments(t *testing.T) {
	var resp resource.SchemaResponse
	NewMetricViewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, name := range []string{"warehouse_id", "catalog_name", "schema_name", "name", "definition"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("resource schema is missing %q", name)
		}
	}
	warehouse, ok := resp.Schema.Attributes["warehouse_id"].(resourceschema.StringAttribute)
	if !ok || !warehouse.Required {
		t.Fatal("warehouse_id must be a required resource argument")
	}
	if _, ok := resp.Schema.Attributes["full_name"]; ok {
		t.Fatal("resource schema must use separate catalog, schema, and name arguments")
	}
}

func TestExecSQLUsesRequestedWarehouse(t *testing.T) {
	var warehouseIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/api/2.0/sql/statements") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			WarehouseID string `json:"warehouse_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode statement request: %v", err)
		}
		warehouseIDs = append(warehouseIDs, request.WarehouseID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"statement_id":"statement-id","status":{"state":"SUCCEEDED"}}`))
	}))
	defer server.Close()

	client, err := databricks.NewWorkspaceClient(&databricks.Config{Host: server.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("create Databricks client without provider-level warehouse: %v", err)
	}
	c := &clientData{w: client}
	for _, warehouseID := range []string{"warehouse-one", "warehouse-two"} {
		if err := c.execSQL(context.Background(), warehouseID, "SELECT 1"); err != nil {
			t.Fatalf("execute statement on %s: %v", warehouseID, err)
		}
	}
	if len(warehouseIDs) != 2 || warehouseIDs[0] != "warehouse-one" || warehouseIDs[1] != "warehouse-two" {
		t.Fatalf("unexpected warehouse IDs in requests: %v", warehouseIDs)
	}
}

func TestExecSQLRejectsMissingWarehouse(t *testing.T) {
	err := (&clientData{}).execSQL(context.Background(), "", "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "warehouse_id") {
		t.Fatalf("expected missing warehouse error, got %v", err)
	}
}

func TestExecSQLReturnsStatementFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"statement_id":"statement-id","status":{"state":"FAILED","error":{"message":"simulated failure"}}}`))
	}))
	defer server.Close()

	client, err := databricks.NewWorkspaceClient(&databricks.Config{Host: server.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("create Databricks client: %v", err)
	}
	err = (&clientData{w: client}).execSQL(context.Background(), "warehouse", "SELECT invalid")
	if err == nil || !strings.Contains(err.Error(), "simulated failure") {
		t.Fatalf("expected statement failure, got %v", err)
	}
}

func TestMetricViewImportNameParts(t *testing.T) {
	parts, ok := splitFullName("catalog.schema.view")
	if !ok || len(parts) != 3 || parts[0] != "catalog" || parts[1] != "schema" || parts[2] != "view" {
		t.Fatalf("unexpected import ID parts: %v, %t", parts, ok)
	}
	for _, invalid := range []string{"catalog.schema", "catalog..view", "catalog.schema.view.extra", "catalog.sch`ema.view"} {
		if _, ok := splitFullName(invalid); ok {
			t.Errorf("expected import ID %q to be rejected", invalid)
		}
	}
}

func TestMetricViewFullName(t *testing.T) {
	model := metricViewModel{
		CatalogName: types.StringValue("catalog"),
		SchemaName:  types.StringValue("schema"),
		Name:        types.StringValue("view"),
	}
	if got := metricViewFullName(model); got != "catalog.schema.view" {
		t.Fatalf("unexpected full name %q", got)
	}
}

func TestMetricViewReadUsesShowCreate(t *testing.T) {
	const yaml = "version: 1.1\nsource: sales\nmeasures:\n  - name: total\n    expr: SUM(amount)"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/unity-catalog/tables/"):
			_, _ = w.Write([]byte(`{"table_type":"METRIC_VIEW","view_definition":"stale: true"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/sql/statements"):
			var request struct {
				WarehouseID string `json:"warehouse_id"`
				Statement   string `json:"statement"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if request.WarehouseID != "warehouse-1" || request.Statement != "SHOW CREATE TABLE `main`.`analytics`.`daily view`" {
				t.Errorf("unexpected SQL request: %#v", request)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"statement_id": "id", "status": map[string]any{"state": "SUCCEEDED"}, "result": map[string]any{"data_array": [][]string{{"CREATE VIEW main.analytics.`daily view` COMMENT 'AS $$ not the body' WITH METRICS LANGUAGE YAML AS $$\n" + yaml + "\n$$"}}}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := databricks.NewWorkspaceClient(&databricks.Config{Host: server.URL, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	r := &metricViewResource{client: &clientData{w: client}}
	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	state := tfsdk.State{Schema: schemaResp.Schema}
	model := metricViewModel{ID: types.StringValue("main.analytics.daily view"), WarehouseID: types.StringValue("warehouse-1"), CatalogName: types.StringValue("main"), SchemaName: types.StringValue("analytics"), Name: types.StringValue("daily view"), Definition: NewYAMLValue("old: true")}
	if diags := state.Set(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	resp := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)
	if len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.State.Get(ctx, &model); diags.HasError() {
		t.Fatal(diags)
	}
	if strings.TrimSpace(model.Definition.ValueString()) != yaml {
		t.Fatalf("unexpected YAML: %q", model.Definition.ValueString())
	}
}

func TestParseMetricViewDDL(t *testing.T) {
	for _, test := range []struct {
		name      string
		ddl       string
		want      string
		wantError bool
	}{
		{name: "documented format", ddl: "CREATE VIEW x WITH METRICS\nLANGUAGE YAML\nAS $$\nversion: 1.1\nsource: sales\n$$", want: "\nversion: 1.1\nsource: sales\n"},
		{name: "case and semicolon", ddl: "create view x with metrics language yaml as $$source: sales$$;", want: "source: sales"},
		{name: "missing body", ddl: "CREATE VIEW x AS SELECT 1", wantError: true},
		{name: "empty body", ddl: "LANGUAGE YAML AS $$  $$", wantError: true},
		{name: "invalid yaml", ddl: "LANGUAGE YAML AS $$source: [$$", wantError: true},
		{name: "unterminated body", ddl: "LANGUAGE YAML AS $$source: sales", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseMetricViewDDL(test.ddl)
			if (err != nil) != test.wantError || got != test.want {
				t.Fatalf("got %q, error %v; want %q, error %t", got, err, test.want, test.wantError)
			}
		})
	}
}
