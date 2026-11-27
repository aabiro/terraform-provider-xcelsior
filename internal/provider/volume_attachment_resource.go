package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aabiro/terraform-provider-xcelsior/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*volumeAttachmentResource)(nil)
	_ resource.ResourceWithImportState = (*volumeAttachmentResource)(nil)
)

// NewVolumeAttachmentResource returns the xcelsior_volume_attachment resource.
func NewVolumeAttachmentResource() resource.Resource { return &volumeAttachmentResource{} }

type volumeAttachmentResource struct{ data *providerData }

type volumeAttachmentModel struct {
	ID         types.String `tfsdk:"id"`
	VolumeID   types.String `tfsdk:"volume_id"`
	InstanceID types.String `tfsdk:"instance_id"`
	MountPath  types.String `tfsdk:"mount_path"`
	Mode       types.String `tfsdk:"mode"`
}

func (r *volumeAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume_attachment"
}

func (r *volumeAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a volume to a running instance. To mount a volume from the start, " +
			"use `volume_ids` on `xcelsior_instance` instead. Every argument forces a new attachment.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, PlanModifiers: keepString(),
				MarkdownDescription: "`<volume_id>/<instance_id>`.",
			},
			"volume_id":   schema.StringAttribute{Required: true, PlanModifiers: replaceString()},
			"instance_id": schema.StringAttribute{Required: true, PlanModifiers: replaceString()},
			"mount_path": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:             stringdefault.StaticString("/workspace"),
				MarkdownDescription: "`/workspace` (the default), `/data`, or `/mnt/<name>`.",
				PlanModifiers:       replaceString(),
			},
			"mode": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:             stringdefault.StaticString("rw"),
				MarkdownDescription: "`rw` (the default) or `ro`.",
				PlanModifiers:       replaceString(),
			},
		},
	}
}

func (r *volumeAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configuredData(req.ProviderData, resp.Diagnostics.AddError)
}

func (r *volumeAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m volumeAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.data.client.AttachVolume(ctx, m.VolumeID.ValueString(), m.InstanceID.ValueString(),
		m.MountPath.ValueString(), m.Mode.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not attach the volume", err.Error())
		return
	}
	m.ID = types.StringValue(m.VolumeID.ValueString() + "/" + m.InstanceID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *volumeAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m volumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vol, err := r.data.client.GetVolume(ctx, m.VolumeID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the volume", err.Error())
		return
	}
	if vol.AttachedTo == nil || *vol.AttachedTo != m.InstanceID.ValueString() {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *volumeAttachmentResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unexpected update", "every attachment argument forces a new attachment; please report this")
}

func (r *volumeAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m volumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vol, err := r.data.client.GetVolume(ctx, m.VolumeID.ValueString())
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the volume", err.Error())
		return
	}
	if vol.AttachedTo == nil || *vol.AttachedTo != m.InstanceID.ValueString() {
		return // already detached, or attached elsewhere by someone else
	}
	if err := r.data.client.DetachVolume(ctx, m.VolumeID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not detach the volume", err.Error())
	}
}

func (r *volumeAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	volumeID, instanceID, ok := strings.Cut(req.ID, "/")
	if !ok || volumeID == "" || instanceID == "" {
		resp.Diagnostics.AddError("Invalid import id", fmt.Sprintf("expected <volume_id>/<instance_id>, got %q", req.ID))
		return
	}
	m := volumeAttachmentModel{
		ID:         types.StringValue(req.ID),
		VolumeID:   types.StringValue(volumeID),
		InstanceID: types.StringValue(instanceID),
		MountPath:  types.StringValue("/workspace"),
		Mode:       types.StringValue("rw"),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
