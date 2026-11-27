package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aabiro/terraform-provider-xcelsior/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*endpointResource)(nil)
	_ resource.ResourceWithImportState = (*endpointResource)(nil)
)

// NewServerlessEndpointResource returns the xcelsior_serverless_endpoint resource.
func NewServerlessEndpointResource() resource.Resource { return &endpointResource{} }

type endpointResource struct{ data *providerData }

type endpointModel struct {
	ID             types.String   `tfsdk:"id"`
	Name           types.String   `tfsdk:"name"`
	Mode           types.String   `tfsdk:"mode"`
	ModelRef       types.String   `tfsdk:"model_ref"`
	DockerImage    types.String   `tfsdk:"docker_image"`
	GPUTier        types.String   `tfsdk:"gpu_tier"`
	GPUCount       types.Int64    `tfsdk:"gpu_count"`
	Region         types.String   `tfsdk:"region"`
	MinWorkers     types.Int64    `tfsdk:"min_workers"`
	MaxWorkers     types.Int64    `tfsdk:"max_workers"`
	MaxConcurrency types.Int64    `tfsdk:"max_concurrency"`
	IdleTimeoutSec types.Int64    `tfsdk:"idle_timeout_sec"`
	Env            types.Map      `tfsdk:"env"`
	Status         types.String   `tfsdk:"status"`
	LaunchPlanID   types.String   `tfsdk:"launch_plan_id"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func (r *endpointResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_serverless_endpoint"
}

func replaceOrKeepString() []planmodifier.String {
	return []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()}
}

func replaceOrKeepInt64() []planmodifier.Int64 {
	return []planmodifier.Int64{int64planmodifier.RequiresReplaceIfConfigured(), int64planmodifier.UseStateForUnknown()}
}

func (r *endpointResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A serverless inference endpoint, billed per worker-second.\n\n" +
			"Created through an endpoint plan approved like an instance launch plan: by the token's spend envelope, " +
			"or by a person within the provider's `approval_timeout`. The plan approves one hour at `max_workers`. " +
			"Only `name` is updated in place. Worker counts and every other argument replace the endpoint, " +
			"because raising them in place would add spend that no plan approved.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: keepString()},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Endpoint name. Renamed in place."},
			"mode": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: replaceOrKeepString(),
				MarkdownDescription: "`preset` (a managed engine serving `model_ref`, the API default) or a custom-container mode with `docker_image`.",
			},
			"model_ref":    schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: replaceOrKeepString(), MarkdownDescription: "Model to serve, such as a Hugging Face repo id."},
			"docker_image": schema.StringAttribute{Optional: true, PlanModifiers: replaceString(), MarkdownDescription: "Custom container image."},
			"gpu_tier":     schema.StringAttribute{Required: true, PlanModifiers: replaceString(), MarkdownDescription: "GPU model each worker runs on."},
			"gpu_count":    schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: replaceOrKeepInt64(), MarkdownDescription: "GPUs per worker, 1 to 8."},
			"region":       schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: replaceOrKeepString()},
			"min_workers":  schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: replaceOrKeepInt64(), MarkdownDescription: "Workers kept warm, 0 to 32. Warm workers bill while idle; set 0 to pay nothing while idle."},
			"max_workers":  schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: replaceOrKeepInt64(), MarkdownDescription: "Worker ceiling, 1 to 32. The plan approves one hour at this many workers."},
			"max_concurrency": schema.Int64Attribute{
				Optional: true, Computed: true, PlanModifiers: replaceOrKeepInt64(),
				MarkdownDescription: "Requests one worker serves at once, 1 to 256.",
			},
			"idle_timeout_sec": schema.Int64Attribute{
				Optional: true, Computed: true, PlanModifiers: replaceOrKeepInt64(),
				MarkdownDescription: "Seconds a worker idles before it scales down, 60 to 86400.",
			},
			"env": schema.MapAttribute{
				ElementType: types.StringType, Optional: true, Sensitive: true,
				MarkdownDescription: "Environment for the workers. Stored encrypted; the API never returns it, so drift in it is not detected.",
				PlanModifiers:       []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"status":         schema.StringAttribute{Computed: true},
			"launch_plan_id": schema.StringAttribute{Computed: true, PlanModifiers: keepString()},
			"timeouts":       timeouts.Attributes(ctx, timeouts.Opts{Create: true}),
		},
	}
}

func (r *endpointResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configuredData(req.ProviderData, resp.Diagnostics.AddError)
}

func optInt64(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	n := v.ValueInt64()
	return &n
}

func knownString(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func knownInt64(v types.Int64) int64 {
	if v.IsNull() || v.IsUnknown() {
		return 0
	}
	return v.ValueInt64()
}

func (r *endpointResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m endpointModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	createTimeout, diags := m.Timeouts.Create(ctx, 30*time.Minute)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	env := map[string]string{}
	if !m.Env.IsNull() && !m.Env.IsUnknown() {
		resp.Diagnostics.Append(m.Env.ElementsAs(ctx, &env, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	spec := client.EndpointSpec{
		Name:           m.Name.ValueString(),
		Mode:           knownString(m.Mode),
		ModelRef:       knownString(m.ModelRef),
		DockerImage:    knownString(m.DockerImage),
		GPUTier:        m.GPUTier.ValueString(),
		GPUCount:       knownInt64(m.GPUCount),
		Region:         knownString(m.Region),
		MinWorkers:     optInt64(m.MinWorkers),
		MaxWorkers:     knownInt64(m.MaxWorkers),
		MaxConcurrency: knownInt64(m.MaxConcurrency),
		IdleTimeoutSec: knownInt64(m.IdleTimeoutSec),
		Env:            env,
	}
	c := r.data.client
	var planID string
	var created *client.Endpoint
	endpointID, err := c.RunPlan(ctx, client.PlanStep{
		Create: func(ctx context.Context) (*client.Preview, error) {
			p, err := c.CreateEndpointPlan(ctx, spec)
			if err == nil {
				planID = p.PlanID
			}
			return p, err
		},
		Execute: func(ctx context.Context, id string) (string, error) {
			planID = id
			ep, err := c.ExecuteEndpointPlan(ctx, id)
			if err != nil {
				return "", err
			}
			created = ep
			return ep.EndpointID, nil
		},
	}, r.data.approvalOptions(ctx, nil))
	if err != nil {
		resp.Diagnostics.AddError("Could not create the serverless endpoint", err.Error())
		return
	}
	m.ID = types.StringValue(endpointID)
	m.LaunchPlanID = types.StringValue(planID)
	if ep, err := c.GetEndpoint(ctx, endpointID); err == nil {
		created = ep
	}
	r.applyRead(&m, created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *endpointResource) applyRead(m *endpointModel, ep *client.Endpoint) {
	if ep == nil {
		return
	}
	m.Name = types.StringValue(ep.Name)
	m.Status = types.StringValue(ep.Status)
	if ep.Mode != "" {
		m.Mode = types.StringValue(ep.Mode)
	}
	if ep.ModelRef != "" || m.ModelRef.IsUnknown() {
		m.ModelRef = types.StringValue(ep.ModelRef)
	}
	if ep.GPUTier != "" {
		m.GPUTier = types.StringValue(ep.GPUTier)
	}
	m.GPUCount = types.Int64Value(ep.GPUCount)
	if ep.Region != "" || m.Region.IsUnknown() {
		m.Region = types.StringValue(ep.Region)
	}
	m.MinWorkers = types.Int64Value(ep.MinWorkers)
	m.MaxWorkers = types.Int64Value(ep.MaxWorkers)
	m.MaxConcurrency = types.Int64Value(ep.MaxConcurrency)
	m.IdleTimeoutSec = types.Int64Value(ep.IdleTimeoutSec)
	if m.LaunchPlanID.IsUnknown() {
		m.LaunchPlanID = types.StringNull()
	}
}

func (r *endpointResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m endpointModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ep, err := r.data.client.GetEndpoint(ctx, m.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the serverless endpoint", err.Error())
		return
	}
	if ep.Status == "deleted" {
		resp.State.RemoveResource(ctx)
		return
	}
	r.applyRead(&m, ep)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *endpointResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state endpointModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if plan.Name.ValueString() != state.Name.ValueString() {
		if err := r.data.client.RenameEndpoint(ctx, id, plan.Name.ValueString()); err != nil {
			resp.Diagnostics.AddError("Could not rename the serverless endpoint", err.Error())
			return
		}
	}
	ep, err := r.data.client.GetEndpoint(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the serverless endpoint after renaming it", err.Error())
		return
	}
	state.Timeouts = plan.Timeouts
	r.applyRead(&state, ep)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *endpointResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m endpointModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.DeleteEndpoint(ctx, m.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not delete the serverless endpoint", err.Error())
	}
}

func (r *endpointResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
