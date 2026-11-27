package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aabiro/terraform-provider-xcelsior/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*sshKeyResource)(nil)
	_ resource.ResourceWithImportState = (*sshKeyResource)(nil)
)

// NewSSHKeyResource returns the xcelsior_ssh_key resource.
func NewSSHKeyResource() resource.Resource { return &sshKeyResource{} }

type sshKeyResource struct{ data *providerData }

type sshKeyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	PublicKey   types.String `tfsdk:"public_key"`
	Fingerprint types.String `tfsdk:"fingerprint"`
}

func (r *sshKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

func (r *sshKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An SSH public key on the account. Registered keys are installed in the account's interactive instances, " +
			"including ones already running (within about 30 seconds). " +
			"A key can be registered once per account. Both arguments force a new key.\n\n" +
			"Registering and deleting a key needs the `ssh:write` scope. Checking it on refresh, and importing it, " +
			"needs `ssh:read`, which Quick Connect keys do not hold: with one, Terraform keeps what it recorded " +
			"at registration and warns that it cannot check the key.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keepString()},
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:             stringdefault.StaticString("default"),
				MarkdownDescription: "Label, at most 100 characters. Defaults to `default`.",
				PlanModifiers:       replaceString(),
			},
			"public_key": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "OpenSSH public key (`ssh-ed25519 AAAA... comment`).",
				PlanModifiers:       replaceString(),
			},
			"fingerprint": schema.StringAttribute{
				Computed: true, PlanModifiers: keepString(),
				MarkdownDescription: "`SHA256:` fingerprint.",
			},
		},
	}
}

func (r *sshKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configuredData(req.ProviderData, resp.Diagnostics.AddError)
}

func (r *sshKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m sshKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key, err := r.data.client.CreateSSHKey(ctx, m.Name.ValueString(), m.PublicKey.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not register the SSH key", err.Error())
		return
	}
	m.ID = types.StringValue(key.ID)
	m.Fingerprint = types.StringValue(key.Fingerprint)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func sameKey(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

func (r *sshKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	keys, err := r.data.client.ListSSHKeys(ctx)
	if client.IsMissingScope(err, "ssh:read") {
		// A Quick Connect key holds ssh:write and not ssh:read: it can
		// register and delete keys, not list them. Keep what Create recorded.
		if m.PublicKey.IsNull() {
			resp.Diagnostics.AddError("Cannot import this SSH key",
				"Importing reads the key from the account, which needs a token that holds ssh:read. "+
					"This token can register and delete SSH keys but not list them.")
			return
		}
		resp.Diagnostics.AddWarning("Terraform cannot check this SSH key",
			"This token holds ssh:write but not ssh:read, so it can register and delete SSH keys but not list them. "+
				"Terraform keeps what it recorded when it registered the key, and will not notice the key being deleted outside Terraform.")
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not list SSH keys", err.Error())
		return
	}
	var found *client.SSHKey
	for i := range keys {
		if keys[i].ID == m.ID.ValueString() {
			found = &keys[i]
			break
		}
	}
	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	m.Name = types.StringValue(found.Name)
	m.Fingerprint = types.StringValue(found.Fingerprint)
	// The API normalises whitespace; keep the configured spelling when the
	// key is the same, so a re-read does not plan a replacement.
	if m.PublicKey.IsNull() || !sameKey(m.PublicKey.ValueString(), found.PublicKey) {
		m.PublicKey = types.StringValue(found.PublicKey)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *sshKeyResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unexpected update", "every SSH key argument forces a new key; please report this")
}

func (r *sshKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.client.DeleteSSHKey(ctx, m.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not delete the SSH key", err.Error())
	}
}

func (r *sshKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
