package provider

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var testFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"xcelsior": providerserver.NewProtocol6WithError(New("test")()),
}

// requireTerraform skips a test that drives the Terraform CLI when none is
// installed, rather than letting the test framework download one.
func requireTerraform(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC_TERRAFORM_PATH") != "" {
		return
	}
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("no terraform CLI: set TF_ACC_TERRAFORM_PATH or put terraform on PATH (see README)")
	}
}

// fakeTest starts a fake API and returns it with a provider block for it.
func fakeTest(t *testing.T) (*fakeAPI, func(approvalTimeout string) string) {
	t.Helper()
	requireTerraform(t)
	t.Setenv("XCELSIOR_TF_POLL_INTERVAL", "10ms")
	t.Setenv(EnvAPIToken, "")
	t.Setenv(EnvEndpoint, "")
	t.Setenv(EnvApprovalTimeout, "")
	f := newFakeAPI(t)
	return f, func(approvalTimeout string) string {
		return fmt.Sprintf(`
provider "xcelsior" {
  endpoint         = %q
  api_token        = %q
  approval_timeout = %q
}
`, f.srv.URL, fakeToken, approvalTimeout)
	}
}

const instanceConfig = `
resource "xcelsior_instance" "test" {
  name      = "tf-unit"
  gpu_model = "RTX 2060"
}
`

func noneLeft(f *fakeAPI, what string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if n := f.count(what); n != 0 {
			return fmt.Errorf("%d %s left after destroy", n, what)
		}
		return nil
	}
}

func TestAnInstanceInsideTheEnvelopeLaunchesWaitsForRunningAndTerminates(t *testing.T) {
	f, provider := fakeTest(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy:             noneLeft(f, "instances"),
		Steps: []resource.TestStep{
			{
				Config: provider("0s") + instanceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("xcelsior_instance.test", "id"),
					resource.TestCheckResourceAttrSet("xcelsior_instance.test", "launch_plan_id"),
					resource.TestCheckResourceAttr("xcelsior_instance.test", "status", "running"),
					resource.TestCheckResourceAttr("xcelsior_instance.test", "num_gpus", "1"),
					resource.TestCheckResourceAttr("xcelsior_instance.test", "pricing_mode", "on_demand"),
					resource.TestCheckResourceAttr("xcelsior_instance.test", "host_ip", "203.0.113.10"),
					resource.TestCheckResourceAttr("xcelsior_instance.test", "ssh_port", "2222"),
					resource.TestCheckResourceAttr("xcelsior_instance.test", "rate_per_hour_micros", "450000"),
					resource.TestCheckNoResourceAttr("xcelsior_instance.test", "ssh_password"),
					func(*terraform.State) error {
						if f.count("quotes") == 0 {
							return fmt.Errorf("terraform plan did not quote the launch")
						}
						return nil
					},
				),
			},
			{
				ResourceName:            "xcelsior_instance.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"launch_plan_id", "timeouts"},
			},
		},
	})
}

func TestALaunchOutsideTheEnvelopeFailsAtOnceAndLeavesNoPlanWaiting(t *testing.T) {
	f, provider := fakeTest(t)
	f.envelope = false
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{{
			Config:      provider("0s") + instanceConfig,
			ExpectError: regexp.MustCompile(`(?s)needs a person's approval.*daily ceiling.*revoked`),
		}},
	})
	statuses := f.planStatuses()
	if len(statuses) != 1 {
		t.Fatalf("plans %v, want exactly one", statuses)
	}
	for id, s := range statuses {
		if s != "revoked" {
			t.Fatalf("plan %s is %q, want revoked", id, s)
		}
	}
	if n := f.count("instances"); n != 0 {
		t.Fatalf("%d instances launched without approval", n)
	}
}

func TestApplyWaitsForAPersonToApproveWithinTheApprovalTimeout(t *testing.T) {
	f, provider := fakeTest(t)
	f.envelope = false
	f.approveAfterGets = 3
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy:             noneLeft(f, "instances"),
		Steps: []resource.TestStep{{
			Config: provider("30s") + instanceConfig,
			Check:  resource.TestCheckResourceAttr("xcelsior_instance.test", "status", "running"),
		}},
	})
}

func TestAPriceMoveIsFollowedToTheReplacementPlan(t *testing.T) {
	f, provider := fakeTest(t)
	f.quoteChangedOnce = true
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy:             noneLeft(f, "instances"),
		Steps: []resource.TestStep{{
			Config: provider("0s") + instanceConfig,
			Check: func(*terraform.State) error {
				var superseded, succeeded int
				for _, s := range f.planStatuses() {
					switch s {
					case "superseded":
						superseded++
					case "succeeded":
						succeeded++
					}
				}
				if superseded != 1 || succeeded != 1 {
					return fmt.Errorf("plans %v", f.planStatuses())
				}
				return nil
			},
		}},
	})
}

func TestASpecTheAPIRefusesFailsAtPlanTime(t *testing.T) {
	f, provider := fakeTest(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{{
			Config:      provider("0s") + strings.Replace(instanceConfig, "RTX 2060", "NOPE", 1),
			ExpectError: regexp.MustCompile(`refused this launch`),
		}},
	})
	if len(f.planStatuses()) != 0 {
		t.Fatalf("a refused spec created plans: %v", f.planStatuses())
	}
}

func TestAQueuedInstanceIsCancelledOnDestroy(t *testing.T) {
	f, provider := fakeTest(t)
	f.instanceStartsAfterGets = 1_000_000
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy:             noneLeft(f, "instances"),
		Steps: []resource.TestStep{{
			Config: provider("0s") + `
resource "xcelsior_instance" "test" {
  name             = "tf-unit"
  gpu_model        = "RTX 2060"
  pricing_mode     = "spot"
  wait_for_running = false
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("xcelsior_instance.test", "status", "queued"),
				resource.TestCheckResourceAttr("xcelsior_instance.test", "pricing_mode", "spot"),
			),
		}},
	})
}

func TestAnInstanceTerminatedOutsideTerraformIsPlannedAgain(t *testing.T) {
	f, provider := fakeTest(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{Config: provider("0s") + instanceConfig},
			{
				PreConfig: func() {
					f.mu.Lock()
					for _, inst := range f.instances {
						inst["status"] = "terminated"
					}
					f.mu.Unlock()
				},
				Config:             provider("0s") + instanceConfig,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAnInvalidPricingModeIsRefusedBeforeAnyCall(t *testing.T) {
	f, provider := fakeTest(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{{
			Config:      provider("0s") + strings.Replace(instanceConfig, `gpu_model = "RTX 2060"`, `gpu_model = "RTX 2060"`+"\n  pricing_mode = \"reserved\"", 1),
			ExpectError: regexp.MustCompile(`Invalid pricing_mode`),
		}},
	})
	if f.count("quotes") != 0 {
		t.Fatal("an invalid config reached the API")
	}
}

func TestAVolumeIsRenamedInPlaceAndReplacedOnResize(t *testing.T) {
	f, provider := fakeTest(t)
	volume := func(name string, size int) string {
		return fmt.Sprintf(`
resource "xcelsior_volume" "data" {
  name    = %q
  size_gb = %d
}
`, name, size)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy:             noneLeft(f, "volumes"),
		Steps: []resource.TestStep{
			{
				Config: provider("0s") + volume("datasets", 50),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("xcelsior_volume.data", "status", "available"),
					resource.TestCheckResourceAttr("xcelsior_volume.data", "encrypted", "true"),
					resource.TestCheckResourceAttr("xcelsior_volume.data", "region", "ca-on"),
				),
			},
			{
				Config: provider("0s") + volume("datasets-v2", 50),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("xcelsior_volume.data", plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr("xcelsior_volume.data", "name", "datasets-v2"),
			},
			{
				Config: provider("0s") + volume("datasets-v2", 100),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("xcelsior_volume.data", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
			{ResourceName: "xcelsior_volume.data", ImportState: true, ImportStateVerify: true},
		},
	})
}

func TestAVolumeNameAlreadyTakenFailsAtApply(t *testing.T) {
	f, provider := fakeTest(t)
	f.volumes["vol-0"] = map[string]any{"volume_id": "vol-0", "name": "taken", "size_gb": 1, "status": "available"}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{{
			Config: provider("0s") + `
resource "xcelsior_volume" "data" {
  name    = "taken"
  size_gb = 1
}
`,
			ExpectError: regexp.MustCompile(`(?s)Could not create the volume.*already exists`),
		}},
	})
}

func TestAVolumeAttachesToARunningInstanceAndDetachesOnDestroy(t *testing.T) {
	f, provider := fakeTest(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			noneLeft(f, "volumes"), noneLeft(f, "instances"),
		),
		Steps: []resource.TestStep{{
			Config: provider("0s") + instanceConfig + `
resource "xcelsior_volume" "data" {
  name    = "scratch"
  size_gb = 10
}

resource "xcelsior_volume_attachment" "data" {
  volume_id   = xcelsior_volume.data.id
  instance_id = xcelsior_instance.test.id
  mount_path  = "/data"
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("xcelsior_volume_attachment.data", "mode", "rw"),
				resource.TestCheckResourceAttrPair("xcelsior_volume_attachment.data", "instance_id", "xcelsior_instance.test", "id"),
			),
		}},
	})
}

func TestAnSSHKeyRegistersImportsAndKeepsItsSpelling(t *testing.T) {
	f, provider := fakeTest(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy:             noneLeft(f, "keys"),
		Steps: []resource.TestStep{
			{
				// Two spaces: the API normalises them, and a refresh must not
				// plan a replacement for it.
				Config: provider("0s") + `
resource "xcelsior_ssh_key" "laptop" {
  name       = "laptop"
  public_key = "ssh-ed25519  AAAAC3NzaC1lZDI1NTE5AAAAIFake laptop"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("xcelsior_ssh_key.laptop", "fingerprint"),
					resource.TestCheckResourceAttr("xcelsior_ssh_key.laptop", "public_key", "ssh-ed25519  AAAAC3NzaC1lZDI1NTE5AAAAIFake laptop"),
				),
			},
			{
				ResourceName: "xcelsior_ssh_key.laptop", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"public_key"},
			},
		},
	})
}

func TestAnSSHKeyIsKeptWhenTheTokenCannotListKeys(t *testing.T) {
	f, provider := fakeTest(t)
	f.noSSHRead = true
	config := provider("0s") + `
resource "xcelsior_ssh_key" "ci" {
  name       = "ci"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFake ci"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy:             noneLeft(f, "keys"),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttrSet("xcelsior_ssh_key.ci", "fingerprint"),
			},
			// A refresh that cannot list keys keeps the state, so a second
			// plan is empty rather than a replacement.
			{Config: config, PlanOnly: true},
			{
				ResourceName: "xcelsior_ssh_key.ci", ImportState: true,
				ExpectError: regexp.MustCompile(`Cannot import this SSH key`),
			},
		},
	})
}

func TestAServerlessEndpointIsRenamedInPlaceAndReplacedWhenItsWorkersChange(t *testing.T) {
	f, provider := fakeTest(t)
	endpoint := func(name string, maxWorkers int) string {
		return fmt.Sprintf(`
resource "xcelsior_serverless_endpoint" "chat" {
  name        = %q
  model_ref   = "Qwen/Qwen2.5-0.5B-Instruct"
  gpu_tier    = "RTX 3060"
  min_workers = 0
  max_workers = %d
  env = {
    HF_TOKEN = "not-a-real-token"
  }
}
`, name, maxWorkers)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy:             noneLeft(f, "endpoints"),
		Steps: []resource.TestStep{
			{
				Config: provider("0s") + endpoint("chat", 2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("xcelsior_serverless_endpoint.chat", "status", "active"),
					resource.TestCheckResourceAttr("xcelsior_serverless_endpoint.chat", "mode", "preset"),
					resource.TestCheckResourceAttr("xcelsior_serverless_endpoint.chat", "min_workers", "0"),
					resource.TestCheckResourceAttr("xcelsior_serverless_endpoint.chat", "gpu_count", "1"),
					resource.TestCheckResourceAttrSet("xcelsior_serverless_endpoint.chat", "launch_plan_id"),
				),
			},
			{
				Config: provider("0s") + endpoint("chat-v2", 2),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("xcelsior_serverless_endpoint.chat", plancheck.ResourceActionUpdate),
				}},
			},
			{
				Config: provider("0s") + endpoint("chat-v2", 3),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("xcelsior_serverless_endpoint.chat", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
		},
	})
}

func TestAnEndpointOutsideTheEnvelopeFailsAtOnce(t *testing.T) {
	f, provider := fakeTest(t)
	f.envelope = false
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{{
			Config: provider("0s") + `
resource "xcelsior_serverless_endpoint" "chat" {
  name     = "chat"
  gpu_tier = "RTX 3060"
}
`,
			ExpectError: regexp.MustCompile(`needs a person's approval`),
		}},
	})
	if f.count("endpoints") != 0 {
		t.Fatal("an endpoint was created without approval")
	}
}

func TestTheDataSourcesReportPricesInMicros(t *testing.T) {
	_, provider := fakeTest(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{{
			Config: provider("0s") + `
data "xcelsior_gpus" "rtx2060" {
  gpu_model = "rtx 2060"
}

data "xcelsior_gpus" "elsewhere" {
  region = "eu-west"
}

data "xcelsior_pricing" "all" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.xcelsior_gpus.rtx2060", "gpus.#", "1"),
				resource.TestCheckResourceAttr("data.xcelsior_gpus.rtx2060", "gpus.0.price_per_hour_micros", "450000"),
				resource.TestCheckResourceAttr("data.xcelsior_gpus.rtx2060", "gpus.0.service_tiers.0", "standard"),
				resource.TestCheckResourceAttr("data.xcelsior_gpus.elsewhere", "gpus.#", "0"),
				resource.TestCheckResourceAttr("data.xcelsior_pricing.all", "currency", "CAD"),
				resource.TestCheckResourceAttr("data.xcelsior_pricing.all", "rates.#", "2"),
				resource.TestCheckResourceAttr("data.xcelsior_pricing.all", "rates.0.gpu_model", "RTX 2060"),
				resource.TestCheckNoResourceAttr("data.xcelsior_pricing.all", "rates.0.spot_per_hour_micros"),
				resource.TestCheckResourceAttr("data.xcelsior_pricing.all", "rates.0.reserved_1yr_per_hour_micros", "350000"),
				resource.TestCheckResourceAttr("data.xcelsior_pricing.all", "rates.1.on_demand_per_hour_micros", "600000"),
			),
		}},
	})
}

func TestAMissingTokenIsAClearError(t *testing.T) {
	f, _ := fakeTest(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "xcelsior" {
  endpoint = %q
}

data "xcelsior_pricing" "all" {}
`, f.srv.URL),
			ExpectError: regexp.MustCompile(`Missing API token`),
		}},
	})
}

func TestARejectedTokenFailsAtConfigure(t *testing.T) {
	f, _ := fakeTest(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "xcelsior" {
  endpoint  = %q
  api_token = "wrong"
}

data "xcelsior_pricing" "all" {}
`, f.srv.URL),
			ExpectError: regexp.MustCompile(`rejected the provider's token`),
		}},
	})
}
