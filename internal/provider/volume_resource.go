package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aabiro/terraform-provider-xcelsior/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*volumeResource)(nil)
	_ resource.ResourceWithImportState = (*volumeResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*volumeResource)(nil)
)

// NewVolumeResource returns the xcelsior_volume resource.
func NewVolumeResource() resource.Resource { return &volumeResource{} }

type volumeResource struct{ data *providerData }

type volumeModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	SizeGB     types.Int64  `tfsdk:"size_gb"`
	Region     types.String `tfsdk:"region"`
	Encrypted  types.Bool   `tfsdk:"encrypted"`
	Status     types.String `tfsdk:"status"`
	AttachedTo types.String `tfsdk:"attached_to"`
}

func (r *volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (r *volumeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A persistent volume, billed per GB-month from the account's wallet.\n\n" +
			"`terraform plan` runs every check a create runs (name, quota, size) without creating anything. " +
			"A rename is made in place; changing `size_gb`, `region` or `encrypted` replaces the volume and " +
			"**destroys its data**, so protect volumes that hold data with `lifecycle { prevent_destroy = true }`. " +
			"The API refuses to delete a volume that is attached.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, PlanModifiers: keepString(),
				MarkdownDescription: "The volume id.",
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Volume name, unique in the account, 1 to 128 characters. Renamed in place.",
			},
			"size_gb": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "Size in GB, 1 to 2000. There is no resize: a change replaces the volume.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"region": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Region to create the volume in. Omit to let the platform choose.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown(),
				},
			},
			"encrypted": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Encrypt the volume at rest. Defaults to true.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`available`, `attached`, `provisioning` or `error`.",
			},
			"attached_to": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Instance the volume is attached to, if any.",
			},
		},
	}
}

func (r *volumeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configuredData(req.ProviderData, resp.Diagnostics.AddError)
}

func (r *volumeResource) body(m volumeModel) client.VolumeCreate {
	region := ""
	if !m.Region.IsUnknown() {
		region = m.Region.ValueString()
	}
	return client.VolumeCreate{
		Name:      m.Name.ValueString(),
		SizeGB:    m.SizeGB.ValueInt64(),
		Region:    region,
		Encrypted: m.Encrypted.ValueBool(),
	}
}

// ModifyPlan previews a new volume: the API runs every create check and
// creates nothing. A refusal is a warning, not an error: Terraform plans the
// new half of a replacement with no prior state, so the preview cannot tell
// a fresh create from a replacement whose old volume still holds the name
// and the quota until apply destroys it.
func (r *volumeResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.data == nil || req.Plan.Raw.IsNull() || !req.State.Raw.IsNull() {
		return
	}
	var m volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Name.IsUnknown() || m.SizeGB.IsUnknown() || m.Encrypted.IsUnknown() {
		return
	}
	if _, err := r.data.client.PreviewVolume(ctx, r.body(m)); err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusBadRequest || apiErr.Status == http.StatusUnprocessableEntity) {
			resp.Diagnostics.AddWarning("Xcelsior would refuse this volume as things stand",
				apiErr.Detail+". apply will fail unless this volume replaces one that the same apply destroys first.")
			return
		}
		resp.Diagnostics.AddWarning("Could not preview this volume", err.Error())
	}
}

func (r *volumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vol, err := r.data.client.CreateVolume(ctx, r.body(m))
	if err != nil {
		resp.Diagnostics.AddError("Could not create the volume", err.Error())
		return
	}
	r.applyRead(&m, vol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if vol.Status == "error" {
		resp.Diagnostics.AddError("The volume failed to provision",
			"The API created volume "+vol.VolumeID+" but could not provision its storage. It is in state and will be replaced on the next apply.")
	}
}

func (r *volumeResource) applyRead(m *volumeModel, v *client.Volume) {
	m.ID = types.StringValue(v.VolumeID)
	m.Name = types.StringValue(v.Name)
	m.SizeGB = types.Int64Value(v.SizeGB)
	if v.Region != "" || m.Region.IsUnknown() {
		m.Region = types.StringValue(v.Region)
	}
	m.Encrypted = types.BoolValue(v.Encrypted)
	m.Status = types.StringValue(v.Status)
	m.AttachedTo = strOrNull(v.AttachedTo)
}

func (r *volumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vol, err := r.data.client.GetVolume(ctx, m.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the volume", err.Error())
		return
	}
	r.applyRead(&m, vol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *volumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if plan.Name.ValueString() != state.Name.ValueString() {
		if err := r.data.client.RenameVolume(ctx, id, plan.Name.ValueString()); err != nil {
			resp.Diagnostics.AddError("Could not rename the volume", err.Error())
			return
		}
	}
	vol, err := r.data.client.GetVolume(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the volume after renaming it", err.Error())
		return
	}
	r.applyRead(&plan, vol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := m.ID.ValueString()
	// An instance being destroyed in the same apply releases its volumes as
	// it stops; give that a moment before reporting "attached".
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err := r.data.client.DeleteVolume(ctx, id)
		if err == nil || client.IsNotFound(err) {
			return
		}
		var apiErr *client.APIError
		attached := errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict &&
			strings.Contains(strings.ToLower(apiErr.Detail), "attach")
		if !attached || time.Now().After(deadline) {
			msg := err.Error()
			if attached {
				msg += ". Detach it first (remove its xcelsior_volume_attachment, or destroy the instance using it)."
			}
			resp.Diagnostics.AddError("Could not delete the volume", msg)
			return
		}
		select {
		case <-ctx.Done():
			resp.Diagnostics.AddError("Could not delete the volume", ctx.Err().Error())
			return
		case <-time.After(r.data.pollInterval):
		}
	}
}

func (r *volumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
