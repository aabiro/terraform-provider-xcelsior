package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewRefusesPlainHTTPExceptForLocalhost(t *testing.T) {
	if _, err := New("http://api.example.com", "tok", "", nil); err == nil {
		t.Fatal("plain http to a remote host was accepted")
	}
	for _, ok := range []string{"http://localhost:9500", "http://127.0.0.1:9500", "https://api.example.com"} {
		if _, err := New(ok, "tok", "", nil); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	if _, err := New("https://api.example.com", " ", "", nil); err == nil {
		t.Fatal("an empty token was accepted")
	}
}

func TestProblemJSONDecodesCodeAndExtensionMembers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"Conflict","status":409,"code":"quote_changed","detail":"moved","replacement_plan":{"plan_id":"p2","approval_mode":"standing_policy"}}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "tok", "", nil)
	_, err := c.ExecuteLaunchPlan(context.Background(), "p1")
	if !HasCode(err, "quote_changed") {
		t.Fatalf("code not decoded: %v", err)
	}
	p, ok := ReplacementPlan(err)
	if !ok || p.PlanID != "p2" || p.ApprovalMode != "standing_policy" {
		t.Fatalf("replacement not decoded: %+v %v", p, ok)
	}
}

func TestTheAPIErrorEnvelopeDecodes(t *testing.T) {
	// The bodies api/api.py's exception handlers write.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/keys"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"http_error","message":"Insufficient scope — required: ssh:read; granted: ssh:write"}}`))
		case strings.HasSuffix(r.URL.Path, "/keys"):
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"ssh_key_exists","message":"This key is already added","key_id":"k1"}}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"legacy FastAPI shape"}`))
		default:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"validation_error","message":"Request validation failed","details":[{"loc":["body","name"],"type":"missing","msg":"Field required"}]}}`))
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "tok", "", nil)
	ctx := context.Background()
	var apiErr *APIError

	_, err := c.ListSSHKeys(ctx)
	if !IsMissingScope(err, "ssh:read") || !errors.As(err, &apiErr) || apiErr.Code != "" {
		t.Fatalf("scope refusal not decoded: %#v", err)
	}
	if IsMissingScope(err, "ssh:write") {
		t.Fatal("a scope the token holds was reported missing")
	}
	_, err = c.CreateSSHKey(ctx, "k", "ssh-ed25519 AAAA")
	if !HasCode(err, "ssh_key_exists") || !errors.As(err, &apiErr) ||
		apiErr.Detail != "This key is already added" || apiErr.Body["key_id"] != "k1" {
		t.Fatalf("coded envelope not decoded: %#v", err)
	}
	_, err = c.CreateVolume(ctx, VolumeCreate{})
	if !HasCode(err, "validation_error") || !strings.Contains(err.Error(), "Field required") {
		t.Fatalf("validation error not decoded: %v", err)
	}
	if err = c.DeleteSSHKey(ctx, "k1"); !IsNotFound(err) || !strings.Contains(err.Error(), "legacy FastAPI shape") {
		t.Fatalf("FastAPI detail not decoded: %v", err)
	}
}

func TestBearerTokenAndUserAgentAreSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || !strings.HasPrefix(r.Header.Get("User-Agent"), "tf-test") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"client_id":null,"envelope":null,"usage":null}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "tok", "tf-test/1", nil)
	env, err := c.GetSpendEnvelope(context.Background())
	if err != nil || env.Envelope != nil {
		t.Fatalf("%v %+v", err, env)
	}
}

func TestCADMicrosRoundsToTheNearestMicro(t *testing.T) {
	for in, want := range map[float64]int64{0.45: 450_000, 1.2345678: 1_234_568, 0: 0, 2.5: 2_500_000} {
		if got := CADMicros(in); got != want {
			t.Fatalf("CADMicros(%v) = %d, want %d", in, got, want)
		}
	}
}

// ── RunPlan ─────────────────────────────────────────────────────────

// planServer is a fake of the plan routes with scripted behaviour.
type planServer struct {
	mu           sync.Mutex
	mode         string // approval_mode new plans get
	approveAfter int    // GETs before a "person" approves a human plan; -1 never
	quoteChanges int    // executes that answer quote_changed first
	spendRaces   int    // executes that answer daily_spend_limit_exceeded first
	execErrors   []int  // statuses executes answer, in order, before the scripted behaviour
	plans        map[string]string
	gets         map[string]int
	revoked      []string
	created      int
	executed     []string
}

func newPlanServer(mode string) *planServer {
	return &planServer{mode: mode, approveAfter: -1, plans: map[string]string{}, gets: map[string]int{}}
}

func (s *planServer) newPlan() map[string]any {
	s.created++
	id := "plan-" + string(rune('a'+s.created-1))
	s.plans[id] = "awaiting_approval"
	return map[string]any{
		"ok": true, "plan_id": id, "status": "awaiting_approval", "approval_mode": s.mode,
		"policy":       map[string]any{"allowed": true, "approval_mode": s.mode, "reasons": []string{"per-action ceiling 1 would be exceeded"}},
		"approval_url": "/dashboard/launch-plans/" + id,
	}
}

func problem(w http.ResponseWriter, status int, code string, extra map[string]any) {
	body := map[string]any{"status": status, "code": code, "detail": code}
	for k, v := range extra {
		body[k] = v
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *planServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/launch-plans"), "/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/launch-plans":
		_ = json.NewEncoder(w).Encode(s.newPlan())
	case len(parts) == 2 && r.Method == http.MethodGet:
		id := parts[1]
		s.gets[id]++
		if s.plans[id] == "awaiting_approval" && s.approveAfter >= 0 && s.gets[id] > s.approveAfter {
			s.plans[id] = "approved"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"plan": map[string]any{"plan_id": id, "status": s.plans[id]}})
	case len(parts) == 3 && parts[2] == "approve":
		id := parts[1]
		if s.mode != "standing_policy" {
			problem(w, 409, "human_approval_required", nil)
			return
		}
		s.plans[id] = "approved"
		_ = json.NewEncoder(w).Encode(map[string]any{"plan": map[string]any{"plan_id": id, "status": "approved"}})
	case len(parts) == 3 && parts[2] == "revoke":
		s.plans[parts[1]] = "revoked"
		s.revoked = append(s.revoked, parts[1])
		_, _ = w.Write([]byte(`{"ok":true}`))
	case len(parts) == 3 && parts[2] == "execute":
		id := parts[1]
		if len(s.execErrors) > 0 {
			status := s.execErrors[0]
			s.execErrors = s.execErrors[1:]
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"http_error","message":"scripted failure"}}`))
			return
		}
		if s.plans[id] != "approved" {
			problem(w, 409, "plan_not_approved", nil)
			return
		}
		if s.quoteChanges > 0 {
			s.quoteChanges--
			s.plans[id] = "superseded"
			problem(w, 409, "quote_changed", map[string]any{"replacement_plan": s.newPlan()})
			return
		}
		if s.spendRaces > 0 {
			s.spendRaces--
			problem(w, 409, "daily_spend_limit_exceeded", nil)
			return
		}
		s.plans[id] = "succeeded"
		s.executed = append(s.executed, id)
		_ = json.NewEncoder(w).Encode(map[string]any{"job": map[string]any{"job_id": "job-" + id}})
	default:
		http.NotFound(w, r)
	}
}

func runInstancePlan(t *testing.T, s *planServer, opts ApprovalOptions) (string, error) {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "tok", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Millisecond
	}
	return c.RunPlan(context.Background(), PlanStep{
		Create: func(ctx context.Context) (*Preview, error) { return c.CreateLaunchPlan(ctx, InstanceSpec{Name: "x"}) },
		Execute: func(ctx context.Context, id string) (string, error) {
			out, err := c.ExecuteLaunchPlan(ctx, id)
			if err != nil {
				return "", err
			}
			return out.Job.JobID, nil
		},
	}, opts)
}

func TestAPlanInsideTheEnvelopeApprovesItselfAndExecutes(t *testing.T) {
	s := newPlanServer("standing_policy")
	id, err := runInstancePlan(t, s, ApprovalOptions{})
	if err != nil || id != "job-plan-a" {
		t.Fatalf("%q %v", id, err)
	}
	if len(s.revoked) != 0 {
		t.Fatalf("revoked %v", s.revoked)
	}
}

func TestAPlanOutsideTheEnvelopeFailsAtOnceAndIsRevokedByDefault(t *testing.T) {
	s := newPlanServer("human")
	var notified string
	_, err := runInstancePlan(t, s, ApprovalOptions{Notify: func(link string, _ []string) { notified = link }})
	var need *ApprovalRequiredError
	if !errors.As(err, &need) {
		t.Fatalf("want ApprovalRequiredError, got %v", err)
	}
	if !strings.HasSuffix(need.Link, "/dashboard/launch-plans/plan-a") || notified != need.Link {
		t.Fatalf("link %q notified %q", need.Link, notified)
	}
	if !strings.Contains(err.Error(), "per-action ceiling") {
		t.Fatalf("reasons missing from %q", err)
	}
	if len(s.revoked) != 1 || s.revoked[0] != "plan-a" || len(s.executed) != 0 {
		t.Fatalf("revoked %v executed %v", s.revoked, s.executed)
	}
}

func TestApplyWaitsForAPersonWithinTheApprovalTimeout(t *testing.T) {
	s := newPlanServer("human")
	s.approveAfter = 3
	id, err := runInstancePlan(t, s, ApprovalOptions{Timeout: 5 * time.Second})
	if err != nil || id != "job-plan-a" {
		t.Fatalf("%q %v", id, err)
	}
	if len(s.revoked) != 0 {
		t.Fatalf("an approved plan was revoked: %v", s.revoked)
	}
}

func TestAPlanRevokedByAPersonFailsWithoutExecuting(t *testing.T) {
	s := newPlanServer("human")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			s.mu.Lock()
			s.plans["plan-a"] = "revoked"
			s.mu.Unlock()
		}
		s.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "tok", "", nil)
	_, err := c.RunPlan(context.Background(), PlanStep{
		Create:  func(ctx context.Context) (*Preview, error) { return c.CreateLaunchPlan(ctx, InstanceSpec{Name: "x"}) },
		Execute: func(ctx context.Context, id string) (string, error) { t.Fatal("executed"); return "", nil },
	}, ApprovalOptions{Timeout: time.Second, PollInterval: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("%v", err)
	}
}

func TestAQuoteChangeIsFollowedToTheReplacementPlan(t *testing.T) {
	s := newPlanServer("standing_policy")
	s.quoteChanges = 2
	id, err := runInstancePlan(t, s, ApprovalOptions{})
	if err != nil || id != "job-plan-c" {
		t.Fatalf("%q %v", id, err)
	}
}

func TestQuoteChangesStopAfterThreeReplacements(t *testing.T) {
	s := newPlanServer("standing_policy")
	s.quoteChanges = 10
	_, err := runInstancePlan(t, s, ApprovalOptions{})
	if !HasCode(err, "quote_changed") {
		t.Fatalf("%v", err)
	}
	// The first plan and four replacements; the last is not followed, and is
	// revoked so it does not wait in anyone's dashboard.
	if s.created != 5 || len(s.executed) != 0 {
		t.Fatalf("created %d plans, executed %v", s.created, s.executed)
	}
	if len(s.revoked) != 1 || s.revoked[0] != "plan-e" {
		t.Fatalf("revoked %v, want [plan-e]", s.revoked)
	}
}

func TestLosingASpendWindowRaceRePreviews(t *testing.T) {
	s := newPlanServer("standing_policy")
	s.spendRaces = 1
	id, err := runInstancePlan(t, s, ApprovalOptions{})
	if err != nil || id != "job-plan-b" {
		t.Fatalf("%q %v", id, err)
	}
	if len(s.revoked) != 1 || s.revoked[0] != "plan-a" {
		t.Fatalf("the plan that lost the race was not withdrawn: revoked %v", s.revoked)
	}
}

func TestAnExecuteThatFailsWithdrawsItsApprovedPlan(t *testing.T) {
	s := newPlanServer("standing_policy")
	s.execErrors = []int{http.StatusInternalServerError}
	if _, err := runInstancePlan(t, s, ApprovalOptions{}); err == nil || !strings.Contains(err.Error(), "scripted failure") {
		t.Fatalf("want the execute error, got %v", err)
	}
	if len(s.revoked) != 1 || s.revoked[0] != "plan-a" {
		t.Fatalf("an approved plan was left standing: revoked %v", s.revoked)
	}
	if len(s.execErrors) != 0 || len(s.executed) != 0 {
		t.Fatal("a 500 was retried")
	}
}

func TestAnExecuteTheGatewayDroppedIsReplayedNotAbandoned(t *testing.T) {
	s := newPlanServer("standing_policy")
	s.execErrors = []int{http.StatusBadGateway, http.StatusServiceUnavailable}
	id, err := runInstancePlan(t, s, ApprovalOptions{})
	if err != nil || id != "job-plan-a" {
		t.Fatalf("%q %v", id, err)
	}
	if len(s.revoked) != 0 || s.created != 1 {
		t.Fatalf("revoked %v, created %d plans", s.revoked, s.created)
	}
}

func TestAnEnvelopeTightenedBeforeApproveFallsBackToAPerson(t *testing.T) {
	s := newPlanServer("standing_policy")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/approve") {
			problem(w, 409, "auto_approval_denied", nil)
			return
		}
		s.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "tok", "", nil)
	_, err := c.RunPlan(context.Background(), PlanStep{
		Create:  func(ctx context.Context) (*Preview, error) { return c.CreateLaunchPlan(ctx, InstanceSpec{Name: "x"}) },
		Execute: func(ctx context.Context, id string) (string, error) { t.Fatal("executed"); return "", nil },
	}, ApprovalOptions{PollInterval: time.Millisecond})
	var need *ApprovalRequiredError
	if !errors.As(err, &need) {
		t.Fatalf("%v", err)
	}
}
