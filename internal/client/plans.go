package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ── Launch plans ────────────────────────────────────────────────────
//
// Every launch goes through an action plan: preview (create), approve,
// execute. Approval comes from the credential's spend envelope
// (approval_mode "standing_policy") or from a person in the dashboard
// (approval_mode "human"). See RunPlan.

// InstanceSpec is the launch spec (`JobIn` in the API). Zero values are
// omitted so the API's own defaults apply.
type InstanceSpec struct {
	Name               string   `json:"name"`
	GPUModel           string   `json:"gpu_model,omitempty"`
	NumGPUs            int64    `json:"num_gpus,omitempty"`
	VRAMNeededGB       float64  `json:"vram_needed_gb,omitempty"`
	Tier               string   `json:"tier,omitempty"`
	PricingMode        string   `json:"pricing_mode,omitempty"`
	Image              string   `json:"image,omitempty"`
	Command            string   `json:"command,omitempty"`
	Interactive        *bool    `json:"interactive,omitempty"`
	HostID             string   `json:"host_id,omitempty"`
	VolumeIDs          []string `json:"volume_ids,omitempty"`
	EncryptedWorkspace bool     `json:"encrypted_workspace,omitempty"`
	ExclusiveGPU       bool     `json:"exclusive_gpu,omitempty"`
	InitScript         string   `json:"init_script,omitempty"`
	GitRepo            string   `json:"git_repo,omitempty"`
	ExposedPorts       []int64  `json:"exposed_ports,omitempty"`
}

// PolicyDecision is what the credential's spend envelope decided.
type PolicyDecision struct {
	Allowed      bool     `json:"allowed"`
	ApprovalMode string   `json:"approval_mode"`
	Reasons      []string `json:"reasons"`
}

// Estimate is the quoted price, in integer micros of Currency.
type Estimate struct {
	QuoteID           string  `json:"quote_id"`
	PricingVersion    string  `json:"pricing_version"`
	Currency          string  `json:"currency"`
	HourlyBurnMicros  int64   `json:"hourly_burn_micros"`
	EstimateMicros    int64   `json:"estimate_micros"`
	RunwayHours       float64 `json:"runway_hours"`
	ApprovedMaxMicros int64   `json:"approved_max_micros"`
}

// Micros is the amount the plan approves: the instance estimate, or the
// serverless approved maximum.
func (e Estimate) Micros() int64 {
	if e.EstimateMicros != 0 {
		return e.EstimateMicros
	}
	return e.ApprovedMaxMicros
}

// Availability is the placement check a preview ran.
type Availability struct {
	Feasible      *bool `json:"feasible"`
	EligibleHosts int64 `json:"eligible_hosts"`
	// Reason is a string or a structured object, depending on the check.
	Reason any `json:"reason"`
}

// ReasonText renders Reason for a diagnostic.
func (a Availability) ReasonText() string {
	switch r := a.Reason.(type) {
	case nil:
		return ""
	case string:
		return r
	default:
		buf, _ := json.Marshal(r)
		return string(buf)
	}
}

// Preview is the answer to a plan create or a quote.
type Preview struct {
	OK            bool           `json:"ok"`
	PlanID        string         `json:"plan_id"`
	Status        string         `json:"status"`
	ApprovalState string         `json:"approval_state"`
	ApprovalMode  string         `json:"approval_mode"`
	Policy        PolicyDecision `json:"policy"`
	Estimate      Estimate       `json:"estimate"`
	Availability  Availability   `json:"availability"`
	ExpiresAt     string         `json:"expires_at"`
	ApprovalURL   string         `json:"approval_url"`
}

// Plan is a launch plan as GET /api/v1/launch-plans/{id} reports it.
type Plan struct {
	PlanID              string  `json:"plan_id"`
	Status              string  `json:"status"`
	ApprovalMode        string  `json:"approval_mode"`
	ApprovalMethod      *string `json:"approval_method"`
	EstimateMicros      *int64  `json:"estimate_micros"`
	ExpiresAt           string  `json:"expires_at"`
	JobID               *string `json:"job_id"`
	Version             int64   `json:"version"`
	ActionType          string  `json:"action_type"`
	ResultingResourceID *string `json:"resulting_resource_id"`
}

// QuoteLaunch answers what a preview would, and creates nothing. Used at
// `terraform plan` time.
func (c *Client) QuoteLaunch(ctx context.Context, spec InstanceSpec) (*Preview, error) {
	var out Preview
	if err := c.do(ctx, http.MethodPost, "/api/v1/launch-plans/quote", nil, spec, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateLaunchPlan previews a launch and persists the plan.
func (c *Client) CreateLaunchPlan(ctx context.Context, spec InstanceSpec) (*Preview, error) {
	var out Preview
	if err := c.do(ctx, http.MethodPost, "/api/v1/launch-plans", nil, spec, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPlan reads a plan.
func (c *Client) GetPlan(ctx context.Context, planID string) (*Plan, error) {
	var out struct {
		Plan Plan `json:"plan"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/launch-plans/"+url.PathEscape(planID), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.Plan, nil
}

// ApprovePlan asks for approval with the provider's own token. It succeeds
// only when the plan is inside the token's spend envelope; otherwise the API
// answers 409 human_approval_required (or auto_approval_denied).
func (c *Client) ApprovePlan(ctx context.Context, planID string) (*Plan, error) {
	var out struct {
		Plan Plan `json:"plan"`
	}
	body := map[string]any{"confirm": true}
	if err := c.do(ctx, http.MethodPost, "/api/v1/launch-plans/"+url.PathEscape(planID)+"/approve", nil, body, &out); err != nil {
		return nil, err
	}
	return &out.Plan, nil
}

// RevokePlan withdraws a plan so it no longer waits in anyone's dashboard.
func (c *Client) RevokePlan(ctx context.Context, planID, reason string) error {
	body := map[string]any{"reason": reason}
	return c.do(ctx, http.MethodPost, "/api/v1/launch-plans/"+url.PathEscape(planID)+"/revoke", nil, body, nil)
}

// LaunchResult is what executing an instance plan started.
type LaunchResult struct {
	Job struct {
		JobID string `json:"job_id"`
		Phase string `json:"phase"`
	} `json:"job"`
	Idempotent bool `json:"idempotent"`
}

// ExecuteLaunchPlan starts an approved instance plan. A 409 quote_changed
// carries a replacement plan; see ReplacementPlan.
func (c *Client) ExecuteLaunchPlan(ctx context.Context, planID string) (*LaunchResult, error) {
	var out LaunchResult
	body := map[string]any{"confirm": true}
	if err := c.do(ctx, http.MethodPost, "/api/v1/launch-plans/"+url.PathEscape(planID)+"/execute", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReplacementPlan extracts the replacement preview from a 409 quote_changed.
func ReplacementPlan(err error) (*Preview, bool) {
	apiErr, ok := asAPIError(err)
	if !ok || apiErr.Code != "quote_changed" || apiErr.Body == nil {
		return nil, false
	}
	raw, ok := apiErr.Body["replacement_plan"]
	if !ok || raw == nil {
		return nil, false
	}
	buf, err2 := json.Marshal(raw)
	if err2 != nil {
		return nil, false
	}
	var p Preview
	if json.Unmarshal(buf, &p) != nil || p.PlanID == "" {
		return nil, false
	}
	return &p, true
}

// ── The approval runner ─────────────────────────────────────────────

// ApprovalOptions controls how RunPlan waits for a person.
type ApprovalOptions struct {
	// Timeout is how long to wait for a person to approve a plan outside the
	// envelope. Zero fails at once, which is what an unattended pipeline
	// wants.
	Timeout time.Duration
	// PollInterval between plan reads while waiting. Defaults to 5s.
	PollInterval time.Duration
	// Notify is called once with the approval link and the reasons, before
	// waiting, so the caller can log them where a person will see them.
	Notify func(link string, reasons []string)
}

// ApprovalRequiredError means a plan needed a person and none approved it in
// time. The plan has been revoked.
type ApprovalRequiredError struct {
	PlanID  string
	Link    string
	Reasons []string
	Waited  time.Duration
}

func (e *ApprovalRequiredError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "launch plan %s needs a person's approval", e.PlanID)
	if len(e.Reasons) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(e.Reasons, "; "))
	}
	if e.Waited > 0 {
		fmt.Fprintf(&b, " and was not approved within %s", e.Waited)
	}
	fmt.Fprintf(&b, ". The plan was revoked. Either set a spend envelope on this token that covers the launch, or raise the provider's approval_timeout and approve it at %s", e.Link)
	return b.String()
}

// PlanStep creates a plan and runs it once it is approved. Create returns a
// fresh preview; Execute runs an approved plan and returns the id of the
// resource it made.
type PlanStep struct {
	Create  func(ctx context.Context) (*Preview, error)
	Execute func(ctx context.Context, planID string) (string, error)
}

const maxQuoteRetries = 3

// RunPlan drives one plan from preview to execution:
//
//  1. create the plan;
//  2. if the envelope covers it, approve it with the same token;
//     otherwise print the approval link and poll until a person approves,
//     revokes, or the plan expires, up to opts.Timeout (revoking it on
//     timeout so nothing is left waiting);
//  3. execute; on 409 quote_changed, start again from the replacement plan,
//     at most three times.
func (c *Client) RunPlan(ctx context.Context, step PlanStep, opts ApprovalOptions) (string, error) {
	preview, err := step.Create(ctx)
	if err != nil {
		return "", err
	}
	for attempt := 0; ; attempt++ {
		if err := c.awaitApproval(ctx, preview, opts); err != nil {
			return "", err
		}
		id, err := c.executeReplaying(ctx, step, preview.PlanID, opts.PollInterval)
		if err == nil {
			return id, nil
		}
		if replacement, ok := ReplacementPlan(err); ok {
			if attempt < maxQuoteRetries {
				preview = replacement
				continue
			}
			// Giving up: do not leave the API's replacement waiting for a person.
			c.revokeQuietly(replacement.PlanID, "terraform: price kept moving; gave up after retries")
			return "", fmt.Errorf("the price moved beyond the approved tolerance %d times in a row; run apply again: %w", maxQuoteRetries+1, err)
		}
		// From here the plan will not be executed again: withdraw it, so an
		// approved spend is not left standing until it expires. A plan the
		// API has already moved past approved refuses the revoke, which is
		// the outcome wanted.
		c.revokeQuietly(preview.PlanID, "terraform: execute failed")
		if HasCode(err, "daily_spend_limit_exceeded") || HasCode(err, "hourly_spend_limit_exceeded") {
			// Another launch took the last of the window between preview
			// and execute. A fresh plan now lands on a person.
			if attempt < maxQuoteRetries {
				preview, err = step.Create(ctx)
				if err != nil {
					return "", err
				}
				continue
			}
		}
		return "", err
	}
}

func (c *Client) awaitApproval(ctx context.Context, p *Preview, opts ApprovalOptions) error {
	if p.ApprovalMode == "standing_policy" || p.ApprovalMode == "none" {
		_, err := c.ApprovePlan(ctx, p.PlanID)
		if err == nil {
			return nil
		}
		if !HasCode(err, "auto_approval_denied") && !HasCode(err, "human_approval_required") {
			return err
		}
		// The envelope changed between preview and approve: a person decides.
	}
	link := c.URL(p.ApprovalURL)
	if link == "" {
		link = c.URL("/dashboard/launch-plans/" + p.PlanID)
	}
	reasons := p.Policy.Reasons
	if opts.Notify != nil {
		opts.Notify(link, reasons)
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	start := time.Now()
	deadline := start.Add(opts.Timeout)
	for {
		plan, err := c.GetPlan(ctx, p.PlanID)
		if err != nil {
			return err
		}
		switch plan.Status {
		case "approved", "executing", "succeeded":
			return nil
		case "revoked":
			return fmt.Errorf("launch plan %s was revoked before it was approved", p.PlanID)
		case "expired":
			return fmt.Errorf("launch plan %s expired before it was approved; run apply again for a fresh quote", p.PlanID)
		case "failed":
			return fmt.Errorf("launch plan %s failed", p.PlanID)
		}
		if !time.Now().Before(deadline) {
			// Leave nothing waiting in the dashboard. Background context: the
			// caller's may already be cancelled.
			c.revokeQuietly(p.PlanID, "terraform: approval_timeout reached")
			return &ApprovalRequiredError{PlanID: p.PlanID, Link: link, Reasons: reasons, Waited: opts.Timeout}
		}
		wait := interval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			c.revokeQuietly(p.PlanID, "terraform: apply cancelled")
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// executeAttempts bounds executeReplaying.
const executeAttempts = 3

// executeReplaying runs step.Execute, and repeats it when the answer was lost
// or the API was briefly unavailable. Execute is exactly-once per plan, so a
// repeat returns the first launch instead of starting a second one, and the
// instance a lost answer launched is not orphaned outside Terraform's state.
func (c *Client) executeReplaying(ctx context.Context, step PlanStep, planID string, pause time.Duration) (string, error) {
	if pause <= 0 {
		pause = 2 * time.Second
	}
	var err error
	for i := 1; ; i++ {
		var id string
		id, err = step.Execute(ctx, planID)
		if err == nil || i == executeAttempts || ctx.Err() != nil || !executeMayReplay(err) {
			return id, err
		}
		select {
		case <-ctx.Done():
			return "", err
		case <-time.After(pause * time.Duration(i)):
		}
	}
}

func executeMayReplay(err error) bool {
	apiErr, ok := asAPIError(err)
	if !ok {
		return true // the request or its answer was lost in transit
	}
	switch apiErr.Status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// revokeQuietly withdraws a plan on a fresh context (the caller's may be
// cancelled already). A failure is ignored: the plan expires on its own.
func (c *Client) revokeQuietly(planID, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = c.RevokePlan(ctx, planID, reason)
}
