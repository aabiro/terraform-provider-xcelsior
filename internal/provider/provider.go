// Package provider implements the Xcelsior Terraform provider.
package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/aabiro/terraform-provider-xcelsior/internal/client"
)

// Environment variables the provider block falls back to.
const (
	EnvAPIToken        = "XCELSIOR_API_TOKEN"
	EnvEndpoint        = "XCELSIOR_ENDPOINT"
	EnvApprovalTimeout = "XCELSIOR_APPROVAL_TIMEOUT"
)

var _ provider.Provider = (*xcelsiorProvider)(nil)

type xcelsiorProvider struct {
	version string
}

// New returns a provider factory for the given build version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &xcelsiorProvider{version: version}
	}
}

type providerModel struct {
	APIToken        types.String `tfsdk:"api_token"`
	Endpoint        types.String `tfsdk:"endpoint"`
	ApprovalTimeout types.String `tfsdk:"approval_timeout"`
}

// providerData is handed to every resource and data source.
type providerData struct {
	client          *client.Client
	approvalTimeout time.Duration
	pollInterval    time.Duration
}

func (p *xcelsiorProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "xcelsior"
	resp.Version = p.version
}

func (p *xcelsiorProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Declare Xcelsior GPU instances, volumes, SSH keys and serverless endpoints.\n\n" +
			"Every launch goes through an Xcelsior launch plan. A launch inside the token's **spend envelope** " +
			"approves itself; one outside it waits for a person to approve it in the dashboard, for up to " +
			"`approval_timeout`. See the provider guide for how to set an envelope.",
		Attributes: map[string]schema.Attribute{
			"api_token": schema.StringAttribute{
				MarkdownDescription: "API token. An agent key from Settings is recommended. Falls back to the `" +
					EnvAPIToken + "` environment variable.",
				Optional:  true,
				Sensitive: true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "API base URL. Defaults to `" + client.DefaultEndpoint + "`, or the `" +
					EnvEndpoint + "` environment variable. Plain `http` is accepted only for localhost.",
				Optional: true,
			},
			"approval_timeout": schema.StringAttribute{
				MarkdownDescription: "How long `apply` waits for a person to approve a launch outside the token's " +
					"spend envelope, as a Go duration (`15m`). The default, `0s`, fails at once and revokes the plan, " +
					"which is what an unattended pipeline wants. Falls back to `" + EnvApprovalTimeout + "`.",
				Optional: true,
			},
		},
	}
}

func (p *xcelsiorProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for name, v := range map[string]types.String{
		"api_token": cfg.APIToken, "endpoint": cfg.Endpoint, "approval_timeout": cfg.ApprovalTimeout,
	} {
		if v.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Unknown provider configuration",
				fmt.Sprintf("%s must be known when the provider is configured; set it from a variable, not another resource's output.", name))
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	token := firstNonEmpty(cfg.APIToken.ValueString(), os.Getenv(EnvAPIToken))
	endpoint := firstNonEmpty(cfg.Endpoint.ValueString(), os.Getenv(EnvEndpoint), client.DefaultEndpoint)
	timeoutText := firstNonEmpty(cfg.ApprovalTimeout.ValueString(), os.Getenv(EnvApprovalTimeout), "0s")

	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "Missing API token",
			"Set api_token in the provider block or the "+EnvAPIToken+" environment variable. "+
				"Create an agent key in the Xcelsior dashboard under Settings.")
		return
	}
	approvalTimeout, err := time.ParseDuration(timeoutText)
	if err != nil || approvalTimeout < 0 {
		resp.Diagnostics.AddAttributeError(path.Root("approval_timeout"), "Invalid approval_timeout",
			fmt.Sprintf("%q is not a non-negative duration such as 0s, 10m or 1h.", timeoutText))
		return
	}

	userAgent := fmt.Sprintf("terraform-provider-xcelsior/%s (+https://www.terraform.io) Terraform/%s", p.version, req.TerraformVersion)
	c, err := client.New(endpoint, token, userAgent, nil)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Invalid provider configuration", err.Error())
		return
	}

	// One cheap read that every credential type may make: it checks the
	// token and tells the log whether launches will approve themselves.
	env, err := c.GetSpendEnvelope(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Xcelsior rejected the provider's token", err.Error())
		return
	}
	if env.Envelope == nil {
		tflog.Info(ctx, "this token has no spend envelope; every launch will wait for a person's approval")
	} else {
		tflog.Info(ctx, "this token has a spend envelope", map[string]any{"client_id": env.ClientID})
	}

	data := &providerData{client: c, approvalTimeout: approvalTimeout, pollInterval: 5 * time.Second}
	if v := os.Getenv("XCELSIOR_TF_POLL_INTERVAL"); v != "" {
		// Test hook: unit tests poll a fake API in milliseconds.
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			data.pollInterval = d
		}
	}
	resp.DataSourceData = data
	resp.ResourceData = data
}

func (p *xcelsiorProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewInstanceResource,
		NewVolumeResource,
		NewVolumeAttachmentResource,
		NewSSHKeyResource,
		NewServerlessEndpointResource,
	}
}

func (p *xcelsiorProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewGPUsDataSource,
		NewPricingDataSource,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// configuredData extracts providerData in a resource or data source
// Configure; nil means "not configured yet" (validate, early plan).
func configuredData(raw any, addError func(summary, detail string)) *providerData {
	if raw == nil {
		return nil
	}
	data, ok := raw.(*providerData)
	if !ok {
		addError("Unexpected provider data", fmt.Sprintf("expected *providerData, got %T; please report this", raw))
		return nil
	}
	return data
}

func (d *providerData) approvalOptions(ctx context.Context, notify func(link string, reasons []string)) client.ApprovalOptions {
	return client.ApprovalOptions{
		Timeout:      d.approvalTimeout,
		PollInterval: d.pollInterval,
		Notify: func(link string, reasons []string) {
			tflog.Warn(ctx, "launch plan needs a person's approval", map[string]any{"approve_at": link, "reasons": reasons})
			if notify != nil {
				notify(link, reasons)
			}
		},
	}
}
