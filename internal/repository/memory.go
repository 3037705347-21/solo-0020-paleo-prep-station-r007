package repository

import (
	"sort"
	"sync"
	"time"

	"example.com/paleo-prep-station/internal/domain"
)

var defaultWorkbenchIDs = []string{"bench-a", "bench-b", "bench-c"}

type state struct {
	specimens             map[string]domain.Specimen
	plans                 map[string]domain.PrepPlan
	progress              map[string][]domain.ProgressEntry
	releases              map[string]domain.ReleaseRecord
	workbenches           map[string]domain.Workbench
	workbenchReservations map[string]string
}

type MemoryRepository struct {
	mu    sync.Mutex
	state state
}

func NewMemory() *MemoryRepository {
	now := time.Now().UTC()
	workbenches := make(map[string]domain.Workbench, len(defaultWorkbenchIDs))
	for _, id := range defaultWorkbenchIDs {
		workbenches[id] = domain.Workbench{
			ID:        id,
			Status:    domain.WorkbenchAvailable,
			Version:   1,
			UpdatedAt: now,
		}
	}
	return &MemoryRepository{
		state: state{
			specimens:             make(map[string]domain.Specimen),
			plans:                 make(map[string]domain.PrepPlan),
			progress:              make(map[string][]domain.ProgressEntry),
			releases:              make(map[string]domain.ReleaseRecord),
			workbenches:           workbenches,
			workbenchReservations: make(map[string]string),
		},
	}
}

func (r *MemoryRepository) Read(fn func(*Mutation) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fn(cloneMutation(r.state))
}

func (r *MemoryRepository) Mutate(fn func(*Mutation) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	tx := cloneMutation(r.state)
	if err := fn(tx); err != nil {
		return err
	}
	r.state = tx.snapshot()
	return nil
}

func cloneMutation(source state) *Mutation {
	copied := state{
		specimens:             make(map[string]domain.Specimen, len(source.specimens)),
		plans:                 make(map[string]domain.PrepPlan, len(source.plans)),
		progress:              make(map[string][]domain.ProgressEntry, len(source.progress)),
		releases:              make(map[string]domain.ReleaseRecord, len(source.releases)),
		workbenches:           make(map[string]domain.Workbench, len(source.workbenches)),
		workbenchReservations: make(map[string]string, len(source.workbenchReservations)),
	}
	for key, value := range source.specimens {
		copied.specimens[key] = value
	}
	for key, value := range source.plans {
		copied.plans[key] = value
	}
	for key, values := range source.progress {
		copied.progress[key] = append([]domain.ProgressEntry(nil), values...)
	}
	for key, value := range source.releases {
		copied.releases[key] = value
	}
	for key, value := range source.workbenches {
		copied.workbenches[key] = value
	}
	for key, value := range source.workbenchReservations {
		copied.workbenchReservations[key] = value
	}
	return &Mutation{state: copied}
}

func (r *MemoryRepository) FindAvailableWorkbench() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range defaultWorkbenchIDs {
		workbench, ok := r.state.workbenches[id]
		if !ok || workbench.Status != domain.WorkbenchAvailable {
			continue
		}
		if _, reserved := r.state.workbenchReservations[id]; reserved {
			continue
		}
		return id, true
	}
	return "", false
}

func (r *MemoryRepository) ReserveWorkbench(planID, workbenchID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.workbenchReservations[workbenchID] = planID
	return nil
}

type Mutation struct {
	state state
}

func (m *Mutation) snapshot() state {
	return m.state
}

func (m *Mutation) GetSpecimen(id string) (domain.Specimen, bool) {
	value, ok := m.state.specimens[id]
	return value, ok
}

func (m *Mutation) FindSpecimenByCatalog(catalogNumber string) (domain.Specimen, bool) {
	for _, value := range m.state.specimens {
		if value.CatalogNumber == catalogNumber {
			return value, true
		}
	}
	return domain.Specimen{}, false
}

func (m *Mutation) ListSpecimens() []domain.Specimen {
	values := make([]domain.Specimen, 0, len(m.state.specimens))
	for _, value := range m.state.specimens {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.Before(values[j].CreatedAt)
	})
	return values
}

func (m *Mutation) PutSpecimen(value domain.Specimen) {
	m.state.specimens[value.ID] = value
}

func (m *Mutation) GetPlan(id string) (domain.PrepPlan, bool) {
	value, ok := m.state.plans[id]
	return value, ok
}

func (m *Mutation) ListPlansForSpecimen(specimenID string) []domain.PrepPlan {
	values := make([]domain.PrepPlan, 0)
	for _, value := range m.state.plans {
		if value.SpecimenID == specimenID {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.Before(values[j].CreatedAt)
	})
	return values
}

func (m *Mutation) PutPlan(value domain.PrepPlan) {
	m.state.plans[value.ID] = value
}

func (m *Mutation) HasOpenPlan(specimenID string) bool {
	for _, value := range m.state.plans {
		if value.SpecimenID == specimenID && value.IsOpen() {
			return true
		}
	}
	return false
}

func (m *Mutation) ProgressForPlan(planID string) []domain.ProgressEntry {
	return append([]domain.ProgressEntry(nil), m.state.progress[planID]...)
}

func (m *Mutation) AppendProgress(value domain.ProgressEntry) {
	m.state.progress[value.PlanID] = append(m.state.progress[value.PlanID], value)
}

func (m *Mutation) ReleaseForSpecimen(specimenID string) (domain.ReleaseRecord, bool) {
	for _, value := range m.state.releases {
		if value.SpecimenID == specimenID {
			return value, true
		}
	}
	return domain.ReleaseRecord{}, false
}

func (m *Mutation) PutRelease(value domain.ReleaseRecord) {
	m.state.releases[value.ID] = value
}

func (m *Mutation) ListWorkbenches() []domain.Workbench {
	values := make([]domain.Workbench, 0, len(m.state.workbenches))
	for id, value := range m.state.workbenches {
		if planID, reserved := m.state.workbenchReservations[id]; reserved && value.Status == domain.WorkbenchAvailable {
			value.Status = domain.WorkbenchOccupied
			value.PlanID = planID
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].ID < values[j].ID
	})
	return values
}

func (m *Mutation) GetWorkbench(id string) (domain.Workbench, bool) {
	value, ok := m.state.workbenches[id]
	return value, ok
}

func (m *Mutation) PutWorkbench(value domain.Workbench) {
	m.state.workbenches[value.ID] = value
}

func (m *Mutation) AssignWorkbench(planID, workbenchID string, now time.Time) error {
	workbench, ok := m.state.workbenches[workbenchID]
	if !ok {
		return nil
	}
	if workbench.Status == domain.WorkbenchMaintenance {
		return nil
	}
	workbench.Status = domain.WorkbenchOccupied
	workbench.PlanID = planID
	workbench.Reason = ""
	workbench.Version++
	workbench.UpdatedAt = now.UTC()
	m.state.workbenches[workbenchID] = workbench
	return nil
}

func (m *Mutation) MarkWorkbenchCleaned(planID string, now time.Time) {
	for id, workbench := range m.state.workbenches {
		if workbench.PlanID != planID {
			continue
		}
		workbench.Version++
		workbench.UpdatedAt = now.UTC()
		m.state.workbenches[id] = workbench
	}
}

func (m *Mutation) ReleaseWorkbench(planID string, now time.Time) {
	for id, reservedPlanID := range m.state.workbenchReservations {
		if reservedPlanID == planID {
			delete(m.state.workbenchReservations, id)
		}
	}
	for id, workbench := range m.state.workbenches {
		if workbench.PlanID != planID {
			continue
		}
		workbench.Status = domain.WorkbenchAvailable
		workbench.PlanID = ""
		workbench.Reason = ""
		workbench.Version++
		workbench.UpdatedAt = now.UTC()
		m.state.workbenches[id] = workbench
	}
}
