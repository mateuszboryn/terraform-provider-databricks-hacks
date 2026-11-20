package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/databricks/databricks-sdk-go/apierr"
	"github.com/databricks/databricks-sdk-go/service/catalog"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &metricViewResource{}
	_ resource.ResourceWithConfigure   = &metricViewResource{}
	_ resource.ResourceWithImportState = &metricViewResource{}
)

func NewMetricViewResource() resource.Resource { return &metricViewResource{} }

type metricViewResource struct {
	client *clientData
}

type metricViewModel struct {
	ID          types.String `tfsdk:"id"`
	WarehouseID types.String `tfsdk:"warehouse_id"`
	CatalogName types.String `tfsdk:"catalog_name"`
	SchemaName  types.String `tfsdk:"schema_name"`
	Name        types.String `tfsdk:"name"`
	Definition  YAMLValue    `tfsdk:"definition"`
}

func (r *metricViewResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "databricks_metric_view"
}

func (r *metricViewResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Unity Catalog metric view defined by a YAML document.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Full name of the metric view (catalog.schema.name).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"warehouse_id": schema.StringAttribute{
				Required:    true,
				Description: "SQL warehouse ID used to create, read, update, and delete this metric view.",
				Validators:  []validator.String{namePartValidator{}},
			},
			"catalog_name": schema.StringAttribute{
				Required:      true,
				Description:   "Unity Catalog catalog containing the metric view. Changing it recreates the view.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{namePartValidator{}},
			},
			"schema_name": schema.StringAttribute{
				Required:      true,
				Description:   "Schema containing the metric view. Changing it recreates the view.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{namePartValidator{}},
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Metric view name. Changing it recreates the view.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{namePartValidator{}},
			},
			"definition": schema.StringAttribute{
				Required:    true,
				CustomType:  YAMLType{},
				Description: "Metric view YAML. Compared after normalization, so formatting-only changes produce no diff.",
				Validators:  []validator.String{yamlValidator{}},
			},
		},
	}
}

func (r *metricViewResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ---------- CRUD ----------

func (r *metricViewResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan metricViewModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fullName := metricViewFullName(plan)
	stmt := fmt.Sprintf("CREATE VIEW %s\nWITH METRICS\nLANGUAGE YAML\nAS $$\n%s\n$$",
		quoteFullName(fullName), plan.Definition.ValueString())
	if err := r.client.execSQL(ctx, plan.WarehouseID.ValueString(), stmt); err != nil {
		resp.Diagnostics.AddError("Failed to create metric view "+fullName, err.Error())
		return
	}

	plan.ID = types.StringValue(fullName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *metricViewResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state metricViewModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fullName := metricViewFullName(state)
	info, err := r.client.w.Tables.Get(ctx, catalog.GetTableRequest{FullName: fullName})
	if err != nil {
		if errors.Is(err, apierr.ErrNotFound) {
			// deleted outside Terraform -> plan will recreate it
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read metric view "+fullName, err.Error())
		return
	}
	if string(info.TableType) != "METRIC_VIEW" {
		resp.Diagnostics.AddError("Object is not a metric view",
			fmt.Sprintf("%s has table_type %s", fullName, info.TableType))
		return
	}

	// To read YAML definition, instead of using API, use SQL statement show create table to get YAML string. Then create YAMLValue.
	// this is because API returns incomplete YAML.
	statement, err := r.client.execSQLWithResult(ctx, state.WarehouseID.ValueString(), "SHOW CREATE TABLE "+quoteFullName(fullName))
	if err != nil {
		resp.Diagnostics.AddError("Failed to read metric view "+fullName, err.Error())
		return
	}
	if statement.Result == nil || len(statement.Result.DataArray) == 0 || len(statement.Result.DataArray[0]) == 0 {
		resp.Diagnostics.AddError("Failed to read metric view "+fullName, "SHOW CREATE TABLE returned no DDL")
		return
	}
	definition, err := parseMetricViewDDL(statement.Result.DataArray[0][0])
	if err != nil {
		resp.Diagnostics.AddError("Failed to read metric view "+fullName, err.Error())
		return
	}
	state.Definition = NewYAMLValue(definition)
	state.ID = types.StringValue(fullName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *metricViewResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan metricViewModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ALTER VIEW keeps grants / tags (CREATE OR REPLACE would drop them).
	fullName := metricViewFullName(plan)
	stmt := fmt.Sprintf("ALTER VIEW %s AS $$\n%s\n$$",
		quoteFullName(fullName), plan.Definition.ValueString())
	if err := r.client.execSQL(ctx, plan.WarehouseID.ValueString(), stmt); err != nil {
		resp.Diagnostics.AddError("Failed to update metric view "+fullName, err.Error())
		return
	}

	plan.ID = types.StringValue(fullName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *metricViewResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state metricViewModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fullName := metricViewFullName(state)
	if err := r.client.execSQL(ctx, state.WarehouseID.ValueString(), "DROP VIEW IF EXISTS "+quoteFullName(fullName)); err != nil {
		resp.Diagnostics.AddError("Failed to delete metric view "+fullName, err.Error())
	}
}

func (r *metricViewResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	warehouseID, fullName, ok := parseViewImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid metric view import ID", "Expected warehouse_id|catalog.schema.name, got: "+req.ID)
		return
	}
	parts, _ := splitFullName(fullName)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("warehouse_id"), warehouseID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("catalog_name"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("schema_name"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), fullName)...)
}

// ModifyPlan prints a readable diff of the normalized YAML when an update is planned,
// because Terraform's own diff shows the raw (differently formatted) strings.
func (r *metricViewResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return // create or destroy
	}
	var state, plan metricViewModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Definition.IsUnknown() || plan.Definition.IsNull() {
		return
	}
	diff, err := YAMLDiff(state.Definition.ValueString(), plan.Definition.ValueString())
	//if err != nil || diff == "" || len(strings.Split(diff, "\n")) > 100 {
	if err != nil || diff == "" {
		return
	}
	resp.Diagnostics.AddWarning(
		"Metric view definition change: "+metricViewFullName(plan),
		"Normalized YAML diff (- workspace/state, + configuration):\n\n"+diff)
}

// ---------- helpers ----------

var namePartRe = regexp.MustCompile("^[^.`]+$")
var metricViewDDLRe = regexp.MustCompile(`(?is)\bLANGUAGE\s+YAML\s+AS\s+\$\$(.*?)\$\$\s*;?\s*$`)

func parseMetricViewDDL(ddl string) (string, error) {
	match := metricViewDDLRe.FindStringSubmatch(ddl)
	if match == nil || strings.TrimSpace(match[1]) == "" {
		return "", fmt.Errorf("SHOW CREATE TABLE returned invalid metric view DDL")
	}
	if _, err := NormalizeYAML(match[1]); err != nil {
		return "", fmt.Errorf("SHOW CREATE TABLE returned invalid metric view YAML: %w", err)
	}
	return match[1], nil
}

func metricViewFullName(model metricViewModel) string {
	return strings.Join([]string{
		model.CatalogName.ValueString(),
		model.SchemaName.ValueString(),
		model.Name.ValueString(),
	}, ".")
}

func splitFullName(name string) ([]string, bool) {
	parts := strings.Split(name, ".")
	if len(parts) != 3 {
		return nil, false
	}
	for _, p := range parts {
		if !namePartRe.MatchString(p) {
			return nil, false
		}
	}
	return parts, true
}

// quoteFullName returns `catalog`.`schema`.`name`. Backticks are rejected by the validator.
func quoteFullName(name string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = "`" + strings.ReplaceAll(p, "`", "``") + "`"
	}
	return strings.Join(parts, ".")
}

type namePartValidator struct{}

func (namePartValidator) Description(_ context.Context) string {
	return "must be a non-empty identifier without dots or backticks"
}
func (v namePartValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (namePartValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	if strings.TrimSpace(value) == "" || !namePartRe.MatchString(value) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid metric view identifier",
			"Expected a non-empty identifier without dots or backticks, got: "+value)
	}
}

type yamlValidator struct{}

func (yamlValidator) Description(_ context.Context) string {
	return "must be valid YAML and must not contain $$"
}
func (v yamlValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (yamlValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	if strings.Contains(s, "$$") {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid definition",
			"definition must not contain '$$' (used as the SQL string delimiter)")
		return
	}
	if _, err := NormalizeYAML(s); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid definition", err.Error())
	}
}
