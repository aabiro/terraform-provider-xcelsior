package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/aabiro/terraform-provider-xcelsior/internal/client"
)

func strOrNull(p *string) types.String {
	if p == nil || *p == "" {
		return types.StringNull()
	}
	return types.StringValue(*p)
}

func int64OrNull(p *int64) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*p)
}

func microsOrNull(p *float64) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(client.CADMicros(*p))
}

// fillString sets dst from the API when Terraform has no value for it yet,
// which is the case after an import. A configured value is never replaced,
// so a normalised echo from the API cannot cause a diff.
func fillString(dst *types.String, api string) {
	if (dst.IsNull() || dst.IsUnknown()) && api != "" {
		*dst = types.StringValue(api)
	}
}

func fillInt64(dst *types.Int64, api *int64) {
	if (dst.IsNull() || dst.IsUnknown()) && api != nil {
		*dst = types.Int64Value(*api)
	}
}

func stringList(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var out []string
	diags := l.ElementsAs(ctx, &out, false)
	return out, diags
}

func int64List(ctx context.Context, l types.List) ([]int64, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var out []int64
	diags := l.ElementsAs(ctx, &out, false)
	return out, diags
}

// waitFor polls check every interval until it reports done, fails, or ctx
// ends.
func waitFor(ctx context.Context, interval time.Duration, what string, check func() (bool, error)) error {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		done, err := check()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s: %w", what, ctx.Err())
		case <-time.After(interval):
		}
	}
}

// approvalWarning is the plan-time warning for a launch that will stop for a
// person.
func approvalWarning(endpointURL string, reasons []string) string {
	var b strings.Builder
	b.WriteString("This launch is outside the token's spend envelope, so apply will create a launch plan and wait for a person to approve it")
	if len(reasons) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(reasons, "; "))
	}
	fmt.Fprintf(&b, ". Approve it under Launch plans at %s, within the provider's approval_timeout; with the default of 0s, apply fails at once and revokes the plan.", endpointURL)
	return b.String()
}
