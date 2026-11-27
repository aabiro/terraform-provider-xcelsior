package client

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
)

func asAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// CADMicros converts a decimal amount from the API boundary to integer
// micros, the unit the provider exposes.
func CADMicros(amount float64) int64 {
	return int64(math.Round(amount * 1_000_000))
}

// ── Instances ───────────────────────────────────────────────────────

// Instance is the subset of GET /instance/{id} the provider stores. The API
// also returns connection secrets (such as an SSH password); they are not
// decoded, so they never reach Terraform state.
type Instance struct {
	JobID              string   `json:"job_id"`
	Name               string   `json:"name"`
	Status             string   `json:"status"`
	HostID             *string  `json:"host_id"`
	GPUModel           *string  `json:"gpu_model"`
	NumGPUs            *int64   `json:"num_gpus"`
	PricingMode        *string  `json:"pricing_mode"`
	Image              *string  `json:"image"`
	HostIP             *string  `json:"host_ip"`
	SSHPort            *int64   `json:"ssh_port"`
	HostKeyFingerprint *string  `json:"host_key_fingerprint"`
	RatePerHourCAD     *float64 `json:"rate_per_hour_cad"`
	Region             *string  `json:"region"`
}

// TerminalInstanceStatuses are the states an instance does not leave.
var TerminalInstanceStatuses = map[string]bool{
	"terminated": true, "completed": true, "failed": true, "preempted": true, "cancelled": true,
}

// GetInstance reads one instance.
func (c *Client) GetInstance(ctx context.Context, id string) (*Instance, error) {
	var out struct {
		Instance Instance `json:"instance"`
	}
	if err := c.do(ctx, http.MethodGet, "/instance/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.Instance, nil
}

// TerminateInstance permanently stops a running instance.
func (c *Client) TerminateInstance(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/instances/"+url.PathEscape(id)+"/terminate", nil, map[string]any{}, nil)
}

// CancelInstance cancels an instance that has not started.
func (c *Client) CancelInstance(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/instances/"+url.PathEscape(id)+"/cancel", nil, map[string]any{}, nil)
}

// ── Volumes ─────────────────────────────────────────────────────────

// Volume is a persistent volume.
type Volume struct {
	VolumeID   string  `json:"volume_id"`
	Name       string  `json:"name"`
	SizeGB     int64   `json:"size_gb"`
	Region     string  `json:"region"`
	Province   *string `json:"province"`
	Encrypted  bool    `json:"encrypted"`
	Status     string  `json:"status"`
	AttachedTo *string `json:"attached_to"`
}

// VolumeCreate is the create body. Preview runs every check and creates
// nothing.
type VolumeCreate struct {
	Name      string `json:"name"`
	SizeGB    int64  `json:"size_gb"`
	Region    string `json:"region,omitempty"`
	Encrypted bool   `json:"encrypted"`
	Preview   bool   `json:"preview,omitempty"`
}

// VolumePreview is a create preview: the price, and the name the API accepted.
type VolumePreview struct {
	Volume struct {
		Name string `json:"name"`
	} `json:"volume"`
	PricePerGBMonthCAD      float64 `json:"price_per_gb_month_cad"`
	EstimatedMonthlyCostCAD float64 `json:"estimated_monthly_cost_cad"`
}

// PreviewVolume runs a create's checks and returns its price.
func (c *Client) PreviewVolume(ctx context.Context, body VolumeCreate) (*VolumePreview, error) {
	body.Preview = true
	var out VolumePreview
	if err := c.do(ctx, http.MethodPost, "/api/v2/volumes", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateVolume creates a volume.
func (c *Client) CreateVolume(ctx context.Context, body VolumeCreate) (*Volume, error) {
	body.Preview = false
	var out struct {
		Volume Volume `json:"volume"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v2/volumes", nil, body, &out); err != nil {
		return nil, err
	}
	return &out.Volume, nil
}

// GetVolume reads a volume.
func (c *Client) GetVolume(ctx context.Context, id string) (*Volume, error) {
	var out struct {
		Volume Volume `json:"volume"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v2/volumes/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.Volume, nil
}

// RenameVolume changes a volume's name in place.
func (c *Client) RenameVolume(ctx context.Context, id, name string) error {
	return c.do(ctx, http.MethodPatch, "/api/v2/volumes/"+url.PathEscape(id), nil, map[string]any{"name": name}, nil)
}

// DeleteVolume deletes a volume. The API refuses (409) while it is attached.
func (c *Client) DeleteVolume(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v2/volumes/"+url.PathEscape(id), nil, nil, nil)
}

// AttachVolume mounts a volume in a running instance.
func (c *Client) AttachVolume(ctx context.Context, volumeID, instanceID, mountPath, mode string) error {
	body := map[string]any{"instance_id": instanceID}
	if mountPath != "" {
		body["mount_path"] = mountPath
	}
	if mode != "" {
		body["mode"] = mode
	}
	return c.do(ctx, http.MethodPost, "/api/v2/volumes/"+url.PathEscape(volumeID)+"/attach", nil, body, nil)
}

// DetachVolume unmounts a volume from its instance.
func (c *Client) DetachVolume(ctx context.Context, volumeID string) error {
	return c.do(ctx, http.MethodPost, "/api/v2/volumes/"+url.PathEscape(volumeID)+"/detach", nil, map[string]any{}, nil)
}

// ── SSH keys ────────────────────────────────────────────────────────

// SSHKey is a registered public key.
type SSHKey struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Fingerprint string  `json:"fingerprint"`
	PublicKey   string  `json:"public_key"`
	CreatedAt   float64 `json:"created_at"`
}

// CreateSSHKey registers a public key on the account.
func (c *Client) CreateSSHKey(ctx context.Context, name, publicKey string) (*SSHKey, error) {
	var out SSHKey
	body := map[string]any{"name": name, "public_key": publicKey}
	if err := c.do(ctx, http.MethodPost, "/api/ssh/keys", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListSSHKeys lists the account's keys. There is no single-key read.
func (c *Client) ListSSHKeys(ctx context.Context) ([]SSHKey, error) {
	var out struct {
		Keys []SSHKey `json:"keys"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/ssh/keys", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// DeleteSSHKey removes a key.
func (c *Client) DeleteSSHKey(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/ssh/keys/"+url.PathEscape(id), nil, nil, nil)
}

// ── Serverless endpoints ────────────────────────────────────────────

// EndpointSpec is the endpoint create body (`ServerlessEndpointCreate`).
type EndpointSpec struct {
	Name           string            `json:"name"`
	Mode           string            `json:"mode,omitempty"`
	ManagedEngine  string            `json:"managed_engine,omitempty"`
	ModelRef       string            `json:"model_ref,omitempty"`
	DockerImage    string            `json:"docker_image,omitempty"`
	GPUTier        string            `json:"gpu_tier,omitempty"`
	GPUCount       int64             `json:"gpu_count,omitempty"`
	Region         string            `json:"region,omitempty"`
	MinWorkers     *int64            `json:"min_workers,omitempty"`
	MaxWorkers     int64             `json:"max_workers,omitempty"`
	MaxConcurrency int64             `json:"max_concurrency,omitempty"`
	IdleTimeoutSec int64             `json:"idle_timeout_sec,omitempty"`
	HTTPPort       int64             `json:"http_port,omitempty"`
	HealthPath     string            `json:"health_check_path,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
}

// Endpoint is a serverless endpoint.
type Endpoint struct {
	EndpointID     string `json:"endpoint_id"`
	Name           string `json:"name"`
	Mode           string `json:"mode"`
	ModelRef       string `json:"model_ref"`
	GPUTier        string `json:"gpu_tier"`
	GPUCount       int64  `json:"gpu_count"`
	Region         string `json:"region"`
	Status         string `json:"status"`
	MinWorkers     int64  `json:"min_workers"`
	MaxWorkers     int64  `json:"max_workers"`
	MaxConcurrency int64  `json:"max_concurrency"`
	IdleTimeoutSec int64  `json:"idle_timeout_sec"`
}

// CreateEndpointPlan previews an endpoint and persists its plan. Plans are
// approved through the same /api/v1/launch-plans/{id}/approve.
func (c *Client) CreateEndpointPlan(ctx context.Context, spec EndpointSpec) (*Preview, error) {
	var out Preview
	if err := c.do(ctx, http.MethodPost, "/api/v1/serverless/endpoint-plans", nil, spec, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExecuteEndpointPlan creates the endpoint an approved plan describes.
func (c *Client) ExecuteEndpointPlan(ctx context.Context, planID string) (*Endpoint, error) {
	var out struct {
		Endpoint Endpoint `json:"endpoint"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/serverless/endpoint-plans/"+url.PathEscape(planID)+"/execute", nil, map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out.Endpoint, nil
}

// GetEndpoint reads an endpoint.
func (c *Client) GetEndpoint(ctx context.Context, id string) (*Endpoint, error) {
	var out struct {
		Endpoint Endpoint `json:"endpoint"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v2/serverless/endpoints/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.Endpoint, nil
}

// RenameEndpoint changes only an endpoint's name. Worker counts are never
// patched by the provider: raising them would add spend without a plan.
func (c *Client) RenameEndpoint(ctx context.Context, id, name string) error {
	return c.do(ctx, http.MethodPatch, "/api/v2/serverless/endpoints/"+url.PathEscape(id), nil, map[string]any{"name": name}, nil)
}

// DeleteEndpoint deletes an endpoint.
func (c *Client) DeleteEndpoint(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v2/serverless/endpoints/"+url.PathEscape(id), nil, nil, nil)
}

// ── Catalog ─────────────────────────────────────────────────────────

// AvailableGPU is one row of GET /api/v2/gpu/available.
type AvailableGPU struct {
	GPUModel        string   `json:"gpu_model"`
	VRAMGB          float64  `json:"vram_gb"`
	Region          string   `json:"region"`
	Province        string   `json:"province"`
	CountAvailable  int64    `json:"count_available"`
	PricePerHourCAD float64  `json:"price_per_hour_cad"`
	ServiceTiers    []string `json:"service_tiers_available"`
}

// GPUAvailability lists free GPUs, optionally in one region.
func (c *Client) GPUAvailability(ctx context.Context, region string) ([]AvailableGPU, string, error) {
	q := url.Values{}
	if region != "" {
		q.Set("region", region)
	}
	var out struct {
		GPUs       []AvailableGPU `json:"gpus"`
		ObservedAt any            `json:"observed_at"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v2/gpu/available", q, nil, &out); err != nil {
		return nil, "", err
	}
	observed := ""
	if s, ok := out.ObservedAt.(string); ok {
		observed = s
	}
	return out.GPUs, observed, nil
}

// RateCardRow is one GPU model's published rates.
type RateCardRow struct {
	GPUModel       string   `json:"gpu_model"`
	VRAMGB         float64  `json:"vram_gb"`
	OnDemandCAD    *float64 `json:"on_demand_cad"`
	SpotCAD        *float64 `json:"spot_cad"`
	Reserved1MoCAD *float64 `json:"reserved_1mo_cad"`
	Reserved3MoCAD *float64 `json:"reserved_3mo_cad"`
	Reserved1YrCAD *float64 `json:"reserved_1yr_cad"`
}

// RateCard is the published rate card.
type RateCard struct {
	Currency  string        `json:"currency"`
	Source    string        `json:"source"`
	Reference []RateCardRow `json:"reference"`
}

// PricingReference reads the published rate card.
func (c *Client) PricingReference(ctx context.Context) (*RateCard, error) {
	var out RateCard
	if err := c.do(ctx, http.MethodGet, "/api/pricing/reference", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
