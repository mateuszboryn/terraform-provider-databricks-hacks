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
	_ resource.ResourceWithModifyPlan  = &metricViewResource{}
)

func NewMetricViewResource() resource.Resource { return &metricViewResource{} }

type metricViewResource struct {
	client *clientData
}

type metricViewModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Definition YAMLValue    `tfsdk:"definition"`
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
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Three-level name: catalog.schema.view_name. Changing it recreates the view.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{fullNameValidator{}},
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

	stmt := fmt.Sprintf("CREATE VIEW %s\nWITH METRICS\nLANGUAGE YAML\nAS $$\n%s\n$$",
		quoteFullName(plan.Name.ValueString()), plan.Definition.ValueString())
	if err := r.client.execSQL(ctx, stmt); err != nil {
		resp.Diagnostics.AddError("Failed to create metric view "+plan.Name.ValueString(), err.Error())
		return
	}

	plan.ID = plan.Name
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *metricViewResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state metricViewModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	info, err := r.client.w.Tables.Get(ctx, catalog.GetTableRequest{FullName: state.Name.ValueString()})
	if err != nil {
		if errors.Is(err, apierr.ErrNotFound) {
			// deleted outside Terraform -> plan will recreate it
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read metric view "+state.Name.ValueString(), err.Error())
		return
	}
	if string(info.TableType) != "METRIC_VIEW" {
		resp.Diagnostics.AddError("Object is not a metric view",
			fmt.Sprintf("%s has table_type %s", state.Name.ValueString(), info.TableType))
		return
	}

	// Store the workspace YAML. If it is only re-formatted, semantic equality keeps the
	// prior state value; if it was changed in the workspace, the next plan shows an update.
	state.Definition = NewYAMLValue(info.ViewDefinition)
	state.ID = state.Name
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *metricViewResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan metricViewModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ALTER VIEW keeps grants / tags (CREATE OR REPLACE would drop them).
	stmt := fmt.Sprintf("ALTER VIEW %s AS $$\n%s\n$$",
		quoteFullName(plan.Name.ValueString()), plan.Definition.ValueString())
	if err := r.client.execSQL(ctx, stmt); err != nil {
		resp.Diagnostics.AddError("Failed to update metric view "+plan.Name.ValueString(), err.Error())
		return
	}

	plan.ID = plan.Name
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *metricViewResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state metricViewModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.execSQL(ctx, "DROP VIEW IF EXISTS "+quoteFullName(state.Name.ValueString())); err != nil {
		resp.Diagnostics.AddError("Failed to delete metric view "+state.Name.ValueString(), err.Error())
	}
}

func (r *metricViewResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
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
	if err != nil || diff == "" {
		return
	}
	resp.Diagnostics.AddWarning(
		"Metric view definition change: "+plan.Name.ValueString(),
		"Normalized YAML diff (- workspace/state, + configuration):\n\n"+diff)
}

// ---------- helpers ----------

var namePartRe = regexp.MustCompile("^[^.`]+$")

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

type fullNameValidator struct{}

func (fullNameValidator) Description(_ context.Context) string {
	return "must be catalog.schema.name without backticks"
}
func (v fullNameValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (fullNameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, ok := splitFullName(req.ConfigValue.ValueString()); !ok {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid metric view name",
			"Expected three-level name catalog.schema.view_name (no backticks), got: "+req.ConfigValue.ValueString())
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
