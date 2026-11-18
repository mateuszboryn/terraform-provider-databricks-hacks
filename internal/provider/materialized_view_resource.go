package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/databricks/databricks-sdk-go/apierr"
	"github.com/databricks/databricks-sdk-go/service/catalog"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

var (
	_ resource.Resource                     = &materializedViewResource{}
	_ resource.ResourceWithConfigure        = &materializedViewResource{}
	_ resource.ResourceWithImportState      = &materializedViewResource{}
	_ resource.ResourceWithConfigValidators = &materializedViewResource{}
	_ resource.ResourceWithModifyPlan       = &materializedViewResource{}
)

func NewMaterializedViewResource() resource.Resource { return &materializedViewResource{} }

type materializedViewResource struct {
	client *clientData
}

type materializedViewModel struct {
	ID                      types.String        `tfsdk:"id"`
	WarehouseID             types.String        `tfsdk:"warehouse_id"`
	CatalogName             types.String        `tfsdk:"catalog_name"`
	SchemaName              types.String        `tfsdk:"schema_name"`
	Name                    types.String        `tfsdk:"name"`
	ViewDefinition          viewDefinitionValue `tfsdk:"view_definition"`
	Partitions              types.List          `tfsdk:"partitions"`
	ClusterKeys             types.List          `tfsdk:"cluster_keys"`
	Properties              types.Map           `tfsdk:"properties"`
	DataFormat              types.String        `tfsdk:"data_format"`
	RefreshPolicy           types.String        `tfsdk:"refresh_policy"`
	Schedule                types.String        `tfsdk:"schedule"`
	TriggerOnUpdate         types.Bool          `tfsdk:"trigger_on_update"`
	TriggerOnUpdateInterval types.String        `tfsdk:"trigger_on_update_interval"`
	RowFilter               types.String        `tfsdk:"row_filter"`
	Comment                 types.String        `tfsdk:"comment"`
}

func (r *materializedViewResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "databricks_materialized_view"
}

func (r *materializedViewResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Unity Catalog materialized view using Databricks SQL.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Fully qualified materialized view name in catalog.schema.name format.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"warehouse_id": schema.StringAttribute{
				Required:    true,
				Description: "SQL warehouse used for materialized view DDL and reads.",
			},
			"catalog_name": schema.StringAttribute{
				Required:      true,
				Description:   "Unity Catalog catalog. Changing it replaces the resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"schema_name": schema.StringAttribute{
				Required:      true,
				Description:   "Unity Catalog schema. Changing it replaces the resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Materialized view name. Changing it replaces the resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"view_definition": schema.StringAttribute{
				Required:      true,
				CustomType:    viewDefinitionType{},
				Description:   "SQL query defining the materialized view. Leading and trailing spaces, tabs, carriage returns, and newlines are ignored.",
				PlanModifiers: []planmodifier.String{viewDefinitionPlanModifier{}},
			},
			"partitions": schema.ListAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Partition columns. Maps to PARTITIONED BY and conflicts with cluster_keys.",
			},
			"cluster_keys": schema.ListAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Liquid clustering columns, or [\"AUTO\"] / [\"NONE\"]. Conflicts with partitions.",
			},
			"properties": schema.MapAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Table properties stored as string key/value pairs.",
			},
			"data_format": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Storage format: DELTA or ICEBERG.",
			},
			"refresh_policy": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Refresh policy: AUTO, INCREMENTAL, INCREMENTAL STRICT, or FULL.",
			},
			"schedule": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Refresh schedule: EVERY number HOUR(S), DAY(S), or WEEK(S), or CRON cron_string [AT TIME ZONE timezone_id]. Conflicts with trigger_on_update.",
			},
			"trigger_on_update": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Refresh when an upstream source changes. Conflicts with schedule.",
			},
			"trigger_on_update_interval": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Minimum interval in INTERVAL syntax for TRIGGER ON UPDATE; requires trigger_on_update = true.",
			},
			"row_filter": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Contents of the WITH ROW FILTER clause, for example policy_name ON (column_name).",
			},
			"comment": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Materialized view comment.",
			},
		},
	}
}

func (r *materializedViewResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{materializedViewConfigValidator{}}
}

func (r *materializedViewResource) ModifyPlan(_ context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.Config.Raw.IsNull() {
		return
	}
	var config, state, plan map[string]tftypes.Value
	for _, entry := range []struct {
		value  tftypes.Value
		target *map[string]tftypes.Value
	}{
		{req.Config.Raw, &config}, {req.State.Raw, &state}, {req.Plan.Raw, &plan},
	} {
		if err := entry.value.As(entry.target); err != nil {
			resp.Diagnostics.AddError("Unable to compare materialized view plan", err.Error())
			return
		}
	}
	configured, previous := config["view_definition"], state["view_definition"]
	if !configured.IsKnown() || configured.IsNull() || !previous.IsKnown() || previous.IsNull() || configured.Equal(previous) {
		return
	}
	var configuredSQL, previousSQL string
	if err := configured.As(&configuredSQL); err != nil {
		resp.Diagnostics.AddError("Unable to compare materialized view definition", err.Error())
		return
	}
	if err := previous.As(&previousSQL); err != nil {
		resp.Diagnostics.AddError("Unable to compare materialized view definition", err.Error())
		return
	}
	if normalizeViewDefinition(configuredSQL) != normalizeViewDefinition(previousSQL) {
		return
	}
	if !plan["view_definition"].Equal(previous) {
		return
	}
	for name, value := range config {
		if name == "view_definition" {
			continue
		}
		if !value.IsKnown() || (!value.IsNull() && !value.Equal(state[name])) {
			return
		}
		if planned := plan[name]; planned.IsKnown() && !planned.Equal(state[name]) {
			return
		}
	}
	// The framework marks unconfigured computed fields unknown before attribute
	// modifiers run. Restore them only when the entire change is a SQL no-op.
	resp.Plan.Raw = req.State.Raw
}

func (r *materializedViewResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*clientData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *clientData, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *materializedViewResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan materializedViewModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	statement, err := buildMaterializedViewSQL(ctx, plan, false)
	if err != nil {
		resp.Diagnostics.AddError("Invalid materialized view configuration", err.Error())
		return
	}
	fullName := materializedViewFullName(plan)
	if _, err = r.client.execSQLWithResult(ctx, plan.WarehouseID.ValueString(), statement); err != nil {
		resp.Diagnostics.AddError("Failed to create materialized view "+fullName, err.Error())
		return
	}
	updated, err := r.currentMaterializedView(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read materialized view "+fullName, err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &updated)...)
}

func (r *materializedViewResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state materializedViewModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fullName := materializedViewFullName(state)
	info, err := r.client.w.Tables.Get(ctx, catalog.GetTableRequest{FullName: fullName})
	exists, err := checkMaterializedViewInfo(info, err)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read materialized view "+fullName, err.Error())
		return
	}
	if !exists {
		resp.State.RemoveResource(ctx)
		return
	}
	updated, err := r.readMaterializedView(ctx, state, info)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read materialized view "+fullName, err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &updated)...)
}

func (r *materializedViewResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan materializedViewModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	statement, err := buildMaterializedViewSQL(ctx, plan, true)
	if err != nil {
		resp.Diagnostics.AddError("Invalid materialized view configuration", err.Error())
		return
	}
	fullName := materializedViewFullName(plan)
	if _, err = r.client.execSQLWithResult(ctx, plan.WarehouseID.ValueString(), statement); err != nil {
		resp.Diagnostics.AddError("Failed to replace materialized view "+fullName, err.Error())
		return
	}
	updated, err := r.currentMaterializedView(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read materialized view "+fullName, err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &updated)...)
}

func (r *materializedViewResource) currentMaterializedView(ctx context.Context, model materializedViewModel) (materializedViewModel, error) {
	fullName := materializedViewFullName(model)
	info, err := r.client.w.Tables.Get(ctx, catalog.GetTableRequest{FullName: fullName})
	exists, err := checkMaterializedViewInfo(info, err)
	if err != nil {
		return model, err
	}
	if !exists {
		return model, fmt.Errorf("materialized view %s was not found after applying DDL", fullName)
	}
	return r.readMaterializedView(ctx, model, info)
}

func checkMaterializedViewInfo(info *catalog.TableInfo, err error) (bool, error) {
	if errors.Is(err, apierr.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info == nil {
		return false, fmt.Errorf("Unity Catalog returned no table information")
	}
	if info.TableType != catalog.TableTypeMaterializedView {
		return false, fmt.Errorf("object is not a materialized view: %s has table_type %s", info.FullName, info.TableType)
	}
	return true, nil
}

func (r *materializedViewResource) readMaterializedView(ctx context.Context, state materializedViewModel, info *catalog.TableInfo) (materializedViewModel, error) {
	statement, err := r.client.execSQLWithResult(ctx, state.WarehouseID.ValueString(), "SHOW CREATE TABLE "+quoteFullName(info.FullName))
	if err != nil {
		return state, err
	}
	if statement.Result == nil || len(statement.Result.DataArray) == 0 || len(statement.Result.DataArray[0]) == 0 {
		return state, fmt.Errorf("SHOW CREATE TABLE returned no DDL for %s", info.FullName)
	}
	parsed, err := parseMaterializedViewDDL(statement.Result.DataArray[0][0])
	if err != nil {
		return state, err
	}
	state.ID = types.StringValue(info.FullName)
	state.ViewDefinition = newViewDefinitionValue(parsed.ViewDefinition)
	state.Partitions, err = listValue(ctx, parsed.Partitions)
	if err != nil {
		return state, err
	}
	state.ClusterKeys, err = listValue(ctx, parsed.ClusterKeys)
	if err != nil {
		return state, err
	}
	state.Properties, err = mapValue(ctx, parsed.Properties)
	if err != nil {
		return state, err
	}
	state.DataFormat = types.StringValue(parsed.DataFormat)
	state.RefreshPolicy = types.StringValue(parsed.RefreshPolicy)
	state.Schedule = optionalString(parsed.Schedule)
	state.TriggerOnUpdate = types.BoolValue(parsed.TriggerOnUpdate)
	state.TriggerOnUpdateInterval = optionalString(parsed.TriggerOnUpdateInterval)
	state.RowFilter = optionalString(parsed.RowFilter)
	state.Comment = types.StringValue(parsed.Comment)
	return state, nil
}

func (r *materializedViewResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state materializedViewModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fullName := materializedViewFullName(state)
	if err := r.client.execSQL(ctx, state.WarehouseID.ValueString(), "DROP MATERIALIZED VIEW IF EXISTS "+quoteFullName(fullName)); err != nil {
		resp.Diagnostics.AddError("Failed to delete materialized view "+fullName, err.Error())
	}
}

func (r *materializedViewResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	warehouseID, fullName, ok := parseMaterializedViewImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid materialized view import ID", "Expected warehouse_id|catalog.schema.name, got: "+req.ID)
		return
	}
	parts, _ := splitFullName(fullName)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("warehouse_id"), warehouseID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("catalog_name"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("schema_name"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), fullName)...)
}

func materializedViewFullName(model materializedViewModel) string {
	return strings.Join([]string{model.CatalogName.ValueString(), model.SchemaName.ValueString(), model.Name.ValueString()}, ".")
}

func parseMaterializedViewImportID(id string) (string, string, bool) {
	parts := strings.SplitN(id, "|", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		return "", "", false
	}
	if _, ok := splitFullName(parts[1]); !ok {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func buildMaterializedViewSQL(ctx context.Context, model materializedViewModel, replace bool) (string, error) {
	if err := validateMaterializedViewModel(ctx, model); err != nil {
		return "", err
	}
	verb := "CREATE MATERIALIZED VIEW"
	if replace {
		verb = "CREATE OR REPLACE MATERIALIZED VIEW"
	}
	clauses := []string{verb + " " + quoteFullName(materializedViewFullName(model))}
	if !model.DataFormat.IsNull() && !model.DataFormat.IsUnknown() {
		clauses = append(clauses, "USING "+strings.ToUpper(model.DataFormat.ValueString()))
	}
	partitions, err := stringList(ctx, model.Partitions)
	if err != nil {
		return "", err
	}
	if len(partitions) > 0 {
		clauses = append(clauses, "PARTITIONED BY ("+quoteIdentifiers(partitions)+")")
	}
	clusterKeys, err := stringList(ctx, model.ClusterKeys)
	if err != nil {
		return "", err
	}
	if len(clusterKeys) == 1 && strings.EqualFold(clusterKeys[0], "AUTO") {
		clauses = append(clauses, "CLUSTER BY AUTO")
	} else if len(clusterKeys) > 0 && !(len(clusterKeys) == 1 && strings.EqualFold(clusterKeys[0], "NONE")) {
		clauses = append(clauses, "CLUSTER BY ("+quoteIdentifiers(clusterKeys)+")")
	}
	if !model.Comment.IsNull() && !model.Comment.IsUnknown() {
		clauses = append(clauses, "COMMENT "+quoteSQLString(model.Comment.ValueString()))
	}
	properties, err := stringMap(ctx, model.Properties)
	if err != nil {
		return "", err
	}
	if len(properties) > 0 {
		keys := make([]string, 0, len(properties))
		for key := range properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		entries := make([]string, 0, len(keys))
		for _, key := range keys {
			entries = append(entries, quoteSQLString(key)+" = "+quoteSQLString(properties[key]))
		}
		clauses = append(clauses, "TBLPROPERTIES ("+strings.Join(entries, ", ")+")")
	}
	if !model.RefreshPolicy.IsNull() && !model.RefreshPolicy.IsUnknown() {
		clauses = append(clauses, "REFRESH POLICY "+strings.ToUpper(strings.TrimSpace(model.RefreshPolicy.ValueString())))
	}
	if !model.Schedule.IsNull() && !model.Schedule.IsUnknown() {
		clauses = append(clauses, "SCHEDULE "+strings.TrimSpace(model.Schedule.ValueString()))
	}
	if !model.TriggerOnUpdate.IsNull() && !model.TriggerOnUpdate.IsUnknown() && model.TriggerOnUpdate.ValueBool() {
		trigger := "TRIGGER ON UPDATE"
		if !model.TriggerOnUpdateInterval.IsNull() && !model.TriggerOnUpdateInterval.IsUnknown() {
			trigger += " AT MOST EVERY " + strings.TrimSpace(model.TriggerOnUpdateInterval.ValueString())
		}
		clauses = append(clauses, trigger)
	}
	if !model.RowFilter.IsNull() && !model.RowFilter.IsUnknown() {
		clauses = append(clauses, "WITH ROW FILTER "+strings.TrimSpace(model.RowFilter.ValueString()))
	}
	clauses = append(clauses, "AS\n"+normalizeViewDefinition(model.ViewDefinition.ValueString()))
	return strings.Join(clauses, "\n"), nil
}

func validateMaterializedViewModel(ctx context.Context, model materializedViewModel) error {
	if !model.ViewDefinition.IsUnknown() && normalizeViewDefinition(model.ViewDefinition.ValueString()) == "" {
		return fmt.Errorf("view_definition must not be empty")
	}
	partitions, err := stringList(ctx, model.Partitions)
	if err != nil {
		return err
	}
	clusterKeys, err := stringList(ctx, model.ClusterKeys)
	if err != nil {
		return err
	}
	if len(partitions) > 0 && len(clusterKeys) > 0 {
		return fmt.Errorf("partitions and cluster_keys cannot both be set")
	}
	if len(partitions) == 0 && !model.Partitions.IsNull() && !model.Partitions.IsUnknown() {
		return fmt.Errorf("partitions must contain at least one column when specified")
	}
	if len(clusterKeys) == 0 && !model.ClusterKeys.IsNull() && !model.ClusterKeys.IsUnknown() {
		return fmt.Errorf("cluster_keys must contain at least one value when specified")
	}
	for _, value := range append(append([]string{}, partitions...), clusterKeys...) {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("partition and clustering columns must not be empty")
		}
	}
	if len(clusterKeys) > 0 {
		special := strings.EqualFold(clusterKeys[0], "AUTO") || strings.EqualFold(clusterKeys[0], "NONE")
		if special && (len(clusterKeys) != 1 || !strings.EqualFold(clusterKeys[0], strings.ToUpper(clusterKeys[0]))) {
			return fmt.Errorf("cluster_keys AUTO and NONE must be the only value and use uppercase spelling")
		}
		for _, key := range clusterKeys[1:] {
			if strings.EqualFold(key, "AUTO") || strings.EqualFold(key, "NONE") {
				return fmt.Errorf("cluster_keys AUTO and NONE must be the only value")
			}
		}
	}
	if !model.DataFormat.IsNull() && !model.DataFormat.IsUnknown() {
		format := strings.ToUpper(model.DataFormat.ValueString())
		if format != "DELTA" && format != "ICEBERG" {
			return fmt.Errorf("data_format must be DELTA or ICEBERG")
		}
	}
	if !model.RefreshPolicy.IsNull() && !model.RefreshPolicy.IsUnknown() {
		policy := strings.ToUpper(strings.Join(strings.Fields(model.RefreshPolicy.ValueString()), " "))
		if policy != "AUTO" && policy != "INCREMENTAL" && policy != "INCREMENTAL STRICT" && policy != "FULL" {
			return fmt.Errorf("refresh_policy must be AUTO, INCREMENTAL, INCREMENTAL STRICT, or FULL")
		}
	}
	scheduleSet := !model.Schedule.IsNull() && !model.Schedule.IsUnknown() && strings.TrimSpace(model.Schedule.ValueString()) != ""
	triggerSet := !model.TriggerOnUpdate.IsNull() && !model.TriggerOnUpdate.IsUnknown() && model.TriggerOnUpdate.ValueBool()
	if scheduleSet && triggerSet {
		return fmt.Errorf("schedule and trigger_on_update cannot both be enabled")
	}
	if scheduleSet && !validMaterializedViewSchedule(model.Schedule.ValueString()) {
		return fmt.Errorf("schedule must be EVERY number HOUR(S), DAY(S), or WEEK(S), or CRON cron_string [AT TIME ZONE timezone_id]")
	}
	intervalSet := !model.TriggerOnUpdateInterval.IsNull() && !model.TriggerOnUpdateInterval.IsUnknown() && strings.TrimSpace(model.TriggerOnUpdateInterval.ValueString()) != ""
	if intervalSet && !triggerSet {
		return fmt.Errorf("trigger_on_update_interval requires trigger_on_update = true")
	}
	if intervalSet && !validTriggerInterval(model.TriggerOnUpdateInterval.ValueString()) {
		return fmt.Errorf("trigger_on_update_interval must be a positive INTERVAL value, such as INTERVAL 5 MINUTES")
	}
	if !model.RowFilter.IsNull() && !model.RowFilter.IsUnknown() && strings.TrimSpace(model.RowFilter.ValueString()) == "" {
		return fmt.Errorf("row_filter must not be empty when specified")
	}
	if !model.Comment.IsNull() && !model.Comment.IsUnknown() && strings.ContainsRune(model.Comment.ValueString(), '\x00') {
		return fmt.Errorf("comment must not contain a NUL character")
	}
	return nil
}

type materializedViewConfigValidator struct{}

func (materializedViewConfigValidator) Description(context.Context) string {
	return "validates materialized view SQL options and mutually exclusive settings"
}
func (v materializedViewConfigValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (materializedViewConfigValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var model materializedViewModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateMaterializedViewModel(ctx, model); err != nil {
		resp.Diagnostics.AddError("Invalid materialized view configuration", err.Error())
	}
}

var (
	everyScheduleRe = regexp.MustCompile(`(?i)^EVERY\s+\d+\s+(HOURS?|DAYS?|WEEKS?)$`)
	cronScheduleRe  = regexp.MustCompile(`(?i)^CRON\s+'(?:[^']|'')+'(?:\s+AT\s+TIME\s+ZONE\s+(?:'(?:[^']|'')+'|[A-Za-z0-9_./+-]+))?$`)
	triggerEveryRe  = regexp.MustCompile(`(?i)^(?:INTERVAL\s+)?\d+\s+(?:SECONDS?|MINUTES?|HOURS?|DAYS?)$`)
)

func validMaterializedViewSchedule(schedule string) bool {
	schedule = strings.TrimSpace(schedule)
	return everyScheduleRe.MatchString(schedule) || cronScheduleRe.MatchString(schedule)
}

func validTriggerInterval(interval string) bool {
	return triggerEveryRe.MatchString(strings.TrimSpace(interval))
}

func stringList(ctx context.Context, value types.List) ([]string, error) {
	if value.IsNull() || value.IsUnknown() {
		return nil, nil
	}
	var result []string
	if diagnostics := value.ElementsAs(ctx, &result, false); diagnostics.HasError() {
		return nil, fmt.Errorf("decode string list: %s", diagnostics.Errors()[0].Detail())
	}
	return result, nil
}

func stringMap(ctx context.Context, value types.Map) (map[string]string, error) {
	if value.IsNull() || value.IsUnknown() {
		return nil, nil
	}
	result := map[string]string{}
	if diagnostics := value.ElementsAs(ctx, &result, false); diagnostics.HasError() {
		return nil, fmt.Errorf("decode string map: %s", diagnostics.Errors()[0].Detail())
	}
	return result, nil
}

func listValue(ctx context.Context, value []string) (types.List, error) {
	result, diagnostics := types.ListValueFrom(ctx, types.StringType, value)
	if diagnostics.HasError() {
		return result, fmt.Errorf("create string list value: %s", diagnostics.Errors()[0].Detail())
	}
	return result, nil
}

func mapValue(ctx context.Context, value map[string]string) (types.Map, error) {
	result, diagnostics := types.MapValueFrom(ctx, types.StringType, value)
	if diagnostics.HasError() {
		return result, fmt.Errorf("create string map value: %s", diagnostics.Errors()[0].Detail())
	}
	return result, nil
}

func optionalString(value string) types.String {
	if value == "" {
		return types.StringNull()
	}
	return types.StringValue(value)
}

func quoteIdentifiers(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "`" + strings.ReplaceAll(value, "`", "``") + "`"
	}
	return strings.Join(quoted, ", ")
}

func quoteSQLString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

type materializedViewDDL struct {
	ViewDefinition          string
	Partitions              []string
	ClusterKeys             []string
	Properties              map[string]string
	DataFormat              string
	RefreshPolicy           string
	Schedule                string
	TriggerOnUpdate         bool
	TriggerOnUpdateInterval string
	RowFilter               string
	Comment                 string
}

type ddlToken struct {
	text  string
	value string
	start int
	end   int
	depth int
	kind  byte
}

func parseMaterializedViewDDL(ddl string) (materializedViewDDL, error) {
	result := materializedViewDDL{
		Properties:    map[string]string{},
		DataFormat:    "DELTA",
		RefreshPolicy: "AUTO",
		ClusterKeys:   []string{"NONE"},
	}
	tokens, err := lexDDL(ddl)
	if err != nil {
		return result, err
	}
	materialized := false
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].depth == 0 && tokenWord(tokens[i], "MATERIALIZED") && tokenWord(tokens[i+1], "VIEW") {
			materialized = true
			break
		}
	}
	if !materialized {
		return result, fmt.Errorf("SHOW CREATE TABLE did not return MATERIALIZED VIEW DDL")
	}
	asIndex := -1
	for i, token := range tokens {
		if token.depth == 0 && tokenWord(token, "AS") {
			asIndex = i
			break
		}
	}
	if asIndex < 0 {
		return result, fmt.Errorf("materialized view DDL has no top-level AS query clause")
	}
	result.ViewDefinition = normalizeViewDefinition(strings.TrimSuffix(normalizeViewDefinition(ddl[tokens[asIndex].end:]), ";"))
	if result.ViewDefinition == "" {
		return result, fmt.Errorf("materialized view DDL has an empty query")
	}
	starts := make([]int, 0)
	for i := 0; i < asIndex; i++ {
		if tokens[i].depth == 0 && isMaterializedViewClauseStart(tokens, i) {
			starts = append(starts, i)
		}
	}
	for i, startIndex := range starts {
		start := tokens[startIndex].start
		end := tokens[asIndex].start
		if i+1 < len(starts) {
			end = tokens[starts[i+1]].start
		}
		clause := strings.TrimSpace(ddl[start:end])
		switch {
		case tokenWord(tokens[startIndex], "USING"):
			result.DataFormat = strings.ToUpper(strings.TrimSpace(clause[len("USING"):]))
		case tokenWord(tokens[startIndex], "PARTITIONED"):
			result.Partitions, err = parseIdentifierList(clause[len("PARTITIONED BY"):])
			if result.ClusterKeys != nil && len(result.ClusterKeys) == 1 && result.ClusterKeys[0] == "NONE" {
				result.ClusterKeys = nil
			}
		case tokenWord(tokens[startIndex], "CLUSTER"):
			cluster := strings.TrimSpace(clause[len("CLUSTER BY"):])
			if strings.EqualFold(cluster, "AUTO") {
				result.ClusterKeys = []string{"AUTO"}
			} else if strings.EqualFold(cluster, "NONE") {
				result.ClusterKeys = []string{"NONE"}
			} else {
				result.ClusterKeys, err = parseIdentifierList(cluster)
			}
		case tokenWord(tokens[startIndex], "COMMENT"):
			result.Comment, err = parseDDLString(strings.TrimSpace(clause[len("COMMENT"):]))
		case tokenWord(tokens[startIndex], "TBLPROPERTIES"):
			result.Properties, err = parseProperties(clause[len("TBLPROPERTIES"):])
		case tokenWord(tokens[startIndex], "REFRESH"):
			result.RefreshPolicy = strings.ToUpper(strings.Join(strings.Fields(clause[len("REFRESH POLICY"):]), " "))
		case tokenWord(tokens[startIndex], "SCHEDULE"):
			schedule := strings.TrimSpace(clause[len("SCHEDULE"):])
			if strings.HasPrefix(strings.ToUpper(schedule), "REFRESH ") {
				schedule = strings.TrimSpace(schedule[len("REFRESH"):])
			}
			result.Schedule = schedule
		case tokenWord(tokens[startIndex], "TRIGGER"):
			result.TriggerOnUpdate = true
			trigger := strings.TrimSpace(clause[len("TRIGGER ON UPDATE"):])
			if strings.HasPrefix(strings.ToUpper(trigger), "AT MOST EVERY ") {
				result.TriggerOnUpdateInterval = strings.TrimSpace(trigger[len("AT MOST EVERY"):])
			}
		case tokenWord(tokens[startIndex], "WITH"):
			result.RowFilter = strings.TrimSpace(clause[len("WITH ROW FILTER"):])
		}
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func isMaterializedViewClauseStart(tokens []ddlToken, index int) bool {
	first := tokens[index]
	if first.kind != 'w' {
		return false
	}
	if tokenWord(first, "USING") || tokenWord(first, "COMMENT") || tokenWord(first, "TBLPROPERTIES") || tokenWord(first, "SCHEDULE") {
		return true
	}
	return (tokenWord(first, "PARTITIONED") && index+1 < len(tokens) && tokenWord(tokens[index+1], "BY")) ||
		(tokenWord(first, "CLUSTER") && index+1 < len(tokens) && tokenWord(tokens[index+1], "BY")) ||
		(tokenWord(first, "REFRESH") && index+1 < len(tokens) && tokenWord(tokens[index+1], "POLICY")) ||
		(tokenWord(first, "TRIGGER") && index+2 < len(tokens) && tokenWord(tokens[index+1], "ON") && tokenWord(tokens[index+2], "UPDATE")) ||
		(tokenWord(first, "WITH") && index+2 < len(tokens) && tokenWord(tokens[index+1], "ROW") && tokenWord(tokens[index+2], "FILTER"))
}

func tokenWord(token ddlToken, value string) bool {
	return token.kind == 'w' && strings.EqualFold(token.value, value)
}

func parseIdentifierList(value string) ([]string, error) {
	tokens, err := lexDDL(value)
	if err != nil {
		return nil, err
	}
	open, close := -1, -1
	for i, token := range tokens {
		if token.text == "(" && open < 0 {
			open = i
		}
		if token.text == ")" && open >= 0 {
			close = i
		}
	}
	if open < 0 || close <= open {
		return nil, fmt.Errorf("expected parenthesized column list, got %s", value)
	}
	result := []string{}
	for i := open + 1; i < close; i++ {
		token := tokens[i]
		if token.depth != 1 || token.text == "," {
			continue
		}
		if token.kind != 'w' && token.kind != 'i' {
			return nil, fmt.Errorf("invalid column identifier in %s", value)
		}
		result = append(result, token.value)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("empty column list in %s", value)
	}
	return result, nil
}

func parseDDLString(value string) (string, error) {
	tokens, err := lexDDL(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	if len(tokens) != 1 || tokens[0].kind != 's' {
		return "", fmt.Errorf("expected a quoted SQL string, got %s", value)
	}
	return tokens[0].value, nil
}

func parseProperties(value string) (map[string]string, error) {
	tokens, err := lexDDL(value)
	if err != nil {
		return nil, err
	}
	properties := map[string]string{}
	for i := 0; i < len(tokens); {
		if tokens[i].depth != 1 || tokens[i].text == "," {
			i++
			continue
		}
		if (tokens[i].kind != 's' && tokens[i].kind != 'i' && tokens[i].kind != 'w') || i+2 >= len(tokens) || tokens[i+1].text != "=" {
			return nil, fmt.Errorf("invalid TBLPROPERTIES clause")
		}
		key := tokens[i].value
		valueToken := tokens[i+2]
		if valueToken.depth != 1 || (valueToken.kind != 's' && valueToken.kind != 'w') {
			return nil, fmt.Errorf("invalid value for table property %q", key)
		}
		properties[key] = valueToken.value
		i += 3
	}
	return properties, nil
}

func lexDDL(sql string) ([]ddlToken, error) {
	tokens := []ddlToken{}
	depth := 0
	for i := 0; i < len(sql); {
		if isSQLSpace(sql[i]) {
			i++
			continue
		}
		if i+1 < len(sql) && sql[i:i+2] == "--" {
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(sql) && sql[i:i+2] == "/*" {
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("unterminated SQL comment")
			}
			i += end + 4
			continue
		}
		start := i
		if sql[i] == '\'' || sql[i] == '"' || sql[i] == '`' {
			quote := sql[i]
			i++
			var decoded strings.Builder
			closed := false
			for i < len(sql) {
				if sql[i] == quote {
					if i+1 < len(sql) && sql[i+1] == quote {
						decoded.WriteByte(quote)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				decoded.WriteByte(sql[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted SQL value")
			}
			kind := byte('s')
			if quote == '`' {
				kind = 'i'
			}
			tokens = append(tokens, ddlToken{text: sql[start:i], value: decoded.String(), start: start, end: i, depth: depth, kind: kind})
			continue
		}
		if sql[i] == '(' {
			tokens = append(tokens, ddlToken{text: "(", value: "(", start: i, end: i + 1, depth: depth, kind: 'p'})
			depth++
			i++
			continue
		}
		if sql[i] == ')' {
			if depth == 0 {
				return nil, fmt.Errorf("unbalanced closing parenthesis")
			}
			depth--
			tokens = append(tokens, ddlToken{text: ")", value: ")", start: i, end: i + 1, depth: depth, kind: 'p'})
			i++
			continue
		}
		if strings.ContainsRune(".,=;", rune(sql[i])) {
			tokens = append(tokens, ddlToken{text: sql[i : i+1], value: sql[i : i+1], start: i, end: i + 1, depth: depth, kind: 'p'})
			i++
			continue
		}
		for i < len(sql) && !isSQLSpace(sql[i]) && !strings.ContainsRune("()'\"`.,=;", rune(sql[i])) {
			i++
		}
		if i == start {
			i++
		}
		tokens = append(tokens, ddlToken{text: sql[start:i], value: sql[start:i], start: start, end: i, depth: depth, kind: 'w'})
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced parentheses in SQL")
	}
	return tokens, nil
}

func isSQLSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}
