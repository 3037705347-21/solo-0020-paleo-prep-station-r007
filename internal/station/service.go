package station

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

type Service struct {
	repo *repository.MemoryRepository
	now  func() time.Time
	seq  atomic.Uint64
}

func New(repo *repository.MemoryRepository) *Service {
	if repo == nil {
		repo = repository.NewMemory()
	}
	return &Service{
		repo: repo,
		now:  func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) nextID(prefix string) string {
	var random [6]byte
	if _, err := rand.Read(random[:]); err == nil {
		return prefix + "-" + hex.EncodeToString(random[:])
	}
	local := s.seq.Add(1)
	return prefix + "-" + time.Now().UTC().Format("20060102150405") + "-" + hex.EncodeToString([]byte{byte(local)})
}

type PlanSnapshot struct {
	Plan     domain.PrepPlan        `json:"plan"`
	Progress []domain.ProgressEntry `json:"progress"`
}

func (s *Service) ListSpecimens(ctx context.Context) ([]domain.Specimen, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var values []domain.Specimen
	err := s.repo.Read(func(m *repository.Mutation) error {
		values = m.ListSpecimens()
		return nil
	})
	return values, err
}

func (s *Service) GetSpecimen(ctx context.Context, specimenID string) (domain.Specimen, error) {
	if err := ctx.Err(); err != nil {
		return domain.Specimen{}, err
	}
	var value domain.Specimen
	err := s.repo.Read(func(m *repository.Mutation) error {
		found, ok := m.GetSpecimen(specimenID)
		if !ok {
			return notFoundSpecimen(specimenID)
		}
		value = found
		return nil
	})
	return value, err
}

func (s *Service) ListWorkbenches(ctx context.Context) ([]domain.Workbench, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var values []domain.Workbench
	err := s.repo.Read(func(m *repository.Mutation) error {
		values = m.ListWorkbenches()
		return nil
	})
	return values, err
}

func (s *Service) SetWorkbenchMaintenance(
	ctx context.Context,
	workbenchID string,
	inMaintenance bool,
	reason string,
) (domain.Workbench, error) {
	if err := ctx.Err(); err != nil {
		return domain.Workbench{}, err
	}

	cleanedReason := strings.TrimSpace(reason)
	if inMaintenance && cleanedReason == "" {
		return domain.Workbench{}, contracts.Invalid("required_field", "送修原因不能为空")
	}
	if len([]rune(cleanedReason)) > 200 {
		return domain.Workbench{}, contracts.Invalid("field_too_long", "送修原因长度超出限制")
	}

	var updated domain.Workbench
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		workbench, ok := m.GetWorkbench(workbenchID)
		if !ok {
			return contracts.NotFound("workbench_not_found", "未找到工作位 "+workbenchID)
		}
		now := s.now()
		if inMaintenance {
			if workbench.Status != domain.WorkbenchAvailable {
				return contracts.Conflict("workbench_not_maintainable", "当前工作位不能进入送修")
			}
			workbench.Status = domain.WorkbenchMaintenance
			workbench.PlanID = ""
			workbench.Reason = cleanedReason
		} else {
			if workbench.Status != domain.WorkbenchMaintenance {
				return contracts.Conflict("workbench_not_in_maintenance", "当前工作位不在送修状态")
			}
			workbench.Status = domain.WorkbenchAvailable
			workbench.PlanID = ""
			workbench.Reason = ""
		}
		workbench.Version++
		workbench.UpdatedAt = now.UTC()
		m.PutWorkbench(workbench)
		updated = workbench
		return nil
	})
	return updated, err
}
