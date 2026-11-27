package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aabiro/terraform-provider-xcelsior/internal/client"
)

var (
	_ resource.ResourceWithConfigure      = (*instanceResource)(nil)
	_ resource.ResourceWithImportState    = (*instanceResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*instanceResource)(nil)
	_ resource.ResourceWithValidateConfig = (*instanceResource)(nil)
)

// NewInstanceResource returns the xcelsior_instance resource.
func NewInstanceResource() resource.Resource { return &instanceResource{} }

type instanceResource struct{ data *providerData }

type instanceModel struct {
	ID                 types.String   `tfsdk:"id"`
	Name               types.String   `tfsdk:"name"`
	GPUModel           types.String   `tfsdk:"gpu_model"`
	NumGPUs            types.Int64    `tfsdk:"num_gpus"`
	PricingMode        types.String   `tfsdk:"pricing_mode"`
	Tier               types.String   `tfsdk:"tier"`
	Image              types.String   `tfsdk:"image"`
	Command            types.String   `tfsdk:"command"`
	VolumeIDs          types.List     `tfsdk:"volume_ids"`
	EncryptedWorkspace types.Bool     `tfsdk:"encrypted_workspace"`
	ExclusiveGPU       types.Bool     `tfsdk:"exclusive_gpu"`
	InitScript         types.String   `tfsdk:"init_script"`
	GitRepo            types.String   `tfsdk:"git_repo"`
	ExposedPorts       types.List     `tfsdk:"exposed_ports"`
	WaitForRunning     types.Bool     `tfsdk:"wait_for_running"`
	Status             types.String   `tfsdk:"status"`
	HostID             types.String   `tfsdk:"host_id"`
	HostIP             types.String   `tfsdk:"host_ip"`
	SSHPort            types.Int64    `tfsdk:"ssh_port"`
	HostKeyFingerprint types.String   `tfsdk:"host_key_fingerprint"`
	RatePerHourMicros  types.Int64    `tfsdk:"rate_per_hour_micros"`
	LaunchPlanID       types.String   `tfsdk:"launch_plan_id"`
	Timeouts           timeouts.Value `tfsdk:"timeouts"`
}

func (r *instanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func replaceString() []planmodifier.String {
	return []planmodifier.String{stringplanmodifier.RequiresReplace()}
}

func keepString() []planmodifier.String {
	return []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
}

func (r *instanceResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A GPU instance, billed by the second from the account's wallet.\n\n" +
			"Created through a launch plan: `terraform plan` quotes the launch without creating anything and warns " +
			"when it falls outside the token's spend envelope; `apply` creates the plan, has it approved (by the " +
			"envelope, or by a person within the provider's `approval_timeout`) and executes it. Every argument " +
			"except `wait_for_running` forces a new instance. SSH keys registered on the account " +
			"(`xcelsior_ssh_key`) are installed in the instance.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The instance (job) id.",
				PlanModifiers:       keepString(),
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Instance name, 1 to 128 characters.",
				PlanModifiers:       replaceString(),
			},
			"gpu_model": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "GPU model to rent, as `data.xcelsior_gpus` or `data.xcelsior_pricing` lists it. The launch is priced from it.",
				PlanModifiers:       replaceString(),
			},
			"num_gpus": schema.Int64Attribute{
				Optional: true, Computed: true,
				Default:             int64default.StaticInt64(1),
				MarkdownDescription: "Number of GPUs, 1 to 64. Defaults to 1.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"pricing_mode": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:             stringdefault.StaticString("on_demand"),
				MarkdownDescription: "`on_demand` (the default) or `spot`. A spot instance can be preempted; Terraform then plans a new one.",
				PlanModifiers:       replaceString(),
			},
			"tier": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Service tier. Omit for the standard tier.",
				PlanModifiers:       replaceString(),
			},
			"image": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Container image to run. Omit for the platform default.",
				PlanModifiers:       replaceString(),
			},
			"command": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Command to run in the container.",
				PlanModifiers:       replaceString(),
			},
			"volume_ids": schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Volumes to mount at launch (at most 16). To attach a volume to an instance that is already running, use `xcelsior_volume_attachment`.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
			},
			"encrypted_workspace": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"exclusive_gpu": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"init_script": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Script run in the container at boot (at most 4096 characters, 15 second cap).",
				PlanModifiers:       replaceString(),
			},
			"git_repo": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Public `https://` repository cloned at boot.",
				PlanModifiers:       replaceString(),
			},
			"exposed_ports": schema.ListAttribute{
				ElementType:   types.Int64Type,
				Optional:      true,
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
			},
			"wait_for_running": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Wait, within the create timeout, until the instance is running. Defaults to true. When false, apply ends once the launch is queued.",
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Lifecycle status as the API reports it (`queued`, `running`, ...).",
			},
			"host_id":              schema.StringAttribute{Computed: true, MarkdownDescription: "Host the instance was placed on."},
			"host_ip":              schema.StringAttribute{Computed: true, MarkdownDescription: "Address to connect to."},
			"ssh_port":             schema.Int64Attribute{Computed: true, MarkdownDescription: "Public SSH port."},
			"host_key_fingerprint": schema.StringAttribute{Computed: true, MarkdownDescription: "SSH host key fingerprint, to pin in `known_hosts`."},
			"rate_per_hour_micros": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Billed rate per hour, in millionths of the account currency.",
			},
			"launch_plan_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The launch plan this instance was created from.",
				PlanModifiers:       keepString(),
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *instanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configuredData(req.ProviderData, resp.Diagnostics.AddError)
}

func (r *instanceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m instanceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.PricingMode.IsNull() && !m.PricingMode.IsUnknown() {
		if v := m.PricingMode.ValueString(); v != "on_demand" && v != "spot" {
			resp.Diagnostics.AddAttributeError(path.Root("pricing_mode"), "Invalid pricing_mode",
				fmt.Sprintf("%q is not one of on_demand, spot.", v))
		}
	}
	if !m.NumGPUs.IsNull() && !m.NumGPUs.IsUnknown() {
		if n := m.NumGPUs.ValueInt64(); n < 1 || n > 64 {
			resp.Diagnostics.AddAttributeError(path.Root("num_gpus"), "Invalid num_gpus", "num_gpus must be between 1 and 64.")
		}
	}
}

func (r *instanceResource) spec(ctx context.Context, m instanceModel) (client.InstanceSpec, error) {
	volumes, diags := stringList(ctx, m.VolumeIDs)
	if diags.HasError() {
		return client.InstanceSpec{}, errors.New("volume_ids could not be read")
	}
	ports, diags := int64List(ctx, m.ExposedPorts)
	if diags.HasError() {
		return client.InstanceSpec{}, errors.New("exposed_ports could not be read")
	}
	return client.InstanceSpec{
		Name:               m.Name.ValueString(),
		GPUModel:           m.GPUModel.ValueString(),
		NumGPUs:            m.NumGPUs.ValueInt64(),
		PricingMode:        m.PricingMode.ValueString(),
		Tier:               m.Tier.ValueString(),
		Image:              m.Image.ValueString(),
		Command:            m.Command.ValueString(),
		VolumeIDs:          volumes,
		EncryptedWorkspace: m.EncryptedWorkspace.ValueBool(),
		ExclusiveGPU:       m.ExclusiveGPU.ValueBool(),
		InitScript:         m.InitScript.ValueString(),
		GitRepo:            m.GitRepo.ValueString(),
		ExposedPorts:       ports,
	}, nil
}

// ModifyPlan quotes a new instance at plan time. Nothing is created: the
// quote route persists no plan. A spec the API refuses fails the plan; a
// launch that will need a person, or that no host can take, warns.
func (r *instanceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.data == nil || req.Plan.Raw.IsNull() || !req.State.Raw.IsNull() {
		return // not configured, a destroy, or not a create
	}
	var m instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !m.argumentsKnown() {
		return // depends on values only apply will know; quoted at apply
	}
	spec, err := r.spec(ctx, m)
	if err != nil {
		return
	}
	q, err := r.data.client.QuoteLaunch(ctx, spec)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity {
			resp.Diagnostics.AddError("Xcelsior refused this launch", err.Error())
			return
		}
		resp.Diagnostics.AddWarning("Could not quote this launch", err.Error())
		return
	}
	if q.ApprovalMode == "human" {
		resp.Diagnostics.AddWarning("This launch will wait for a person's approval",
			approvalWarning(r.data.client.URL("/dashboard/launch-plans"), q.Policy.Reasons))
	}
	if q.Availability.Feasible != nil && !*q.Availability.Feasible {
		resp.Diagnostics.AddWarning("No host can take this launch right now",
			"The placement check found no eligible host"+suffix(q.Availability.ReasonText())+
				". If this instance replaces one destroyed in the same apply, the capacity that frees is not counted here.")
	}
}

// argumentsKnown reports whether every argument the launch spec is built
// from is known. Computed attributes (id, status, ...) are always unknown on
// a create, so the plan as a whole never is.
func (m instanceModel) argumentsKnown() bool {
	for _, v := range []interface{ IsUnknown() bool }{
		m.Name, m.GPUModel, m.NumGPUs, m.PricingMode, m.Tier, m.Image, m.Command, m.VolumeIDs,
		m.EncryptedWorkspace, m.ExclusiveGPU, m.InitScript, m.GitRepo, m.ExposedPorts,
	} {
		if v.IsUnknown() {
			return false
		}
	}
	return true
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}

func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m instanceModel
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

	spec, err := r.spec(ctx, m)
	if err != nil {
		resp.Diagnostics.AddError("Invalid instance arguments", err.Error())
		return
	}
	c := r.data.client
	var planID string
	jobID, err := c.RunPlan(ctx, client.PlanStep{
		Create: func(ctx context.Context) (*client.Preview, error) {
			p, err := c.CreateLaunchPlan(ctx, spec)
			if err == nil {
				planID = p.PlanID
			}
			return p, err
		},
		Execute: func(ctx context.Context, id string) (string, error) {
			planID = id
			out, err := c.ExecuteLaunchPlan(ctx, id)
			if err != nil {
				return "", err
			}
			return out.Job.JobID, nil
		},
	}, r.data.approvalOptions(ctx, nil))
	if err != nil {
		resp.Diagnostics.AddError("Could not launch the instance", err.Error())
		return
	}

	m.ID = types.StringValue(jobID)
	m.LaunchPlanID = types.StringValue(planID)
	// Save the id first, so an instance that fails to start is still in
	// state (tainted) and the next apply replaces it rather than leaking it.
	r.applyRead(&m, &client.Instance{JobID: jobID, Status: "queued"})
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)

	if m.WaitForRunning.ValueBool() {
		var last *client.Instance
		err := waitFor(ctx, r.data.pollInterval, "the instance to be running", func() (bool, error) {
			inst, err := c.GetInstance(ctx, jobID)
			if err != nil {
				return false, err
			}
			last = inst
			if inst.Status == "running" {
				return true, nil
			}
			if client.TerminalInstanceStatuses[inst.Status] {
				return false, fmt.Errorf("instance %s ended as %q before it was running", jobID, inst.Status)
			}
			return false, nil
		})
		if last != nil {
			r.applyRead(&m, last)
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		}
		if err != nil {
			resp.Diagnostics.AddError("The instance did not start", err.Error())
			return
		}
		return
	}
	if inst, err := c.GetInstance(ctx, jobID); err == nil {
		r.applyRead(&m, inst)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *instanceResource) applyRead(m *instanceModel, inst *client.Instance) {
	m.Status = types.StringValue(inst.Status)
	m.HostID = strOrNull(inst.HostID)
	m.HostIP = strOrNull(inst.HostIP)
	m.SSHPort = int64OrNull(inst.SSHPort)
	m.HostKeyFingerprint = strOrNull(inst.HostKeyFingerprint)
	m.RatePerHourMicros = microsOrNull(inst.RatePerHourCAD)
	// After an import, only the id is known; take the rest from the API.
	fillString(&m.Name, inst.Name)
	if inst.GPUModel != nil {
		fillString(&m.GPUModel, *inst.GPUModel)
	}
	fillInt64(&m.NumGPUs, inst.NumGPUs)
	if inst.PricingMode != nil {
		fillString(&m.PricingMode, *inst.PricingMode)
	}
	if m.LaunchPlanID.IsUnknown() {
		m.LaunchPlanID = types.StringNull()
	}
}

func (r *instanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	inst, err := r.data.client.GetInstance(ctx, m.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the instance", err.Error())
		return
	}
	if client.TerminalInstanceStatuses[inst.Status] {
		// Terminated outside Terraform, finished, or preempted: gone.
		resp.State.RemoveResource(ctx)
		return
	}
	r.applyRead(&m, inst)
	if m.WaitForRunning.IsNull() {
		m.WaitForRunning = types.BoolValue(true)
	}
	if m.EncryptedWorkspace.IsNull() {
		m.EncryptedWorkspace = types.BoolValue(false)
	}
	if m.ExclusiveGPU.IsNull() {
		m.ExclusiveGPU = types.BoolValue(false)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Update changes only provider-side behaviour (wait_for_running, timeouts);
// every API argument forces a new instance.
func (r *instanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.WaitForRunning = plan.WaitForRunning
	state.Timeouts = plan.Timeouts
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete terminates the instance. No approval: a destroy only lowers spend,
// and the Terraform plan output is its preview.
func (r *instanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	deleteTimeout, diags := m.Timeouts.Delete(ctx, 10*time.Minute)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()

	c := r.data.client
	id := m.ID.ValueString()
	inst, err := c.GetInstance(ctx, id)
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the instance before terminating it", err.Error())
		return
	}
	if client.TerminalInstanceStatuses[inst.Status] {
		return
	}
	if err := c.TerminateInstance(ctx, id); err != nil && !client.IsNotFound(err) {
		// A launch that has not started yet is cancelled rather than terminated.
		if cancelErr := c.CancelInstance(ctx, id); cancelErr != nil && !client.IsNotFound(cancelErr) {
			resp.Diagnostics.AddError("Could not terminate the instance",
				fmt.Sprintf("terminate: %s; cancel: %s", err, cancelErr))
			return
		}
	}
	err = waitFor(ctx, r.data.pollInterval, "the instance to stop", func() (bool, error) {
		inst, err := c.GetInstance(ctx, id)
		if client.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return client.TerminalInstanceStatuses[inst.Status], nil
	})
	if err != nil {
		resp.Diagnostics.AddError("The instance did not stop", err.Error())
	}
}

func (r *instanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
