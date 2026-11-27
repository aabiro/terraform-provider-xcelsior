package provider

// Acceptance tests run against a real Xcelsior API and create billed
// resources there. They run only with TF_ACC=1, and only against a local or
// staging API: never production.
//
//	XCELSIOR_ACC_ENDPOINT   required; loopback, or a host with a test label
//	                        (staging, dev, test, sandbox, preview, qa)
//	XCELSIOR_ACC_API_TOKEN  required; an agent key on that API with a spend
//	                        envelope (see README, "Acceptance tests")
//	XCELSIOR_ACC_GPU_MODEL  for the instance test: a priced GPU model
//	XCELSIOR_ACC_WAIT_FOR_RUNNING  "false" when no host on that API can run it
//	XCELSIOR_ACC_SERVERLESS_GPU_TIER and XCELSIOR_ACC_SERVERLESS_MODEL_REF
//	                        for the serverless test
//	XCELSIOR_ACC_SKIP_VOLUMES  set when that API cannot provision volume storage
//
// Separate variables on purpose: XCELSIOR_API_TOKEN is also the name of the
// server's master admin token on operator machines, and an acceptance run
// must never pick that up.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/aabiro/terraform-provider-xcelsior/internal/client"
)

var testHostLabels = map[string]bool{
	"staging": true, "stage": true, "dev": true, "test": true, "sandbox": true, "preview": true, "qa": true,
}

// accEndpointAllowed refuses production: the endpoint must be loopback, or a
// host one of whose labels marks it as a test environment.
func accEndpointAllowed(endpoint string) error {
	if strings.TrimSpace(endpoint) == "" {
		return fmt.Errorf("XCELSIOR_ACC_ENDPOINT is not set; the provider would default to production")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("XCELSIOR_ACC_ENDPOINT %q is not a URL", endpoint)
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("XCELSIOR_ACC_ENDPOINT %q is a non-loopback address; use a named staging host", endpoint)
	}
	labels := strings.Split(host, ".")
	for _, label := range labels[:len(labels)-1] { // the top-level domain never counts
		if testHostLabels[label] {
			return nil
		}
	}
	return fmt.Errorf("XCELSIOR_ACC_ENDPOINT %q does not look like a test environment; acceptance tests never run against production", endpoint)
}

func accPreCheck(t *testing.T) {
	t.Helper()
	requireTerraform(t)
	if err := accEndpointAllowed(os.Getenv("XCELSIOR_ACC_ENDPOINT")); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("XCELSIOR_ACC_API_TOKEN") == "" {
		t.Fatal("XCELSIOR_ACC_API_TOKEN is not set")
	}
}

func accProvider() string {
	return fmt.Sprintf(`
provider "xcelsior" {
  endpoint  = %q
  api_token = %q
}
`, os.Getenv("XCELSIOR_ACC_ENDPOINT"), os.Getenv("XCELSIOR_ACC_API_TOKEN"))
}

func accClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New(os.Getenv("XCELSIOR_ACC_ENDPOINT"), os.Getenv("XCELSIOR_ACC_API_TOKEN"), "terraform-provider-xcelsior/acc", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// accName is unique per run, so a resource a failed run left behind never
// collides with the next run's.
func accName(t *testing.T) string {
	buf := make([]byte, 3)
	_, _ = rand.Read(buf)
	short := strings.ToLower(strings.TrimPrefix(t.Name(), "TestAcc"))
	if len(short) > 16 {
		short = short[:16]
	}
	return fmt.Sprintf("tf-acc-%s-%x", short, buf)
}

// accPublicKey is a fresh ed25519 public key in OpenSSH form.
func accPublicKey(t *testing.T) string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var blob []byte
	for _, field := range [][]byte{[]byte("ssh-ed25519"), pub} {
		blob = binary.BigEndian.AppendUint32(blob, uint32(len(field)))
		blob = append(blob, field...)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " tf-acc"
}

func TestAccEndpointGuardRefusesProduction(t *testing.T) {
	for _, bad := range []string{"", "https://xcelsior.ca", "https://api.xcelsior.ca", "https://203.0.113.7", "https://staging"} {
		if accEndpointAllowed(bad) == nil {
			t.Errorf("%q was allowed", bad)
		}
	}
	for _, ok := range []string{"http://127.0.0.1:9502", "http://localhost:9500", "https://staging.xcelsior.ca", "https://api.dev.example.com"} {
		if err := accEndpointAllowed(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestAccInstanceIsCreatedAndDestroyed(t *testing.T) {
	gpu := os.Getenv("XCELSIOR_ACC_GPU_MODEL")
	if os.Getenv("TF_ACC") != "" && gpu == "" {
		t.Skip("XCELSIOR_ACC_GPU_MODEL is not set")
	}
	wait := os.Getenv("XCELSIOR_ACC_WAIT_FOR_RUNNING") != "false"
	var jobID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy: func(*terraform.State) error {
			if jobID == "" {
				return nil
			}
			inst, err := accClient(t).GetInstance(context.Background(), jobID)
			if client.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if !client.TerminalInstanceStatuses[inst.Status] {
				return fmt.Errorf("instance %s is still %q after destroy", jobID, inst.Status)
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: accProvider() + fmt.Sprintf(`
resource "xcelsior_instance" "acc" {
  name             = %q
  gpu_model        = %q
  wait_for_running = %t
}
`, accName(t), gpu, wait),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("xcelsior_instance.acc", "id"),
				resource.TestCheckResourceAttrSet("xcelsior_instance.acc", "launch_plan_id"),
				resource.TestCheckResourceAttrSet("xcelsior_instance.acc", "status"),
				func(s *terraform.State) error {
					jobID = s.RootModule().Resources["xcelsior_instance.acc"].Primary.ID
					t.Logf("launched instance %s", jobID)
					return nil
				},
			),
		}},
	})
}

func TestAccSSHKey(t *testing.T) {
	name := accName(t)
	publicKey := accPublicKey(t)
	config := accProvider() + fmt.Sprintf(`
resource "xcelsior_ssh_key" "acc" {
  name       = %q
  public_key = %q
}
`, name, publicKey)
	// A Quick Connect key holds ssh:write and not ssh:read: it registers and
	// deletes keys, and import is refused. A token with ssh:read imports.
	importStep := resource.TestStep{ResourceName: "xcelsior_ssh_key.acc", ImportState: true, ImportStateVerify: true}
	if os.Getenv("TF_ACC") != "" {
		if _, err := accClient(t).ListSSHKeys(context.Background()); client.IsMissingScope(err, "ssh:read") {
			t.Log("this token cannot list SSH keys; expecting import to be refused")
			importStep = resource.TestStep{
				ResourceName: "xcelsior_ssh_key.acc", ImportState: true,
				ExpectError: regexp.MustCompile(`Cannot import this SSH key`),
			}
		}
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{Config: config, Check: resource.TestCheckResourceAttrSet("xcelsior_ssh_key.acc", "fingerprint")},
			{Config: config, PlanOnly: true},
			importStep,
		},
	})
}

func TestAccVolume(t *testing.T) {
	if os.Getenv("TF_ACC") != "" && os.Getenv("XCELSIOR_ACC_SKIP_VOLUMES") != "" {
		t.Skip("XCELSIOR_ACC_SKIP_VOLUMES is set: this API cannot provision volume storage")
	}
	name := accName(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{
				Config: accProvider() + fmt.Sprintf(`
resource "xcelsior_volume" "acc" {
  name    = %q
  size_gb = 1
}
`, name),
				Check: resource.TestCheckResourceAttr("xcelsior_volume.acc", "status", "available"),
			},
			{ResourceName: "xcelsior_volume.acc", ImportState: true, ImportStateVerify: true},
		},
	})
}

func TestAccDataSources(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{{
			Config: accProvider() + `
data "xcelsior_gpus" "all" {}
data "xcelsior_pricing" "all" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("data.xcelsior_gpus.all", "observed_at"),
				resource.TestCheckResourceAttrSet("data.xcelsior_pricing.all", "currency"),
			),
		}},
	})
}

func TestAccServerlessEndpoint(t *testing.T) {
	tier, model := os.Getenv("XCELSIOR_ACC_SERVERLESS_GPU_TIER"), os.Getenv("XCELSIOR_ACC_SERVERLESS_MODEL_REF")
	if os.Getenv("TF_ACC") != "" && (tier == "" || model == "") {
		t.Skip("XCELSIOR_ACC_SERVERLESS_GPU_TIER and XCELSIOR_ACC_SERVERLESS_MODEL_REF are not set")
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{{
			Config: accProvider() + fmt.Sprintf(`
resource "xcelsior_serverless_endpoint" "acc" {
  name        = %q
  gpu_tier    = %q
  model_ref   = %q
  min_workers = 0
  max_workers = 1
}
`, accName(t), tier, model),
			Check: resource.TestCheckResourceAttrSet("xcelsior_serverless_endpoint.acc", "id"),
		}},
	})
}
