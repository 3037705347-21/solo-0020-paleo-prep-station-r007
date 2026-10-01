package station_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

func wbNewService() *station.Service {
	return station.New(repository.NewMemory())
}

func wbRegisterAndPlan(ctx context.Context, svc *station.Service, catalog string) (domain.Specimen, domain.PrepPlan, error) {
	specimen, err := svc.RegisterSpecimen(ctx, contracts.RegisterSpecimenRequest{
		CatalogNumber: catalog,
		FieldLabel:    "北坡-07",
		TaxonHint:     "大型脊椎动物",
		MatrixKind:    "粉砂岩",
	})
	if err != nil {
		return domain.Specimen{}, domain.PrepPlan{}, err
	}
	plan, err := svc.CreatePlan(ctx, specimen.ID, contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面",
		Method:         "手工显微剥离",
		TargetExposure: 80,
	})
	if err != nil {
		return domain.Specimen{}, domain.PrepPlan{}, err
	}
	return specimen, plan, nil
}

func wbMustRegisterAndPlan(t *testing.T, ctx context.Context, svc *station.Service, catalog string) (domain.Specimen, domain.PrepPlan) {
	t.Helper()
	specimen, plan, err := wbRegisterAndPlan(ctx, svc, catalog)
	if err != nil {
		t.Fatalf("register+plan %s: %v", catalog, err)
	}
	return specimen, plan
}

func wbMustMaintenance(t *testing.T, ctx context.Context, svc *station.Service, id string, in bool, reason string) {
	t.Helper()
	if _, err := svc.SetWorkbenchMaintenance(ctx, id, in, reason); err != nil {
		t.Fatalf("set maintenance %s in=%v: %v", id, in, err)
	}
}

func wbByID(benches []domain.Workbench, id string) domain.Workbench {
	for _, b := range benches {
		if b.ID == id {
			return b
		}
	}
	return domain.Workbench{}
}

func wbConflictCode(err error) string {
	var appErr *contracts.Error
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return ""
}

// The approval and maintenance flows share one workbench pool. When they race
// for the last available workbench exactly one side may win, and the workbench
// can never end up claimed by both.
func TestWorkbenchApproveAndMaintenanceRace(t *testing.T) {
	ctx := context.Background()
	svc := wbNewService()

	// Only one bench stays available; the other two are already in repair.
	wbMustMaintenance(t, ctx, svc, "bench-a", true, "台面裂纹")
	wbMustMaintenance(t, ctx, svc, "bench-b", true, "夹具有旷量")

	specimen, plan := wbMustRegisterAndPlan(t, ctx, svc, "PPS-RACE-001")

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	var approveErr, maintenanceErr error
	go func() {
		defer wg.Done()
		<-start
		_, approveErr = svc.ApprovePlan(ctx, plan.ID, contracts.ApprovalRequest{ApprovedBy: "curator-lin"})
	}()
	go func() {
		defer wg.Done()
		<-start
		_, maintenanceErr = svc.SetWorkbenchMaintenance(ctx, "bench-c", true, "换班例行检修")
	}()
	close(start)
	wg.Wait()

	benches, err := svc.ListWorkbenches(ctx)
	if err != nil {
		t.Fatalf("list workbenches: %v", err)
	}
	benchC := wbByID(benches, "bench-c")

	switch {
	case approveErr == nil && maintenanceErr == nil:
		t.Fatal("both approval and maintenance succeeded on the same workbench")
	case approveErr != nil && maintenanceErr != nil:
		t.Fatalf("both sides failed: approve=%v maintenance=%v", approveErr, maintenanceErr)
	case approveErr == nil:
		if wbConflictCode(maintenanceErr) != "workbench_not_maintainable" {
			t.Fatalf("expected maintenance conflict, got %v", maintenanceErr)
		}
		if benchC.Status != domain.WorkbenchOccupied || benchC.PlanID != plan.ID {
			t.Fatalf("approval won but bench-c = %+v", benchC)
		}
		updated, err := svc.GetPlan(ctx, plan.ID)
		if err != nil {
			t.Fatalf("get plan: %v", err)
		}
		if updated.Plan.Status != domain.PlanApproved {
			t.Fatalf("expected approved plan, got %s", updated.Plan.Status)
		}
		spec, err := svc.GetSpecimen(ctx, specimen.ID)
		if err != nil {
			t.Fatalf("get specimen: %v", err)
		}
		if spec.Status != domain.SpecimenPreparing {
			t.Fatalf("expected preparing specimen, got %s", spec.Status)
		}
	default:
		if wbConflictCode(approveErr) != "workbench_unavailable" {
			t.Fatalf("expected workbench_unavailable, got %v", approveErr)
		}
		if benchC.Status != domain.WorkbenchMaintenance {
			t.Fatalf("maintenance won but bench-c = %+v", benchC)
		}
	}
}

// A rejected approval (non-draft plan) must not reserve or mark any workbench,
// and a failed repair attempt against an occupied bench must leave it intact.
func TestWorkbenchRejectedApprovalLeavesNoDanglingHold(t *testing.T) {
	ctx := context.Background()
	svc := wbNewService()

	_, plan := wbMustRegisterAndPlan(t, ctx, svc, "PPS-REJECT-001")
	if _, err := svc.ApprovePlan(ctx, plan.ID, contracts.ApprovalRequest{ApprovedBy: "curator-lin"}); err != nil {
		t.Fatalf("first approval: %v", err)
	}
	if _, err := svc.ApprovePlan(ctx, plan.ID, contracts.ApprovalRequest{ApprovedBy: "curator-zhao"}); wbConflictCode(err) != "plan_not_approvable" {
		t.Fatalf("expected plan_not_approvable, got %v", err)
	}

	benches, _ := svc.ListWorkbenches(ctx)
	occupiedID := ""
	occupied := 0
	for _, b := range benches {
		if b.Status == domain.WorkbenchOccupied {
			occupied++
			occupiedID = b.ID
			if b.PlanID != plan.ID {
				t.Fatalf("workbench %s references unexpected plan %s", b.ID, b.PlanID)
			}
		}
	}
	if occupied != 1 {
		t.Fatalf("expected exactly one occupied bench, got %d: %+v", occupied, benches)
	}

	if _, err := svc.SetWorkbenchMaintenance(ctx, occupiedID, true, "突发异响"); wbConflictCode(err) != "workbench_not_maintainable" {
		t.Fatalf("expected workbench_not_maintainable, got %v", err)
	}
	benches, _ = svc.ListWorkbenches(ctx)
	after := wbByID(benches, occupiedID)
	if after.Status != domain.WorkbenchOccupied || after.PlanID != plan.ID || after.Reason != "" {
		t.Fatalf("failed maintenance mutated the bench: %+v", after)
	}
}

// Completing a plan returns its workbench, which then becomes assignable to a
// later approval or eligible for repair.
func TestWorkbenchReleasedWhenPlanCompletes(t *testing.T) {
	ctx := context.Background()
	svc := wbNewService()

	// Tie up two benches in repair so completion must free the occupied one.
	wbMustMaintenance(t, ctx, svc, "bench-a", true, "台面裂纹")
	wbMustMaintenance(t, ctx, svc, "bench-b", true, "夹具有旷量")

	specimen, plan := wbMustRegisterAndPlan(t, ctx, svc, "PPS-DONE-001")
	if _, err := svc.ApprovePlan(ctx, plan.ID, contracts.ApprovalRequest{ApprovedBy: "curator-lin"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	benches, _ := svc.ListWorkbenches(ctx)
	occupiedID := ""
	for _, b := range benches {
		if b.Status == domain.WorkbenchOccupied {
			occupiedID = b.ID
		}
	}
	if occupiedID == "" {
		t.Fatal("no occupied bench after approval")
	}

	if _, err := svc.LogProgress(ctx, plan.ID, contracts.ProgressRequest{
		Stage: "表面加固", Percent: 100, RecordedBy: "preparator-chen",
	}); err != nil {
		t.Fatalf("complete plan: %v", err)
	}

	benches, _ = svc.ListWorkbenches(ctx)
	freed := wbByID(benches, occupiedID)
	if freed.Status != domain.WorkbenchAvailable || freed.PlanID != "" {
		t.Fatalf("workbench not released after completion: %+v", freed)
	}
	spec, _ := svc.GetSpecimen(ctx, specimen.ID)
	if spec.Status != domain.SpecimenStabilized {
		t.Fatalf("expected stabilized specimen, got %s", spec.Status)
	}

	// The freed bench can be handed over for repair at the shift handover.
	wbMustMaintenance(t, ctx, svc, occupiedID, true, "换班例行检修")

	// And the other benches returning from repair let a second approval claim a
	// bench from the shared pool.
	wbMustMaintenance(t, ctx, svc, "bench-a", false, "")
	wbMustMaintenance(t, ctx, svc, "bench-b", false, "")
	_, plan2 := wbMustRegisterAndPlan(t, ctx, svc, "PPS-DONE-002")
	if _, err := svc.ApprovePlan(ctx, plan2.ID, contracts.ApprovalRequest{ApprovedBy: "curator-lin"}); err != nil {
		t.Fatalf("second approval should reuse the pool: %v", err)
	}
}

// When no workbench is available the approval fails without touching the plan,
// the specimen, progress, or any release fact.
func TestWorkbenchShortageChangesNothing(t *testing.T) {
	ctx := context.Background()
	svc := wbNewService()

	wbMustMaintenance(t, ctx, svc, "bench-a", true, "检修")
	wbMustMaintenance(t, ctx, svc, "bench-b", true, "检修")
	wbMustMaintenance(t, ctx, svc, "bench-c", true, "检修")

	specimen, plan := wbMustRegisterAndPlan(t, ctx, svc, "PPS-NOBENCH-001")
	before, err := svc.GetSpecimen(ctx, specimen.ID)
	if err != nil {
		t.Fatalf("get specimen baseline: %v", err)
	}
	if _, err := svc.ApprovePlan(ctx, plan.ID, contracts.ApprovalRequest{ApprovedBy: "curator-lin"}); wbConflictCode(err) != "workbench_unavailable" {
		t.Fatalf("expected workbench_unavailable, got %v", err)
	}

	snapshot, err := svc.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if snapshot.Plan.Status != domain.PlanDraft || snapshot.Plan.ApprovedBy != "" || snapshot.Plan.WorkbenchID != "" {
		t.Fatalf("plan was mutated despite failed approval: %+v", snapshot.Plan)
	}
	if len(snapshot.Progress) != 0 {
		t.Fatalf("progress appeared after failed approval: %+v", snapshot.Progress)
	}
	spec, err := svc.GetSpecimen(ctx, specimen.ID)
	if err != nil {
		t.Fatalf("get specimen: %v", err)
	}
	if spec.Status != before.Status || spec.Version != before.Version {
		t.Fatalf("specimen changed unexpectedly: before=%+v after=%+v", before, spec)
	}
	benches, _ := svc.ListWorkbenches(ctx)
	for _, b := range benches {
		if b.Status != domain.WorkbenchMaintenance {
			t.Fatalf("workbench %s changed: %+v", b.ID, b)
		}
	}

	// Once a bench returns from repair the untouched draft can be approved.
	wbMustMaintenance(t, ctx, svc, "bench-c", false, "")
	if _, err := svc.ApprovePlan(ctx, plan.ID, contracts.ApprovalRequest{ApprovedBy: "curator-lin"}); err != nil {
		t.Fatalf("retry approval: %v", err)
	}
}

// Hammering approvals and maintenance switches concurrently must never produce
// a bench that is both occupied and in maintenance, or occupied by a plan that
// is missing, closed, or pointing at another bench.
func TestWorkbenchPoolConsistentUnderContention(t *testing.T) {
	ctx := context.Background()
	svc := wbNewService()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			catalog := "PPS-FUZZ-" + string(rune('1'+i)) + string(rune('1'+i))
			_, plan, err := wbRegisterAndPlan(ctx, svc, catalog)
			if err != nil {
				t.Errorf("register+plan %s: %v", catalog, err)
				return
			}
			_, _ = svc.ApprovePlan(ctx, plan.ID, contracts.ApprovalRequest{ApprovedBy: "curator-lin"})
			_, _ = svc.SetWorkbenchMaintenance(ctx, "bench-a", true, "例行检修")
			_, _ = svc.SetWorkbenchMaintenance(ctx, "bench-a", false, "")
		}(i)
	}
	wg.Wait()

	benches, _ := svc.ListWorkbenches(ctx)
	for _, b := range benches {
		switch b.Status {
		case domain.WorkbenchAvailable, domain.WorkbenchMaintenance:
			if b.PlanID != "" {
				t.Fatalf("%s bench carries plan: %+v", b.Status, b)
			}
		case domain.WorkbenchOccupied:
			snapshot, err := svc.GetPlan(ctx, b.PlanID)
			if err != nil {
				t.Fatalf("bench %s points to missing plan %s", b.ID, b.PlanID)
			}
			if !snapshot.Plan.IsOpen() || snapshot.Plan.WorkbenchID != b.ID {
				t.Fatalf("bench %s holds inconsistent plan: %+v", b.ID, snapshot.Plan)
			}
		default:
			t.Fatalf("unknown bench status: %+v", b)
		}
	}
}
