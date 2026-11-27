package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const fakeToken = "test-token"

// fakeAPI is an in-memory stand-in for the Xcelsior routes the provider
// calls, shaped like the real responses (see internal/client).
type fakeAPI struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// envelope: new plans self-approve when true; otherwise they need a person.
	envelope bool
	// approveAfterGets: a "person" approves a human plan after this many
	// plan reads; -1 never.
	approveAfterGets int
	// quoteChangedOnce: the next execute answers 409 quote_changed.
	quoteChangedOnce bool
	// instanceStartsAfterGets: reads before a launched instance is running.
	instanceStartsAfterGets int
	// noSSHRead: the token holds ssh:write and not ssh:read, as a Quick
	// Connect key does, so listing SSH keys is refused.
	noSSHRead bool

	seq       int
	plans     map[string]*fakePlan
	instances map[string]map[string]any
	instGets  map[string]int
	volumes   map[string]map[string]any
	keys      map[string]map[string]any
	endpoints map[string]map[string]any
	quotes    int
}

type fakePlan struct {
	id, status, mode, kind string
	reads                  int
	spec                   map[string]any
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{
		t: t, envelope: true, approveAfterGets: -1, instanceStartsAfterGets: 1,
		plans: map[string]*fakePlan{}, instances: map[string]map[string]any{}, instGets: map[string]int{},
		volumes: map[string]map[string]any{}, keys: map[string]map[string]any{}, endpoints: map[string]map[string]any{},
	}
	mux := http.NewServeMux()
	f.routes(mux)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fakeToken {
			writeProblem(w, http.StatusUnauthorized, "unauthorized", "bad token", nil)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) id(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%d", prefix, f.seq)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, status int, code, detail string, extra map[string]any) {
	body := map[string]any{"status": status, "code": code, "detail": detail, "title": http.StatusText(status)}
	for k, v := range extra {
		body[k] = v
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeDetail answers the way the real API answers an HTTPException: its
// exception handler wraps the detail in {"ok": false, "error": {...}}.
func writeDetail(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": false, "error": map[string]any{"code": "http_error", "message": detail},
	})
}

func decode(r *http.Request) map[string]any {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}
	return body
}

func (f *fakeAPI) mode() string {
	if f.envelope {
		return "standing_policy"
	}
	return "human"
}

func (f *fakeAPI) preview(p *fakePlan) map[string]any {
	reasons := []string{}
	if p.mode == "human" {
		reasons = []string{"daily ceiling 1000000 would be exceeded (projected 1450000)"}
	}
	out := map[string]any{
		"ok": true, "plan_id": nil, "status": "awaiting_approval", "approval_mode": p.mode,
		"policy":       map[string]any{"allowed": true, "approval_mode": p.mode, "reasons": reasons},
		"estimate":     map[string]any{"currency": "CAD", "hourly_burn_micros": 450000, "estimate_micros": 450000},
		"availability": map[string]any{"feasible": true, "eligible_hosts": 1},
	}
	if p.id != "" {
		out["plan_id"] = p.id
		out["approval_url"] = "/dashboard/launch-plans/" + p.id
	}
	return out
}

func (f *fakeAPI) newPlan(kind string, spec map[string]any) *fakePlan {
	p := &fakePlan{id: f.id("plan"), status: "awaiting_approval", mode: f.mode(), kind: kind, spec: spec}
	f.plans[p.id] = p
	return p
}

func (f *fakeAPI) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/spend-envelope", func(w http.ResponseWriter, r *http.Request) {
		env := any(nil)
		if f.envelope {
			env = map[string]any{"client_id": "client-1", "daily_spend_max_micros": 50_000_000}
		}
		writeJSON(w, map[string]any{"ok": true, "client_id": "client-1", "envelope": env})
	})

	// ── launch plans ──
	mux.HandleFunc("POST /api/v1/launch-plans/quote", func(w http.ResponseWriter, r *http.Request) {
		spec := decode(r)
		if spec["gpu_model"] == "NOPE" {
			writeProblem(w, 422, "invalid_spec", "the launch spec failed validation",
				map[string]any{"errors": []any{map[string]any{"code": "unknown_gpu_model", "field": "gpu_model"}}})
			return
		}
		f.quotes++
		writeJSON(w, f.preview(&fakePlan{mode: f.mode()}))
	})
	mux.HandleFunc("POST /api/v1/launch-plans", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, f.preview(f.newPlan("instance", decode(r))))
	})
	mux.HandleFunc("GET /api/v1/launch-plans/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := f.plans[r.PathValue("id")]
		if !ok {
			writeProblem(w, 404, "plan_not_found", "not found", nil)
			return
		}
		p.reads++
		if p.status == "awaiting_approval" && f.approveAfterGets >= 0 && p.reads > f.approveAfterGets {
			p.status = "approved"
		}
		writeJSON(w, map[string]any{"ok": true, "plan": map[string]any{"plan_id": p.id, "status": p.status, "approval_mode": p.mode}})
	})
	mux.HandleFunc("POST /api/v1/launch-plans/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		p, ok := f.plans[r.PathValue("id")]
		if !ok {
			writeProblem(w, 404, "plan_not_found", "not found", nil)
			return
		}
		if p.mode != "standing_policy" {
			writeProblem(w, 409, "human_approval_required", "this plan requires an interactive human approval", nil)
			return
		}
		p.status = "approved"
		writeJSON(w, map[string]any{"ok": true, "plan": map[string]any{"plan_id": p.id, "status": p.status}})
	})
	mux.HandleFunc("POST /api/v1/launch-plans/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if p, ok := f.plans[r.PathValue("id")]; ok {
			p.status = "revoked"
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/v1/launch-plans/{id}/execute", func(w http.ResponseWriter, r *http.Request) {
		p, ok := f.plans[r.PathValue("id")]
		if !ok || p.kind != "instance" {
			writeProblem(w, 404, "plan_not_found", "not found", nil)
			return
		}
		if p.status != "approved" {
			writeProblem(w, 409, "plan_not_approved", "plan must be approved before execute", nil)
			return
		}
		if f.quoteChangedOnce {
			f.quoteChangedOnce = false
			p.status = "superseded"
			replacement := f.newPlan("instance", p.spec)
			writeProblem(w, 409, "quote_changed", "the price moved", map[string]any{"replacement_plan": f.preview(replacement)})
			return
		}
		p.status = "succeeded"
		jobID := f.id("job")
		inst := map[string]any{
			"job_id": jobID, "name": p.spec["name"], "status": "queued", "gpu_model": p.spec["gpu_model"],
			"num_gpus": p.spec["num_gpus"], "pricing_mode": p.spec["pricing_mode"],
			// The real API returns connection secrets too; the provider must not store them.
			"ssh_password": "must-not-reach-state",
		}
		f.instances[jobID] = inst
		writeJSON(w, map[string]any{"ok": true, "job": map[string]any{"job_id": jobID, "phase": "pending"}})
	})

	// ── instances ──
	mux.HandleFunc("GET /instance/{id}", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := f.instances[r.PathValue("id")]
		if !ok {
			writeDetail(w, 404, "Instance not found")
			return
		}
		id := r.PathValue("id")
		f.instGets[id]++
		if inst["status"] == "queued" && f.instGets[id] > f.instanceStartsAfterGets {
			inst["status"] = "running"
			inst["host_id"] = "host-1"
			inst["host_ip"] = "203.0.113.10"
			inst["ssh_port"] = 2222
			inst["host_key_fingerprint"] = "SHA256:abc"
			inst["rate_per_hour_cad"] = 0.45
		}
		writeJSON(w, map[string]any{"instance": inst})
	})
	mux.HandleFunc("POST /instances/{id}/terminate", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := f.instances[r.PathValue("id")]
		if !ok {
			writeDetail(w, 404, "Instance not found")
			return
		}
		if inst["status"] == "queued" {
			writeDetail(w, 400, "no_active_fenced_authority")
			return
		}
		inst["status"] = "terminated"
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /instances/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := f.instances[r.PathValue("id")]
		if !ok {
			writeDetail(w, 404, "Instance not found")
			return
		}
		inst["status"] = "cancelled"
		writeJSON(w, map[string]any{"ok": true})
	})

	// ── volumes ──
	mux.HandleFunc("POST /api/v2/volumes", func(w http.ResponseWriter, r *http.Request) {
		body := decode(r)
		name, _ := body["name"].(string)
		for _, v := range f.volumes {
			if v["name"] == name {
				writeDetail(w, 400, fmt.Sprintf("A volume named %q already exists", name))
				return
			}
		}
		if body["preview"] == true {
			writeJSON(w, map[string]any{"ok": true, "preview": true, "created": false, "volume": map[string]any{"name": name}})
			return
		}
		region, _ := body["region"].(string)
		if region == "" {
			region = "ca-on"
		}
		id := f.id("vol")
		f.volumes[id] = map[string]any{
			"volume_id": id, "name": name, "size_gb": body["size_gb"], "region": region,
			"encrypted": body["encrypted"], "status": "available", "attached_to": nil,
		}
		writeJSON(w, map[string]any{"ok": true, "volume": f.volumes[id]})
	})
	mux.HandleFunc("GET /api/v2/volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := f.volumes[r.PathValue("id")]
		if !ok {
			writeDetail(w, 404, "Volume not found")
			return
		}
		writeJSON(w, map[string]any{"ok": true, "volume": v})
	})
	mux.HandleFunc("PATCH /api/v2/volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := f.volumes[r.PathValue("id")]
		if !ok {
			writeDetail(w, 404, "Volume not found")
			return
		}
		v["name"] = decode(r)["name"]
		writeJSON(w, map[string]any{"ok": true, "volume": v})
	})
	mux.HandleFunc("DELETE /api/v2/volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := f.volumes[r.PathValue("id")]
		if !ok {
			writeDetail(w, 404, "Volume not found")
			return
		}
		if v["status"] == "attached" {
			writeDetail(w, 409, "Volume has active attachments")
			return
		}
		delete(f.volumes, r.PathValue("id"))
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/v2/volumes/{id}/attach", func(w http.ResponseWriter, r *http.Request) {
		v, ok := f.volumes[r.PathValue("id")]
		if !ok {
			writeDetail(w, 404, "Volume not found")
			return
		}
		body := decode(r)
		if _, ok := f.instances[fmt.Sprint(body["instance_id"])]; !ok || v["status"] != "available" {
			writeDetail(w, 409, "Volume not available for attachment")
			return
		}
		v["status"] = "attached"
		v["attached_to"] = body["instance_id"]
		writeJSON(w, map[string]any{"ok": true, "attachment": map[string]any{"volume_id": v["volume_id"]}})
	})
	mux.HandleFunc("POST /api/v2/volumes/{id}/detach", func(w http.ResponseWriter, r *http.Request) {
		v, ok := f.volumes[r.PathValue("id")]
		if !ok {
			writeDetail(w, 404, "Volume not found")
			return
		}
		v["status"] = "available"
		v["attached_to"] = nil
		writeJSON(w, map[string]any{"ok": true})
	})

	// ── SSH keys ──
	mux.HandleFunc("POST /api/ssh/keys", func(w http.ResponseWriter, r *http.Request) {
		body := decode(r)
		pub := strings.Join(strings.Fields(fmt.Sprint(body["public_key"])), " ")
		for _, k := range f.keys {
			if k["public_key"] == pub {
				writeDetail(w, 409, "This key is already added")
				return
			}
		}
		id := f.id("key")
		f.keys[id] = map[string]any{"id": id, "name": body["name"], "public_key": pub, "fingerprint": "SHA256:" + id, "created_at": 1.7e9}
		writeJSON(w, map[string]any{"ok": true, "id": id, "name": body["name"], "fingerprint": "SHA256:" + id})
	})
	mux.HandleFunc("GET /api/ssh/keys", func(w http.ResponseWriter, r *http.Request) {
		if f.noSSHRead {
			writeDetail(w, 403, "Insufficient scope — required: ssh:read; granted: instances:read, ssh:write")
			return
		}
		keys := []any{}
		for _, k := range f.keys {
			keys = append(keys, k)
		}
		writeJSON(w, map[string]any{"ok": true, "keys": keys})
	})
	mux.HandleFunc("DELETE /api/ssh/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := f.keys[r.PathValue("id")]; !ok {
			writeDetail(w, 404, "SSH key not found")
			return
		}
		delete(f.keys, r.PathValue("id"))
		writeJSON(w, map[string]any{"ok": true})
	})

	// ── serverless ──
	mux.HandleFunc("POST /api/v1/serverless/endpoint-plans", func(w http.ResponseWriter, r *http.Request) {
		p := f.newPlan("endpoint", decode(r))
		out := f.preview(p)
		out["approval_state"] = p.status
		out["estimate"] = map[string]any{"currency": "CAD", "approved_max_micros": 1_350_000}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /api/v1/serverless/endpoint-plans/{id}/execute", func(w http.ResponseWriter, r *http.Request) {
		p, ok := f.plans[r.PathValue("id")]
		if !ok || p.kind != "endpoint" {
			writeProblem(w, 404, "plan_not_found", "not found", nil)
			return
		}
		if p.status != "approved" {
			writeProblem(w, 409, "approval_required", "approve the endpoint plan before execution", nil)
			return
		}
		p.status = "succeeded"
		id := f.id("ep")
		num := func(k string, d float64) any {
			if v, ok := p.spec[k]; ok && v != nil {
				return v
			}
			return d
		}
		mode, _ := p.spec["mode"].(string)
		if mode == "" {
			mode = "preset"
		}
		f.endpoints[id] = map[string]any{
			"endpoint_id": id, "name": p.spec["name"], "mode": mode, "model_ref": p.spec["model_ref"],
			"gpu_tier": p.spec["gpu_tier"], "gpu_count": num("gpu_count", 1), "region": "ca-on", "status": "active",
			"min_workers": num("min_workers", 1), "max_workers": num("max_workers", 3),
			"max_concurrency": num("max_concurrency", 1), "idle_timeout_sec": num("idle_timeout_sec", 60),
		}
		writeJSON(w, map[string]any{"ok": true, "endpoint": f.endpoints[id], "plan_id": p.id})
	})
	mux.HandleFunc("GET /api/v2/serverless/endpoints/{id}", func(w http.ResponseWriter, r *http.Request) {
		ep, ok := f.endpoints[r.PathValue("id")]
		if !ok {
			writeDetail(w, 404, "Endpoint not found")
			return
		}
		writeJSON(w, map[string]any{"ok": true, "endpoint": ep})
	})
	mux.HandleFunc("PATCH /api/v2/serverless/endpoints/{id}", func(w http.ResponseWriter, r *http.Request) {
		ep, ok := f.endpoints[r.PathValue("id")]
		if !ok {
			writeDetail(w, 404, "Endpoint not found")
			return
		}
		for k, v := range decode(r) {
			if k != "name" {
				f.t.Errorf("the provider patched %q; only name may be patched", k)
			}
			ep[k] = v
		}
		writeJSON(w, map[string]any{"ok": true, "endpoint": ep})
	})
	mux.HandleFunc("DELETE /api/v2/serverless/endpoints/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := f.endpoints[r.PathValue("id")]; !ok {
			writeDetail(w, 404, "Endpoint not found")
			return
		}
		delete(f.endpoints, r.PathValue("id"))
		writeJSON(w, map[string]any{"ok": true})
	})

	// ── catalog ──
	mux.HandleFunc("GET /api/v2/gpu/available", func(w http.ResponseWriter, r *http.Request) {
		gpus := []any{
			map[string]any{"gpu_model": "RTX 2060", "vram_gb": 6, "region": "ca-on", "province": "ON", "count_available": 1, "price_per_hour_cad": 0.45, "service_tiers_available": []string{"standard"}},
			map[string]any{"gpu_model": "RTX 3060", "vram_gb": 12, "region": "ca-on", "province": "ON", "count_available": 2, "price_per_hour_cad": 0.6, "service_tiers_available": nil},
		}
		if region := r.URL.Query().Get("region"); region != "" && region != "ca-on" {
			gpus = []any{}
		}
		writeJSON(w, map[string]any{"gpus": gpus, "observed_at": "2026-11-27T00:00:00Z"})
	})
	mux.HandleFunc("GET /api/pricing/reference", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "currency": "CAD", "source": "gpu_pricing", "reference": []any{
			map[string]any{"gpu_model": "RTX 3060", "vram_gb": 12, "on_demand_cad": 0.6, "spot_cad": 0.3, "reserved_1mo_cad": nil, "reserved_3mo_cad": nil, "reserved_1yr_cad": nil},
			map[string]any{"gpu_model": "RTX 2060", "vram_gb": 6, "on_demand_cad": 0.45, "spot_cad": nil, "reserved_1mo_cad": 0.4, "reserved_3mo_cad": 0.38, "reserved_1yr_cad": 0.35},
		}})
	})
}

// planStatuses lists every plan's status, for assertions.
func (f *fakeAPI) planStatuses() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for id, p := range f.plans {
		out[id] = p.status
	}
	return out
}

func (f *fakeAPI) count(what string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch what {
	case "instances":
		n := 0
		for _, inst := range f.instances {
			if inst["status"] == "queued" || inst["status"] == "running" {
				n++
			}
		}
		return n
	case "volumes":
		return len(f.volumes)
	case "keys":
		return len(f.keys)
	case "endpoints":
		return len(f.endpoints)
	case "quotes":
		return f.quotes
	}
	return -1
}
