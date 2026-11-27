package provider

import (
	"context"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aabiro/terraform-provider-xcelsior/internal/client"
)

var (
	_ datasource.DataSourceWithConfigure = (*gpusDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*pricingDataSource)(nil)
)

// ── xcelsior_gpus ───────────────────────────────────────────────────

// NewGPUsDataSource returns the xcelsior_gpus data source.
func NewGPUsDataSource() datasource.DataSource { return &gpusDataSource{} }

type gpusDataSource struct{ data *providerData }

type gpuModel struct {
	GPUModel           types.String  `tfsdk:"gpu_model"`
	VRAMGB             types.Float64 `tfsdk:"vram_gb"`
	Region             types.String  `tfsdk:"region"`
	Province           types.String  `tfsdk:"province"`
	CountAvailable     types.Int64   `tfsdk:"count_available"`
	PricePerHourMicros types.Int64   `tfsdk:"price_per_hour_micros"`
	ServiceTiers       []string      `tfsdk:"service_tiers"`
}

type gpusModel struct {
	Region     types.String `tfsdk:"region"`
	GPUModel   types.String `tfsdk:"gpu_model"`
	ObservedAt types.String `tfsdk:"observed_at"`
	GPUs       []gpuModel   `tfsdk:"gpus"`
}

func (d *gpusDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gpus"
}

func (d *gpusDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "GPUs free to rent now, from the same placement snapshot launches use. " +
			"Prices are per hour in millionths of the account currency.",
		Attributes: map[string]schema.Attribute{
			"region":      schema.StringAttribute{Optional: true, MarkdownDescription: "Only GPUs in this region."},
			"gpu_model":   schema.StringAttribute{Optional: true, MarkdownDescription: "Only this GPU model (case-insensitive)."},
			"observed_at": schema.StringAttribute{Computed: true, MarkdownDescription: "When the snapshot was taken."},
			"gpus": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"gpu_model":             schema.StringAttribute{Computed: true},
					"vram_gb":               schema.Float64Attribute{Computed: true},
					"region":                schema.StringAttribute{Computed: true},
					"province":              schema.StringAttribute{Computed: true},
					"count_available":       schema.Int64Attribute{Computed: true},
					"price_per_hour_micros": schema.Int64Attribute{Computed: true},
					"service_tiers":         schema.ListAttribute{Computed: true, ElementType: types.StringType},
				}},
			},
		},
	}
}

func (d *gpusDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configuredData(req.ProviderData, resp.Diagnostics.AddError)
}

func (d *gpusDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m gpusModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gpus, observed, err := d.data.client.GPUAvailability(ctx, m.Region.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not list available GPUs", err.Error())
		return
	}
	want := strings.ToLower(strings.TrimSpace(m.GPUModel.ValueString()))
	m.GPUs = []gpuModel{}
	for _, g := range gpus {
		if want != "" && strings.ToLower(g.GPUModel) != want {
			continue
		}
		tiers := g.ServiceTiers
		if tiers == nil {
			tiers = []string{}
		}
		m.GPUs = append(m.GPUs, gpuModel{
			GPUModel:           types.StringValue(g.GPUModel),
			VRAMGB:             types.Float64Value(g.VRAMGB),
			Region:             types.StringValue(g.Region),
			Province:           types.StringValue(g.Province),
			CountAvailable:     types.Int64Value(g.CountAvailable),
			PricePerHourMicros: types.Int64Value(client.CADMicros(g.PricePerHourCAD)),
			ServiceTiers:       tiers,
		})
	}
	m.ObservedAt = types.StringValue(observed)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ── xcelsior_pricing ────────────────────────────────────────────────

// NewPricingDataSource returns the xcelsior_pricing data source.
func NewPricingDataSource() datasource.DataSource { return &pricingDataSource{} }

type pricingDataSource struct{ data *providerData }

type rateModel struct {
	GPUModel                 types.String  `tfsdk:"gpu_model"`
	VRAMGB                   types.Float64 `tfsdk:"vram_gb"`
	OnDemandPerHourMicros    types.Int64   `tfsdk:"on_demand_per_hour_micros"`
	SpotPerHourMicros        types.Int64   `tfsdk:"spot_per_hour_micros"`
	Reserved1MoPerHourMicros types.Int64   `tfsdk:"reserved_1mo_per_hour_micros"`
	Reserved3MoPerHourMicros types.Int64   `tfsdk:"reserved_3mo_per_hour_micros"`
	Reserved1YrPerHourMicros types.Int64   `tfsdk:"reserved_1yr_per_hour_micros"`
}

type pricingModel struct {
	GPUModel types.String `tfsdk:"gpu_model"`
	Currency types.String `tfsdk:"currency"`
	Rates    []rateModel  `tfsdk:"rates"`
}

func (d *pricingDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pricing"
}

func (d *pricingDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	rate := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, MarkdownDescription: desc + " Null when the GPU has no such rate."}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The published rate card. Rates are per GPU-hour in millionths of `currency`.",
		Attributes: map[string]schema.Attribute{
			"gpu_model": schema.StringAttribute{Optional: true, MarkdownDescription: "Only this GPU model (case-insensitive)."},
			"currency":  schema.StringAttribute{Computed: true, MarkdownDescription: "Currency the rates are settled in."},
			"rates": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"gpu_model":                    schema.StringAttribute{Computed: true},
					"vram_gb":                      schema.Float64Attribute{Computed: true},
					"on_demand_per_hour_micros":    rate("On-demand rate."),
					"spot_per_hour_micros":         rate("Spot rate."),
					"reserved_1mo_per_hour_micros": rate("Rate under a one-month commitment."),
					"reserved_3mo_per_hour_micros": rate("Rate under a three-month commitment."),
					"reserved_1yr_per_hour_micros": rate("Rate under a one-year commitment."),
				}},
			},
		},
	}
}

func (d *pricingDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configuredData(req.ProviderData, resp.Diagnostics.AddError)
}

func (d *pricingDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m pricingModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	card, err := d.data.client.PricingReference(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the rate card", err.Error())
		return
	}
	rows := card.Reference
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].GPUModel < rows[j].GPUModel })
	want := strings.ToLower(strings.TrimSpace(m.GPUModel.ValueString()))
	m.Rates = []rateModel{}
	for _, row := range rows {
		if want != "" && strings.ToLower(row.GPUModel) != want {
			continue
		}
		m.Rates = append(m.Rates, rateModel{
			GPUModel:                 types.StringValue(row.GPUModel),
			VRAMGB:                   types.Float64Value(row.VRAMGB),
			OnDemandPerHourMicros:    microsOrNull(row.OnDemandCAD),
			SpotPerHourMicros:        microsOrNull(row.SpotCAD),
			Reserved1MoPerHourMicros: microsOrNull(row.Reserved1MoCAD),
			Reserved3MoPerHourMicros: microsOrNull(row.Reserved3MoCAD),
			Reserved1YrPerHourMicros: microsOrNull(row.Reserved1YrCAD),
		})
	}
	m.Currency = types.StringValue(card.Currency)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
